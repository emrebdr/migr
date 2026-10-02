//go:build integration

package mysql

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// briefWait is the bound a contention case gives up at, long enough to be a wait and short to fail in.
const briefWait = 300 * time.Millisecond

// queuePolls is how many reads a queued waiter is given to appear in, forbidigo leaving no clock.
const queuePolls = 500

// queuedQuery names one session blocked in GET_LOCK, which is what a KILL QUERY needs.
const queuedQuery = `
SELECT ID
  FROM information_schema.PROCESSLIST
 WHERE ID <> CONNECTION_ID()
   AND INFO LIKE ?
 ORDER BY ID
 LIMIT 1`

// waitingQuery counts the sessions blocked in GET_LOCK, which is what a lock queue looks like here.
//
// INFO rather than STATE, because the two servers spell the state differently and both carry the
// statement. It is visible only to a session with PROCESS, which these tests have (M9-T3).
const waitingQuery = `
SELECT count(*)
  FROM information_schema.PROCESSLIST
 WHERE ID <> CONNECTION_ID()
   AND INFO LIKE ?`

// lockSession opens a second session against one server, so contention has two parties.
func lockSession(t *testing.T, s server, table string, wait time.Duration) *mysqlConn {
	t.Helper()

	return openServer(t, s, driver.Options{HistoryTable: table, LockWait: wait})
}

// siblingName is a second table name for the same test, the same length so it still fits.
func siblingName(t *testing.T) string {
	t.Helper()

	const suffix = "_other"

	name := uniqueName(t)

	return name[:len(name)-len(suffix)] + suffix
}

// connectionID reads which connection a session is, which is what the lock belongs to.
func connectionID(t *testing.T, c *mysqlConn) string {
	t.Helper()

	return read(t, c, "SELECT CONNECTION_ID()")[0]
}

// heldBy reads which connection holds a session's lock, and "free" where nobody does.
func heldBy(t *testing.T, c *mysqlConn, name lockName) string {
	t.Helper()

	return read(t, c, "SELECT coalesce(CAST(IS_USED_LOCK(?) AS CHAR), 'free')", name.String())[0]
}

func TestASecondSessionIsBlockedUntilTheFirstReleases(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, first *mysqlConn) {
		second := lockSession(t, s, uniqueName(t), briefWait)
		second.lock = first.lock

		unlock, err := first.Lock(t.Context())
		if err != nil {
			t.Fatalf("the first acquire failed: %v", err)
		}

		if _, err := second.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
			t.Fatalf("the second acquire returned %v, want %v", err, driver.ErrLockTimeout)
		}

		if err := unlock(t.Context()); err != nil {
			t.Fatalf("the release failed: %v", err)
		}

		released, err := second.Lock(t.Context())
		if err != nil {
			t.Fatalf("the lock was not given back: %v", err)
		}

		if err := released(t.Context()); err != nil {
			t.Errorf("releasing the second hold failed: %v", err)
		}
	})
}

func TestAnUnrelatedInstallationIsNotBlocked(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, first *mysqlConn) {
		other := lockSession(t, s, siblingName(t), briefWait)

		if other.lock == first.lock {
			t.Fatalf("two installations derived one lock %s", first.lock)
		}

		unlock, err := first.Lock(t.Context())
		if err != nil {
			t.Fatalf("the first acquire failed: %v", err)
		}

		defer func() {
			if err := unlock(context.WithoutCancel(t.Context())); err != nil {
				t.Errorf("the release failed: %v", err)
			}
		}()

		release, err := other.Lock(t.Context())
		if err != nil {
			t.Fatalf("an unrelated installation was blocked: %v", err)
		}

		if err := release(t.Context()); err != nil {
			t.Errorf("releasing the unrelated hold failed: %v", err)
		}

		t.Log("Each session's history table is its own, so the derived names differ -- which is what")
		t.Log("keeps one server's installations from excluding each other, a user-level lock being")
		t.Log("global to the server rather than scoped to a database.")
	})
}

func TestTheFailureNamesTheConnectionAnOperatorCanKill(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		waiter := lockSession(t, s, uniqueName(t), briefWait)
		waiter.lock = holder.lock

		unlock, err := holder.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		_, err = waiter.Lock(t.Context())

		var busy *driver.LockBusyError
		if !errors.As(err, &busy) {
			t.Fatalf("the wait returned %v, which carries no holder", err)
		}

		if busy.Holder == nil {
			t.Fatalf("no holder was identified: %s", busy.Unidentified)
		}

		id := connectionID(t, holder)
		if !strings.Contains(busy.Holder.Process, id) {
			t.Errorf("the report names %q and the holder is connection %s", busy.Holder.Process, id)
		}

		if busy.Wait != briefWait {
			t.Errorf("the report says it waited %s, and the bound was %s", busy.Wait, briefWait)
		}

		if !strings.Contains(err.Error(), holder.lock.String()) {
			t.Errorf("the failure does not name the lock: %v", err)
		}

		if err := unlock(t.Context()); err != nil {
			t.Errorf("the release failed: %v", err)
		}

		t.Log("The connection id comes from IS_USED_LOCK, which needs no privilege, and")
		t.Log("TestAKilledHolderLeavesALockTheNextRunTakesImmediately is where the id this report")
		t.Log("gives is handed to KILL -- so the advice the message carries is the advice a test")
		t.Log("follows, which is ADR 0020's own precedent for PostgreSQL's pid.")
	})
}

func TestTheHoldersAccountAndStateAreWhatTheServerSaysTheyAre(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		waiter := lockSession(t, s, uniqueName(t), briefWait)
		waiter.lock = holder.lock

		unlock, err := holder.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		defer func() { _ = unlock(context.WithoutCancel(t.Context())) }()

		_, err = waiter.Lock(t.Context())

		var busy *driver.LockBusyError
		if !errors.As(err, &busy) || busy.Holder == nil {
			t.Fatalf("the wait returned %v with no holder", err)
		}

		if !strings.HasPrefix(busy.Holder.Application, "migr@") {
			t.Errorf("the holder is reported as %q, and these tests connect as migr",
				busy.Holder.Application)
		}

		if busy.Holder.Measured != measuredState {
			t.Errorf("the holder's duration is labeled %q, and its row was read", busy.Holder.Measured)
		}

		t.Log("The test user has PROCESS, because M9-T3's compose grant needed it for the InnoDB")
		t.Log("catalog, so this is the privileged rendering. The unprivileged one -- the connection")
		t.Log("id and nothing else -- is what a production grant produces and is asserted untagged,")
		t.Log("since a server cannot be asked to withhold a row it is allowed to show.")
	})
}

func TestAWaitThatGivesUpLeavesTheSessionUsable(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		waiter := lockSession(t, s, uniqueName(t), briefWait)
		waiter.lock = holder.lock

		unlock, err := holder.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		if _, err := waiter.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
			t.Fatalf("the wait returned %v", err)
		}

		if got := read(t, waiter, "SELECT 1")[0]; got != "1" {
			t.Errorf("the session that gave up answers %q", got)
		}

		if err := unlock(t.Context()); err != nil {
			t.Errorf("the release failed: %v", err)
		}

		t.Log("GET_LOCK's own argument is the bound, so giving up is a value the server returns")
		t.Log("rather than an error that ends anything -- no session setting is changed and none is")
		t.Log("reset, which is where this differs from PostgreSQL's lock_timeout dance.")
	})
}

func TestTwoConfiguredWaitsProduceTwoBehaviors(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		patient := lockSession(t, s, uniqueName(t), 10*time.Second)
		impatient := lockSession(t, s, siblingName(t), briefWait)
		patient.lock, impatient.lock = holder.lock, holder.lock

		release, err := holder.Lock(t.Context())
		if err != nil {
			t.Fatalf("the holder could not take the lock: %v", err)
		}

		acquired := make(chan error, 1)
		go func() {
			_, err := patient.Lock(context.WithoutCancel(t.Context()))
			acquired <- err
		}()

		waitForQueue(t, holder)

		if _, err := impatient.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
			t.Fatalf("a session configured to wait %s did not give up: %v", briefWait, err)
		}

		if queued := countWaiting(t, holder); queued == 0 {
			t.Error("nobody is queued, so the patient waiter gave up along with the impatient one")
			t.Log("Two configured values have to produce two behaviors, observed on the server")
			t.Log("rather than timed here -- forbidigo leaves a test no clock, and a wall-clock")
			t.Log("assertion would pass for a driver that ignored lock.timeout and used one bound")
			t.Log("for everything. PostgreSQL's suite makes the same argument with pg_locks.")
		}

		if err := release(t.Context()); err != nil {
			t.Fatalf("releasing: %v", err)
		}

		if err := <-acquired; err != nil {
			t.Errorf("the patient waiter never acquired: %v", err)
		}
	})
}

// waitForQueue blocks until a session is queued on the lock, so the race is the server's and not a sleep.
func waitForQueue(t *testing.T, c *mysqlConn) {
	t.Helper()

	for range queuePolls {
		if countWaiting(t, c) > 0 {
			return
		}
	}

	t.Fatal("no session queued on the lock, so the patient waiter never started waiting")
}

// countWaiting counts the sessions blocked in GET_LOCK, which is how a queue is visible here.
func countWaiting(t *testing.T, c *mysqlConn) int {
	t.Helper()

	rows := read(t, c, waitingQuery, "%GET_LOCK%")

	queued, err := strconv.Atoi(rows[0])
	if err != nil {
		t.Fatalf("counting queued sessions: %v", err)
	}

	return queued
}

func TestALockSurvivesEveryWayATransactionCanEnd(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_t"
		dropLater(t, c, table)
		drop(t.Context(), c, table)

		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		defer func() { _ = unlock(context.WithoutCancel(t.Context())) }()

		id := connectionID(t, c)

		if err := c.Transact(t.Context(), func(context.Context) error { return nil }); err != nil {
			t.Fatalf("the committed transaction failed: %v", err)
		}

		if got := heldBy(t, c, c.lock); got != id {
			t.Errorf("after a commit the lock is held by %q, want connection %s", got, id)
		}

		discarded := errors.New("discard this transaction")
		if err := c.Transact(t.Context(), func(context.Context) error {
			return discarded
		}); !errors.Is(err, discarded) {
			t.Fatalf("the rolled-back transaction returned %v", err)
		}

		if got := heldBy(t, c, c.lock); got != id {
			t.Errorf("after a rollback the lock is held by %q, want connection %s", got, id)
		}

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}, PrimaryKey: []string{"id"}})

		if got := heldBy(t, c, c.lock); got != id {
			t.Errorf("after a DDL statement the lock is held by %q, want connection %s", got, id)
		}

		t.Log("Measured on both servers, and the third case is the one that matters here: MySQL has")
		t.Log("no transactional DDL, so every DDL statement commits the open transaction implicitly")
		t.Log("(ADR 0057). A user-level lock is not transactional, so none of the three releases it --")
		t.Log("and if any did, the lock would vanish partway through the first migration that")
		t.Log("created a table, with the rest of the run unprotected and nothing saying so.")
	})
}

func TestAKilledHolderLeavesALockTheNextRunTakesImmediately(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		waiter := lockSession(t, s, uniqueName(t), briefWait)
		waiter.lock = holder.lock

		if _, err := holder.Lock(t.Context()); err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		_, err := waiter.Lock(t.Context())

		var busy *driver.LockBusyError
		if !errors.As(err, &busy) || busy.Holder == nil {
			t.Fatalf("the wait returned %v with no holder", err)
		}

		kill(t, waiter, busy.Holder.Process)

		taken, err := waiter.Lock(t.Context())
		if err != nil {
			t.Fatalf("the lock outlived the session that held it: %v", err)
		}

		if err := taken(t.Context()); err != nil {
			t.Errorf("the release failed: %v", err)
		}

		t.Log("This is why this strategy owes no staleness rule and SQLite's file lock does: the lock")
		t.Log("is a row in a server that dies with the session holding it, so a crashed migr leaves")
		t.Log("nothing behind to reclaim. The id killed here is the one the report named, so the")
		t.Log("instruction an operator is given is the instruction this test followed.")
	})
}

// kill terminates whichever connection a holder report named, which is the advice it gives.
func kill(t *testing.T, c *mysqlConn, process string) {
	t.Helper()

	id := strings.TrimPrefix(process, "connection ")
	if _, err := strconv.Atoi(id); err != nil {
		t.Fatalf("the report names %q, which is not a connection a KILL can name", process)
	}

	exec(t, c, "KILL "+id)
}

func TestTheAcquireAndTheReleaseHappenOnOneConnection(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		before := connectionID(t, c)

		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		if during := connectionID(t, c); during != before {
			t.Fatalf("the session moved from connection %s to %s", before, during)
		}

		if got := heldBy(t, c, c.lock); got != before {
			t.Errorf("the lock is held by %q and this session is connection %s", got, before)
		}

		if err := unlock(t.Context()); err != nil {
			t.Fatalf("the release failed: %v", err)
		}

		if after := connectionID(t, c); after != before {
			t.Errorf("the session moved to connection %s by the time it released", after)
		}

		if got := heldBy(t, c, c.lock); got != "free" {
			t.Errorf("after the release the lock is held by %q", got)
		}

		t.Log("The done-when's pooling clause, asserted by the server's own id rather than by")
		t.Log("inspecting the pool: GET_LOCK belongs to a connection, so an acquire on one pooled")
		t.Log("connection and a release on another would report success and release nothing. Open")
		t.Log("sets MaxOpenConns to one and holds one *sql.Conn for the session's life (ADR 0057),")
		t.Log("and this is what proves the lock rode on it.")
	})
}

func TestReleasingTwiceIsNotAnError(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		if err := unlock(t.Context()); err != nil {
			t.Fatalf("the first release failed: %v", err)
		}

		if err := unlock(t.Context()); err != nil {
			t.Errorf("the second release returned %v, and Unlock is safe to call more than once", err)
		}
	})
}

func TestASecondAcquireOnOneLiveSessionIsRefusedRatherThanCounted(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		if _, err := c.Lock(t.Context()); !errors.Is(err, ErrLockHeld) {
			t.Errorf("a second acquire returned %v, want %v", err, ErrLockHeld)
		}

		if err := unlock(t.Context()); err != nil {
			t.Fatalf("the release failed: %v", err)
		}

		if got := heldBy(t, c, c.lock); got != "free" {
			t.Errorf("one release left the lock held by %q", got)
		}

		t.Log("The last check is the point: MySQL counts a second GET_LOCK on a held name and needs a")
		t.Log("matching second RELEASE_LOCK -- measured, IS_USED_LOCK still answered the holder after")
		t.Log("one release. Had the second acquire been granted, this one release would have left the")
		t.Log("lock held for the session's life with the engine believing it had given it back.")
	})
}

func TestAReleaseTheServerRefusesIsAnError(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		exec(t, c, "DO RELEASE_LOCK("+quoteLiteral(c.lock.String())+")")

		if err := unlock(t.Context()); !errors.Is(err, ErrLockRelease) {
			t.Errorf("releasing a lock the session no longer held returned %v", err)
		}

		t.Log("Measured: RELEASE_LOCK answers NULL for a name nobody holds and 0 for one another")
		t.Log("session holds, on both servers. Neither is a release, so neither is reported as one --")
		t.Log("only a connection that cannot carry the statement at all is, the lock having died with")
		t.Log("the session by then.")
	})
}

// absentConnection is an id no server will have handed out, so its row cannot be found.
const absentConnection = 999999999

// plausibleState is the longest a freshly started holder's current state can honestly have lasted.
const plausibleState = time.Minute

func TestAHolderWhoseRowTheServerDoesNotHaveIsStillNamed(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := c.holder(t.Context(), absentConnection)
		if held == nil {
			t.Fatal("a holder with no row in the processlist was reported as no holder at all")
		}

		if !strings.Contains(held.Process, strconv.Itoa(absentConnection)) {
			t.Errorf("the holder is named %q", held.Process)
		}

		if held.Application != "" {
			t.Errorf("a connection the server has no row for is reported as %q", held.Application)
		}

		if held.Elapsed != 0 || held.Measured != "" {
			t.Errorf("a connection the server has no row for has been in a state for %s (%q)",
				held.Elapsed, held.Measured)
		}

		t.Log("This is the production rendering reached through the production path: without PROCESS")
		t.Log("the holder's row is filtered out, which is indistinguishable here from a connection")
		t.Log("that does not exist. The id still comes back, because IS_USED_LOCK needs no privilege")
		t.Log("and it is the one thing an operator can KILL -- returning no holder would throw it")
		t.Log("away, and reporting an account for it would be some other session's.")
	})
}

func TestTheHolderReportIsAboutTheConnectionItWasAsked(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		id, err := strconv.ParseInt(connectionID(t, c), 10, 64)
		if err != nil {
			t.Fatalf("reading this session's connection id: %v", err)
		}

		held := c.holder(t.Context(), id)
		if held.Application == "" {
			t.Fatal("this session has PROCESS and could not describe itself")
		}

		if !strings.HasPrefix(held.Application, "migr@") {
			t.Errorf("this session reports itself as %q", held.Application)
		}

		if held.Elapsed > plausibleState {
			t.Errorf("this session has been in its current state for %s, which is a column that is "+
				"not TIME", held.Elapsed)
		}

		if held.Measured != measuredState {
			t.Errorf("a duration read from the server is labeled %q", held.Measured)
		}

		t.Log("Asking about this very session is what makes the row's identity checkable: a lookup")
		t.Log("that matched any other row would describe somebody else, and reading a column that is")
		t.Log("not TIME would report a connection id as a number of seconds -- which on these servers")
		t.Log("is thousands of them, where a session started moments ago is in its state for none.")
	})
}

func TestAWaitThatFindsTheLockFreeNamesNobodyAndSaysSo(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		report := c.busy(t.Context())

		if report.Holder != nil {
			t.Errorf("a free lock is reported as held by %s", report.Holder)
		}

		if !strings.Contains(report.Unidentified, "released") {
			t.Errorf("the report says %q, and the lock was free", report.Unidentified)
		}

		t.Log("The race this covers is real and narrow: GET_LOCK answers 0 and the holder releases")
		t.Log("before IS_USED_LOCK is asked. Reporting a holder read from a null id would be a")
		t.Log("holder nobody named, so the report says why there is none instead.")
	})
}

func TestAHolderReadOnACanceledContextStillAnswers(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		unlock, err := holder.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		defer func() { _ = unlock(context.WithoutCancel(t.Context())) }()

		waiter := lockSession(t, s, uniqueName(t), briefWait)
		waiter.lock = holder.lock

		canceled, cancel := context.WithCancel(t.Context())
		cancel()

		report := waiter.busy(canceled)
		if report.Holder == nil {
			t.Fatalf("a holder read on a canceled context identified nobody: %s",
				report.Unidentified)
		}

		if !strings.Contains(report.Holder.Process, connectionID(t, holder)) {
			t.Errorf("the report names %q", report.Holder.Process)
		}

		if report.Holder.Application == "" || report.Holder.Measured != measuredState {
			t.Errorf("the holder is described as %q with its duration labeled %q, and this session "+
				"has PROCESS", report.Holder.Application, report.Holder.Measured)
		}

		t.Log("Both reads are asserted, because they are two statements and the second is the one a")
		t.Log("canceled context reaches with the first already answered: an id that comes back with")
		t.Log("nothing beside it is what a privileged session looks like when the describing read was")
		t.Log("the one that got canceled.")
		t.Log("")
		t.Log("A wait is abandoned exactly when its context is done, so the read that explains the")
		t.Log("failure runs on a context the failure cannot have canceled -- PostgreSQL's rule, whose")
		t.Log("own suite never exercised it. Reading on the caller's context would have the library")
		t.Log("close the pinned connection and the report would say the lookup failed, on the one")
		t.Log("path that exists to say who is holding things up.")
	})
}

func TestAnInterruptedWaitIsNotAnAcquireAndNotContentionEither(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, holder *mysqlConn) {
		unlock, err := holder.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		defer func() { _ = unlock(context.WithoutCancel(t.Context())) }()

		waiter := lockSession(t, s, uniqueName(t), 10*time.Second)
		waiter.lock = holder.lock

		failed := make(chan error, 1)
		go func() {
			_, err := waiter.Lock(context.WithoutCancel(t.Context()))
			failed <- err
		}()

		waitForQueue(t, holder)
		exec(t, holder, "KILL QUERY "+read(t, holder, queuedQuery, "%GET_LOCK%")[0])

		err = <-failed
		if err == nil {
			t.Fatal("an interrupted wait reported that it had taken the lock")
		}

		if errors.Is(err, driver.ErrLockTimeout) {
			t.Errorf("an interrupted wait is reported as contention: %v", err)
		}

		if !errors.Is(err, ErrLock) {
			t.Errorf("the failure does not carry %v: %v", ErrLock, err)
		}

		t.Log("The third answer, reached: MariaDB answers NULL for a waiter whose query is killed and")
		t.Log("MySQL answers error 1317, measured -- so neither is 0 and neither is contention.")
		t.Log("Reporting either as ErrLockTimeout would send an operator looking for a holder in a")
		t.Log("report that named one only because something unrelated had interrupted the wait.")
	})
}

func TestAReleaseAnotherSessionHasTakenOverIsAnError(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		unlock, err := c.Lock(t.Context())
		if err != nil {
			t.Fatalf("the acquire failed: %v", err)
		}

		exec(t, c, "DO RELEASE_LOCK("+quoteLiteral(c.lock.String())+")")

		thief := lockSession(t, s, uniqueName(t), briefWait)
		thief.lock = c.lock

		stolen, err := thief.Lock(t.Context())
		if err != nil {
			t.Fatalf("the second session could not take the released lock: %v", err)
		}

		defer func() { _ = stolen(context.WithoutCancel(t.Context())) }()

		if err := unlock(t.Context()); !errors.Is(err, ErrLockNotHeld) {
			t.Errorf("releasing a lock another session holds returned %v, want %v", err,
				ErrLockNotHeld)
		}

		t.Log("Measured: RELEASE_LOCK answers 0 for a lock another session holds and NULL for one")
		t.Log("nobody holds, on both servers. Both are the server saying this session did not hold")
		t.Log("it, and reporting either as a release would have migr believe it had given back a lock")
		t.Log("somebody else is relying on.")
	})
}
