//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// waitingQuery counts sessions queued behind this key, which is the block seen from the server.
const waitingQuery = `SELECT count(*) FROM pg_locks
	WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND NOT granted`

// pollInterval is how often a test asks the server whether a second session has queued yet.
const pollInterval = 5 * time.Millisecond

// pollLimit bounds that wait, so a lock that never blocks fails rather than hanging the suite.
const pollLimit = 10 * time.Second

// lockedConn opens a session for a namespace of the test's choosing.
func lockedConn(t *testing.T, dsn, table string) driver.Conn {
	t.Helper()

	return lockedConnWaiting(t, dsn, table, driver.DefaultLockWait)
}

// lockedConnWaiting opens a session whose acquire gives up after the given wait.
func lockedConnWaiting(t *testing.T, dsn, table string, wait time.Duration) driver.Conn {
	t.Helper()

	return lockedConnWith(t, dsn, driver.Options{HistoryTable: table, LockWait: wait})
}

// lockedConnWith opens a session built from options a test chose, so a bound is not the driver's own.
func lockedConnWith(t *testing.T, dsn string, opts driver.Options) driver.Conn {
	t.Helper()

	built, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	d, ok := built.(*pgDriver)
	if !ok {
		t.Fatalf("New returned %T, not this package's driver", built)
	}

	conn, err := d.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v\n\nRun `make test-integration`, which brings the servers up first.", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return conn
}

// waitForQueue blocks until a second session is queued behind the key, or fails saying none is.
func waitForQueue(t *testing.T, observer *pgConn, key lockKey) {
	t.Helper()

	deadline := time.NewTimer(pollLimit)
	defer deadline.Stop()

	for {
		if countWaiting(t, observer, key) > 0 {
			return
		}

		select {
		case <-deadline.C:
			t.Fatalf("no session ever queued behind %s, so nothing was blocked", key)
		case <-time.After(pollInterval):
		}
	}
}

// countWaiting reports how many sessions the server has queued behind a key right now.
func countWaiting(t *testing.T, observer *pgConn, key lockKey) int {
	t.Helper()

	var waiting int
	if err := observer.conn.QueryRow(t.Context(), waitingQuery, key.class, key.obj).
		Scan(&waiting); err != nil {
		t.Fatalf("reading pg_locks: %v", err)
	}

	return waiting
}

func TestASecondConnectionIsBlockedUntilTheFirstReleases(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)

			holder := lockedConn(t, dsn, "migr_lock_probe")
			waiter := lockedConn(t, dsn, "migr_lock_probe")
			observer := openSession(t, dsn)

			key := namespaceKey("", "migr_lock_probe")

			release, err := holder.Lock(t.Context())
			if err != nil {
				t.Fatalf("the first connection could not take the lock: %v", err)
			}

			acquired := make(chan error, 1)

			go func() {
				_, err := waiter.Lock(context.WithoutCancel(t.Context()))
				acquired <- err
			}()

			waitForQueue(t, observer, key)

			select {
			case err := <-acquired:
				t.Fatalf("the second connection took the lock while the first held it: %v", err)
			default:
			}

			if err := release(t.Context()); err != nil {
				t.Fatalf("releasing: %v", err)
			}

			if err := <-acquired; err != nil {
				t.Errorf("the second connection never got the lock after it was released: %v", err)
				t.Log("This half is what makes the blocking half mean anything: a Lock that always")
				t.Log("failed would satisfy 'the second connection is blocked' on its own.")
			}
		})
	}

	t.Log("The block is observed in pg_locks rather than inferred from a sleep, so the test is")
	t.Log("not timing-dependent: the server itself reports a session queued and not granted.")
}

func TestAnUnrelatedNamespaceIsNotBlocked(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	mine := lockedConn(t, dsn, "migr_lock_probe_a")
	theirs := lockedConn(t, dsn, "migr_lock_probe_b")

	release, err := mine.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the first lock: %v", err)
	}

	defer func() {
		if err := release(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("releasing: %v", err)
		}
	}()

	other, err := theirs.Lock(t.Context())
	if err != nil {
		t.Fatalf("a different namespace was blocked by an unrelated migration: %v", err)
	}

	if err := other(t.Context()); err != nil {
		t.Errorf("releasing the second lock: %v", err)
	}

	t.Log("Advisory locks are scoped to the database, not the schema, so two migr installations")
	t.Log("in one database would serialize against each other if the key did not separate them.")
	t.Log("This is what the key derivation is for, and nothing else tests it.")
}

func TestTheLockOutlivesNothingButItsSession(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	d, err := New(driver.Options{HistoryTable: "migr_lock_probe_crash"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	abandoned, err := d.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := abandoned.Lock(t.Context()); err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	if err := abandoned.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	successor := lockedConn(t, dsn, "migr_lock_probe_crash")

	release, err := successor.Lock(t.Context())
	if err != nil {
		t.Fatalf("the lock outlived the session that held it: %v", err)
	}

	if err := release(t.Context()); err != nil {
		t.Errorf("releasing: %v", err)
	}

	t.Log("This is invariant #11's stale-lock recovery story, and PostgreSQL is the whole of it:")
	t.Log("a session-level advisory lock is released by the server when the backend goes away,")
	t.Log("so a crashed migr cannot wedge a database and there is no recovery code to write.")
	t.Log("The table and file strategies in migr.yaml will need real machinery here; this one")
	t.Log("does not, and that absence is a property rather than an omission.")
}

func TestReleasingTwiceIsNotAnError(t *testing.T) {
	conn := lockedConn(t, dsnFor(servers[0].env, servers[0].dsn), "migr_lock_probe_twice")

	release, err := conn.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	if err := release(t.Context()); err != nil {
		t.Fatalf("the first release failed: %v", err)
	}

	if err := release(t.Context()); err != nil {
		t.Errorf("the second release failed: %v", err)
		t.Log("The contract says a release is safe to call more than once, so an early explicit")
		t.Log("release and a deferred one do not double-release. Without it the deferred call")
		t.Log("would ask the server to unlock something nobody holds, which reports false.")
	}

	if _, err := conn.Lock(t.Context()); err != nil {
		t.Errorf("the session could not retake a lock it had released: %v", err)
	}
}

func TestASecondAcquireOnOneSessionIsRefused(t *testing.T) {
	conn := lockedConn(t, dsnFor(servers[0].env, servers[0].dsn), "migr_lock_probe_reentrant")

	release, err := conn.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	defer func() {
		if err := release(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("releasing: %v", err)
		}
	}()

	_, err = conn.Lock(t.Context())
	if err == nil {
		t.Fatal("a session took the migration lock twice")
	}

	if !errors.Is(err, ErrLockHeld) || !errors.Is(err, ErrLock) {
		t.Errorf("the refusal was %v, want ErrLockHeld", err)
	}

	t.Log("pg_advisory_lock is re-entrant and counted, so the second acquire would have")
	t.Log("succeeded instantly and raised the hold count to two -- after which one release")
	t.Log("reports success while the lock is still held, and the next run blocks forever")
	t.Log("against a lock nobody knows about.")
}

func TestAnAcquireLeavesTheSessionSettingsAsItFoundThem(t *testing.T) {
	conn := lockedConn(t, dsnFor(servers[0].env, servers[0].dsn), "migr_lock_probe_settings")

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	before := lockTimeoutOf(t, session)

	release, err := conn.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	defer func() {
		if err := release(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("releasing: %v", err)
		}
	}()

	if after := lockTimeoutOf(t, session); after != before {
		t.Errorf("lock_timeout was %q before the acquire and %q after", before, after)
		t.Log("The bound covers the acquire and nothing else. Left set, it would silently govern")
		t.Log("every statement M3-T5 runs afterwards -- a lock timeout nobody configured, which")
		t.Log("is a different bug from the one invariant #12 asks for.")
	}
}

// configuredStatementWait is deliberately not a round number of seconds and not either default.
//
// PostgreSQL renders lock_timeout back in the largest unit that divides it evenly, so 3000ms reads
// as "3s" -- measured. 1200ms reads as itself, which is what lets this test assert the configured
// value rather than something the driver would have produced anyway.
const configuredStatementWait = 1200 * time.Millisecond

// configuredStatementSetting is how PostgreSQL renders that wait back through SHOW.
const configuredStatementSetting = "1200ms"

func TestTheAcquireRestoresTheBoundEveryStatementRunsUnder(t *testing.T) {
	conn := lockedConnWith(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{
		HistoryTable:      "migr_lock_probe_restored",
		LockWait:          5 * time.Second,
		StatementLockWait: configuredStatementWait,
	})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	if before := lockTimeoutOf(t, session); before != configuredStatementSetting {
		t.Log("The two bounds are different settings. safety.lock_timeout is pinned in the startup")
		t.Log("packet and lock.timeout bounds the acquire; a session showing the acquire's wait")
		t.Log("here means the two were swapped on the way down.")
		t.Fatalf("a fresh session bounds a statement by %q, want %q", before,
			configuredStatementSetting)
	}

	release, err := conn.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	defer func() {
		if err := release(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("releasing: %v", err)
		}
	}()

	if after := lockTimeoutOf(t, session); after != configuredStatementSetting {
		t.Errorf("after the acquire a statement is bounded by %q, want %q",
			after, configuredStatementSetting)
		t.Log("RESET restores reset_val, which only a source at or below \"client\" sets. Establish")
		t.Log("the per-statement bound with set_config and this line silently returns lock_timeout")
		t.Log("to 0 -- so every DDL statement after the first migration lock waits forever, which is")
		t.Log("invariant #12 broken on the happy path. The startup packet is what makes RESET")
		t.Log("restore the bound rather than clear it.")
	}
}

// lockTimeoutOf reads what this session would currently bound a lock wait by.
func lockTimeoutOf(t *testing.T, session *pgConn) string {
	t.Helper()

	var setting string
	if err := session.conn.QueryRow(t.Context(), "SHOW lock_timeout").Scan(&setting); err != nil {
		t.Fatalf("reading lock_timeout: %v", err)
	}

	return setting
}

func TestAReleaseTheServerRefusesIsAnError(t *testing.T) {
	conn := lockedConn(t, dsnFor(servers[0].env, servers[0].dsn), "migr_lock_probe_stolen")

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	release, err := conn.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	if _, err := session.conn.Exec(t.Context(), "SELECT pg_advisory_unlock_all()"); err != nil {
		t.Fatalf("dropping the session's locks out from under the release: %v", err)
	}

	err = release(t.Context())
	if err == nil {
		t.Fatal("releasing a lock the session no longer held reported success")
	}

	if !errors.Is(err, ErrLockNotHeld) || !errors.Is(err, ErrLock) {
		t.Errorf("the release failed with %v, want ErrLockNotHeld", err)
	}

	t.Log("pg_advisory_unlock answers false rather than erroring when the session holds nothing,")
	t.Log("so swallowing that would report a clean release for a lock that was never given back")
	t.Log("-- or, as here, one something else had already dropped. The engine would then run a")
	t.Log("migration believing it was serialized when it was not.")
}

func TestAWaitThatGivesUpLeavesTheSessionUsable(t *testing.T) {
	dsn := dsnFor(servers[0].env, servers[0].dsn)

	holder := lockedConn(t, dsn, "migr_lock_probe_timeout")
	waiter := lockedConnWaiting(t, dsn, "migr_lock_probe_timeout", 150*time.Millisecond)

	release, err := holder.Lock(t.Context())
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}

	session, ok := waiter.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", waiter)
	}

	_, err = waiter.Lock(t.Context())
	if err == nil {
		t.Fatal("the wait never gave up")
	}

	if !errors.Is(err, driver.ErrLockTimeout) {
		t.Errorf("the wait failed with %v, want driver.ErrLockTimeout", err)
	}

	checkStillUsable(t, waiter, session)

	if err := release(t.Context()); err != nil {
		t.Fatalf("releasing: %v", err)
	}

	retry, err := waiter.Lock(t.Context())
	if err != nil {
		t.Fatalf("the session that timed out could not acquire once the lock freed: %v", err)
	}

	if err := retry(t.Context()); err != nil {
		t.Errorf("releasing: %v", err)
	}

	t.Log("pgx closes the connection when a context is canceled mid-query -- its own docs say")
	t.Log("so -- and a Conn is pinned for the lock's lifetime, so bounding the wait with a")
	t.Log("context would kill the one session that could have held it. Bounding it server-side")
	t.Log("with lock_timeout fails the statement instead, and the session survives to retry,")
	t.Log("which is what M4-T4 builds its backoff on.")
}

// checkStillUsable proves a session that gave up waiting is alive, which a canceled one is not.
func checkStillUsable(t *testing.T, conn driver.Conn, session *pgConn) {
	t.Helper()

	var alive int
	if err := session.conn.QueryRow(t.Context(), "SELECT 1").Scan(&alive); err != nil {
		t.Fatalf("the session did not survive giving up on the lock: %v", err)
	}

	if conn.ServerVersion() == "" {
		t.Error("the session forgot what server it is talking to")
	}
}
