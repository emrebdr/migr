//go:build integration

package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// briefWait is short enough that a blocked acquire gives up promptly and long enough not to be flaky.
const briefWait = 150 * time.Millisecond

// backendPIDQuery names the session asking, so a test can check a report against the truth.
const backendPIDQuery = `SELECT pg_backend_pid()`

// terminateQuery ends another backend, which is the command a holder report tells an operator to run.
const terminateQuery = `SELECT pg_terminate_backend($1)`

// keepaliveQuery reads back what the startup packet asked the server to do about a vanished client.
const keepaliveQuery = `SELECT name, setting FROM pg_settings WHERE name LIKE 'tcp_keepalives%'`

// foreignLockQuery takes the single-key advisory lock whose halves decompose to migr's own.
//
// pg_advisory_lock(bigint) lands in pg_locks with classid and objid taken from the high and low
// halves of its argument, so this row is identical to migr's in every column but objsubid, which
// is 1 rather than 2. Measured on PostgreSQL 16 and 17. It is the one way the two advisory key
// spaces -- which PostgreSQL otherwise keeps rigorously apart -- can be confused by a reader.
const foreignLockQuery = `SELECT pg_advisory_lock(($1::bigint << 32) | $2::bigint)`

// blockedAcquire takes the lock on a holder, then reports what a waiter's acquire failed with.
func blockedAcquire(t *testing.T, dsn, table string) (*driver.LockBusyError, driver.Conn) {
	t.Helper()

	holder := lockedConn(t, dsn, table)
	waiter := lockedConnWaiting(t, dsn, table, briefWait)

	release, err := holder.Lock(t.Context())
	if err != nil {
		t.Fatalf("the holder could not take the lock: %v", err)
	}

	t.Cleanup(func() {
		_ = release(context.WithoutCancel(t.Context()))
	})

	_, err = waiter.Lock(t.Context())
	if err == nil {
		t.Fatal("the wait never gave up, so there is no report to inspect")
	}

	var busy *driver.LockBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("the wait failed with %v, want a *driver.LockBusyError", err)
	}

	return busy, holder
}

// itoa renders a backend pid the way a holder report's Process field spells it.
func itoa(pid int32) string {
	return strconv.FormatInt(int64(pid), 10)
}

// parsePID reads a pid back out of a holder report, which is what makes the report actionable.
func parsePID(process string) (int32, error) {
	digits, found := strings.CutPrefix(process, "pid ")
	if !found {
		return 0, errors.New("does not begin with \"pid \"")
	}

	pid, err := strconv.ParseInt(digits, 10, 32)
	if err != nil {
		return 0, err
	}

	return int32(pid), nil
}

// pidOf asks a session for its own backend pid, which is what a holder report should name.
func pidOf(t *testing.T, conn driver.Conn) int32 {
	t.Helper()

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	var pid int32
	if err := session.conn.QueryRow(t.Context(), backendPIDQuery).Scan(&pid); err != nil {
		t.Fatalf("reading pg_backend_pid: %v", err)
	}

	return pid
}

func TestTheFailureNamesThePidAndApplicationOfTheSessionHoldingIt(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)

			busy, holder := blockedAcquire(t, dsn, "migr_lock_probe_holder")

			if busy.Holder == nil {
				t.Fatalf("the report names no holder: %v", busy)
			}

			want := "pid " + itoa(pidOf(t, holder))
			if busy.Holder.Process != want {
				t.Errorf("the report names %q, want %q", busy.Holder.Process, want)
				t.Log("granted is what separates the session holding the lock from the ones queued")
				t.Log("behind it. Without it the report names whichever pid sorts first, which on a")
				t.Log("contended lock is as likely to be a fellow waiter as the holder.")
			}

			if busy.Holder.Application != applicationName() {
				t.Errorf("the holder calls itself %q, want %q", busy.Holder.Application,
					applicationName())
			}

			if busy.Holder.Connected <= 0 {
				t.Errorf("the holder's session age is %s, want a positive duration",
					busy.Holder.Connected)
			}
		})
	}
}

func TestTheReportNamesTheHolderRatherThanAFellowWaiter(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	table := "migr_lock_probe_queued"

	queued := lockedConnWaiting(t, dsn, table, 10*time.Second)
	waiter := lockedConnWaiting(t, dsn, table, briefWait)
	observer := openSession(t, dsn)
	holder := lockedConn(t, dsn, table)

	holderPID := pidOf(t, holder)
	if queuedPID := pidOf(t, queued); queuedPID > holderPID {
		t.Skipf("the queued session took pid %d and the holder %d, so ordering by pid would "+
			"name the holder anyway; this server recycled its pids", queuedPID, holderPID)
	}

	release, err := holder.Lock(t.Context())
	if err != nil {
		t.Fatalf("the holder could not take the lock: %v", err)
	}

	acquired := make(chan error, 1)
	go func() { _, err := queued.Lock(context.WithoutCancel(t.Context())); acquired <- err }()

	waitForQueue(t, observer, namespaceKey("", table))

	_, err = waiter.Lock(t.Context())

	var busy *driver.LockBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("the wait failed with %v, want a *driver.LockBusyError", err)
	}

	if busy.Holder == nil || busy.Holder.Process != "pid "+itoa(holderPID) {
		t.Errorf("with a lower-numbered session queued, the report names %v", busy.Holder)
		t.Log("The holder is deliberately opened last, so it has the highest pid of the three.")
		t.Log("granted is then the only thing separating it from the sessions queued behind it:")
		t.Log("ordering by pid alone would name a waiter, and migr would tell an operator to")
		t.Log("terminate a session that is itself waiting.")
	}

	if err := release(t.Context()); err != nil {
		t.Fatalf("releasing: %v", err)
	}

	if err := <-acquired; err != nil {
		t.Errorf("the queued waiter never acquired: %v", err)
	}
}

func TestAWaitHonorsTheTimeoutItWasConfiguredWith(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	table := "migr_lock_probe_configured"

	holder := lockedConn(t, dsn, table)
	patient := lockedConnWaiting(t, dsn, table, 10*time.Second)
	impatient := lockedConnWaiting(t, dsn, table, briefWait)
	observer := openSession(t, dsn)

	release, err := holder.Lock(t.Context())
	if err != nil {
		t.Fatalf("the holder could not take the lock: %v", err)
	}

	patientAcquired := make(chan error, 1)
	go func() { _, err := patient.Lock(context.WithoutCancel(t.Context())); patientAcquired <- err }()

	waitForQueue(t, observer, namespaceKey("", table))

	if _, err := impatient.Lock(t.Context()); !errors.Is(err, driver.ErrLockTimeout) {
		t.Fatalf("a session configured to wait %s did not give up: %v", briefWait, err)
	}

	if waiting := countWaiting(t, observer, namespaceKey("", table)); waiting == 0 {
		t.Error("no session is queued, so the patient waiter gave up along with the impatient one")
		t.Log("Two configured values have to produce two behaviors, observed on the server rather")
		t.Log("than timed here. A driver ignoring lock.timeout and using one bound for everything")
		t.Log("passes every single-waiter test there is.")
	}

	if err := release(t.Context()); err != nil {
		t.Fatalf("releasing: %v", err)
	}

	if err := <-patientAcquired; err != nil {
		t.Errorf("the patient waiter never acquired: %v", err)
	}
}

func TestTheHolderReportIgnoresAnUnrelatedAdvisoryLock(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	table := "migr_lock_probe_foreign"
	key := namespaceKey("", table)

	foreign := openSession(t, dsn)
	if _, err := foreign.conn.Exec(t.Context(), foreignLockQuery, key.class, key.obj); err != nil {
		t.Fatalf("taking the single-key advisory lock: %v", err)
	}

	waiter := lockedConnWaiting(t, dsn, table, briefWait)

	release, err := waiter.Lock(t.Context())
	if err != nil {
		t.Log("PostgreSQL keeps the one-key and two-key advisory spaces apart, so this acquire")
		t.Log("must succeed. If it does not, the driver is taking the wrong kind of lock.")
		t.Fatalf("migr's own key was blocked by a lock in the other key space: %v", err)
	}

	if err := release(t.Context()); err != nil {
		t.Errorf("releasing: %v", err)
	}

	session, ok := waiter.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", waiter)
	}

	holder, err := session.holder(t.Context())
	if err != nil {
		t.Fatalf("reading the holder: %v", err)
	}

	if holder != nil {
		t.Errorf("the report names %v for a lock only the other key space holds", holder)
		t.Log("pg_advisory_lock(bigint) decomposes to the same classid and objid, differing only")
		t.Log("in objsubid. Dropping that clause makes migr tell an operator to terminate a")
		t.Log("session that has nothing to do with migr.")
	}
}

func TestTheHolderReportIgnoresAnotherToolsAdvisoryLock(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	table := "migr_lock_probe_foreign_class"
	key := namespaceKey("", table)

	foreign := openSession(t, dsn)
	if _, err := foreign.conn.Exec(t.Context(), `SELECT pg_advisory_lock($1, $2)`,
		key.class+1, key.obj); err != nil {
		t.Fatalf("taking another tool's advisory lock: %v", err)
	}

	waiter := lockedConnWaiting(t, dsn, table, briefWait)

	session, ok := waiter.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", waiter)
	}

	holder, err := session.holder(t.Context())
	if err != nil {
		t.Fatalf("reading the holder: %v", err)
	}

	if holder != nil {
		t.Errorf("a lock under class id %d is reported as migr's, which locks under %d",
			key.class+1, key.class)
		t.Log("The class id is what makes migr's locks migr's. Without it any application using")
		t.Log("two-key advisory locks on a colliding object id is reported as holding the")
		t.Log("migration lock, and an operator terminates a session that was never involved.")
	}
}

func TestAHolderReadOnACanceledContextStillAnswers(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	table := "migr_lock_probe_canceled"

	holder := lockedConn(t, dsn, table)
	waiter := lockedConnWaiting(t, dsn, table, briefWait)

	release, err := holder.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	defer func() {
		if err := release(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("releasing: %v", err)
		}
	}()

	session, ok := waiter.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", waiter)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	found, err := session.holder(canceled)
	if err != nil {
		t.Fatalf("reading the holder on a canceled context: %v", err)
	}

	if found == nil {
		t.Fatal("the holder read returned nobody for a lock that is held")
	}

	if err := session.conn.Ping(t.Context()); err != nil {
		t.Errorf("the session did not survive the holder read: %v", err)
		t.Log("A wait is abandoned exactly when its context is done, and pgx closes the")
		t.Log("connection on a query canceled mid-flight. Reading the holder on the caller's")
		t.Log("context would destroy the pinned session on the one path that exists to explain")
		t.Log("a failure -- which is the same trap M3-T4 avoided by bounding the wait server-side.")
	}
}

func TestAHolderReadThatFailsIsReportedAsItselfRatherThanAsTheFailure(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	waiter := lockedConnWaiting(t, dsn, "migr_lock_probe_unreadable", briefWait)

	session, ok := waiter.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", waiter)
	}

	if err := session.conn.Close(t.Context()); err != nil {
		t.Fatalf("closing the session to make the read fail: %v", err)
	}

	cause := errors.New("ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)")

	busy := session.busy(t.Context(), cause)

	if !errors.Is(busy, cause) {
		t.Errorf("the report is %v and no longer carries what the acquire actually failed with",
			busy)
		t.Log("Reporting \"could not read pg_locks\" instead of \"gave up waiting for the lock\"")
		t.Log("replaces the operator's problem with a diagnostic's.")
	}

	if busy.Holder != nil {
		t.Errorf("a failed read produced a holder anyway: %v", busy.Holder)
	}

	if !strings.Contains(busy.Unidentified, "pg_locks") {
		t.Errorf("the report says %q, which does not say the read is what failed",
			busy.Unidentified)
	}
}

func TestTheHolderReportIgnoresAnotherInstallationsLock(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	held := lockedConn(t, dsn, "migr_lock_probe_installation_a")
	other := lockedConn(t, dsn, "migr_lock_probe_installation_b")

	release, err := held.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	defer func() {
		if err := release(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("releasing: %v", err)
		}
	}()

	session, ok := other.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", other)
	}

	holder, err := session.holder(t.Context())
	if err != nil {
		t.Fatalf("reading the holder: %v", err)
	}

	if holder != nil {
		t.Errorf("an installation with nothing held reports %v as its holder", holder)
	}
}

func TestTheReportedSessionAgeIsTheSessionsAndNotItsTransactions(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	busy, _ := blockedAcquire(t, dsn, "migr_lock_probe_age")

	if busy.Holder == nil {
		t.Fatalf("the report names no holder: %v", busy)
	}

	if busy.Holder.Connected <= 0 {
		t.Errorf("the holder's session age is %s, want a positive duration", busy.Holder.Connected)
		t.Log("The holder is idle: it took the lock and is running nothing, so xact_start is NULL.")
		t.Log("Reading the age from it would render \"open for 0s\" for a session that has held")
		t.Log("the lock since it connected. backend_start is what bounds the hold from above.")
	}
}

func TestALockHeldByATerminatedBackendIsTakenByTheNextRun(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			table := "migr_lock_probe_terminated"

			holder := lockedConn(t, dsn, table)
			observer := openSession(t, dsn)

			if _, err := holder.Lock(t.Context()); err != nil {
				t.Fatalf("taking the lock: %v", err)
			}

			var terminated bool
			err := observer.conn.QueryRow(t.Context(), terminateQuery, pidOf(t, holder)).
				Scan(&terminated)
			if err != nil || !terminated {
				t.Fatalf("terminating the holder's backend: %v (reported %t)", err, terminated)
			}

			successor := lockedConnWaiting(t, dsn, table, 10*time.Second)

			release, err := successor.Lock(t.Context())
			if err != nil {
				t.Fatalf("a lock held by a terminated backend was not recoverable: %v", err)
			}

			if err := release(t.Context()); err != nil {
				t.Errorf("releasing: %v", err)
			}
		})
	}
}

func TestTheReportedPidIsTheOneAnOperatorCanTerminate(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)
	table := "migr_lock_probe_actionable"

	busy, _ := blockedAcquire(t, dsn, table)
	if busy.Holder == nil {
		t.Fatalf("the report names no holder, so it advises nothing: %v", busy)
	}

	pid, err := parsePID(busy.Holder.Process)
	if err != nil {
		t.Fatalf("the report names %q, which is not something to hand pg_terminate_backend: %v",
			busy.Holder.Process, err)
	}

	observer := openSession(t, dsn)

	var terminated bool
	if err := observer.conn.QueryRow(t.Context(), terminateQuery, pid).Scan(&terminated); err != nil {
		t.Fatalf("terminating the pid the report named: %v", err)
	}

	if !terminated {
		t.Fatalf("the server does not know a backend with pid %d", pid)
	}

	successor := lockedConnWaiting(t, dsn, table, 10*time.Second)

	release, err := successor.Lock(t.Context())
	if err != nil {
		t.Errorf("terminating the pid the report named did not free the lock: %v", err)
		t.Log("This is the whole value of the report. A pid an operator cannot act on is a")
		t.Log("diagnostic; one that frees the lock when terminated is a remedy.")

		return
	}

	if err := release(t.Context()); err != nil {
		t.Errorf("releasing: %v", err)
	}
}

func TestTheSessionThatGaveUpIsWhatMakesTheHolderAnswerable(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	busy, _ := blockedAcquire(t, dsn, "migr_lock_probe_answerable")

	if busy.Holder == nil {
		t.Errorf("the wait gave up and could not then say who held it: %v", busy)
		t.Log("M3-T4 bounded the wait server-side so a timeout leaves the session alive. Bounded")
		t.Log("by a context instead, pgx would have closed the connection -- and there would be")
		t.Log("nothing left to run pg_locks against. The surviving session is what answers this.")
	}
}

func TestEveryMigrSessionIdentifiesItselfInPgStatActivity(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session := openSession(t, dsnFor(server.env, server.dsn))

			var name string
			err := session.conn.QueryRow(t.Context(),
				`SELECT application_name FROM pg_stat_activity WHERE pid = pg_backend_pid()`).
				Scan(&name)
			if err != nil {
				t.Fatalf("reading application_name: %v", err)
			}

			if name != applicationName() {
				t.Errorf("a migr session calls itself %q, want %q", name, applicationName())
				t.Log("This is what a holder report names beside the pid. Unset, every report says")
				t.Log("only \"pid 4711\" and an operator has no way to tell migr from anything else.")
			}
		})
	}
}

func TestABackendMigrOpenedAsksToBeReapedWhenItsClientVanishes(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session := openSession(t, dsnFor(server.env, server.dsn))

			rows, err := session.conn.Query(t.Context(), keepaliveQuery)
			if err != nil {
				t.Fatalf("reading the keepalive settings: %v", err)
			}

			settings := map[string]string{}

			for rows.Next() {
				var name, setting string
				if err := rows.Scan(&name, &setting); err != nil {
					t.Fatalf("scanning a keepalive setting: %v", err)
				}

				settings[name] = setting
			}

			if err := rows.Err(); err != nil {
				t.Fatalf("reading the keepalive settings: %v", err)
			}

			for name, want := range keepaliveParams {
				if got := settings[name]; got != want {
					t.Errorf("%s is %q, want %q", name, got, want)
					t.Log("A client killed with SIGKILL never sends a FIN, so the backend stays")
					t.Log("blocked in ReadCommand holding the advisory lock until keepalives expire")
					t.Log("-- two hours by default on Linux. These settings make it about ninety")
					t.Log("seconds. The reaping itself needs a dropped packet and is not testable")
					t.Log("here; that the server was asked is.")
				}
			}
		})
	}
}
