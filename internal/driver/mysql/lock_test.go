package mysql

import (
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

func TestTheLockNameIsTheSameEveryTime(t *testing.T) {
	first, second := namespaceLock("app", "migr_migrations"), namespaceLock("app", "migr_migrations")

	if first != second {
		t.Errorf("one installation derived %s and %s", first, second)
	}

	t.Log("Two migr processes agree on a lock only by deriving the same name from the same two")
	t.Log("identifiers, so a derivation that varied would be two locks that exclude nobody.")
}

func TestDifferentInstallationsLockOnDifferentNames(t *testing.T) {
	seen := map[lockName]string{}

	for _, c := range []struct {
		database, table string
	}{
		{"app", "migr_migrations"},
		{"other", "migr_migrations"},
		{"app", "schema_history"},
		{"a", "bc"},
		{"ab", "c"},
	} {
		name := namespaceLock(c.database, c.table)

		if had, clash := seen[name]; clash {
			t.Errorf("%s.%s and %s derive the same lock %s", c.database, c.table, had, name)
		}

		seen[name] = c.database + "." + c.table
	}

	t.Log("A user-level lock is global to the server rather than scoped to a database -- measured, by")
	t.Log("taking one as a user with rights on one schema and reading it from another connection --")
	t.Log("so two installations on one server would exclude each other if the database were not part")
	t.Log("of the name. The last two cases are the separator's reason: \"a\"+\"bc\" and \"ab\"+\"c\"")
	t.Log("concatenate to the same string and must not derive the same lock.")
}

func TestEveryLockNameFitsWhatMySQLKeepsAndSaysWhoseItIs(t *testing.T) {
	name := namespaceLock(strings.Repeat("d", identifierLimit),
		strings.Repeat("t", identifierLimit)).String()

	if len(name) > maxLockNameLength {
		t.Errorf("the derived name is %d characters and MySQL keeps %d", len(name),
			maxLockNameLength)
	}

	if !strings.HasPrefix(name, lockPrefix) {
		t.Errorf("the name is %q and carries no migr prefix", name)
	}

	if name != strings.ToLower(name) {
		t.Errorf("the name is %q and is not lowercase", name)
	}

	t.Log("Measured: MySQL 8.0.46 answers error 4163 for a 65-character lock name and accepts 64,")
	t.Log("while MariaDB 11.4.13 accepts 65 without complaint -- so the bound is MySQL's and the")
	t.Log("widest possible namespace must still fit it, which a digest does and two 64-character")
	t.Log("identifiers do not.")
	t.Log("")
	t.Log("Lowercase matters for a second measured reason: GET_LOCK('migr:abc') is the same lock as")
	t.Log("IS_USED_LOCK('migr:ABC') on MySQL and a different one on MariaDB. Hex in one case cannot")
	t.Log("mean two things on either.")
}

func TestTheWaitIsSpelledInDecimalSecondsAndNotWholeOnes(t *testing.T) {
	for wait, want := range map[time.Duration]string{
		30 * time.Second:        "30",
		3 * time.Second:         "3",
		1500 * time.Millisecond: "1.5",
		250 * time.Millisecond:  "0.25",
		time.Millisecond:        "0.001",
	} {
		if got := lockWaitSeconds(wait); got != want {
			t.Errorf("lockWaitSeconds(%s) = %q, want %q", wait, got, want)
		}
	}

	t.Log("GET_LOCK's second argument is a decimal number of seconds, accepted as 0.5 on both")
	t.Log("servers -- where lock_wait_timeout takes whole seconds and refuses a fraction, which is")
	t.Log("why waitSeconds exists separately and why rendering this one with it would turn every")
	t.Log("sub-second bound into 0: an immediate try rather than a wait. M4-T4 found the mirror of")
	t.Log("this for PostgreSQL, where Duration.String was the unusable spelling.")
}

func TestNoWaitThisDriverRendersCanMeanWaitForever(t *testing.T) {
	for _, wait := range []time.Duration{0, -time.Second, -time.Hour, time.Nanosecond} {
		got := lockWaitSeconds(wait)

		if strings.HasPrefix(got, "-") || got == "0" {
			t.Errorf("lockWaitSeconds(%s) = %q, which is not a bound", wait, got)
		}
	}

	t.Log("Measured, and the engines disagree: GET_LOCK with a negative timeout waits indefinitely")
	t.Log("on MySQL -- it waited three seconds for a holder and then took the lock -- and answers")
	t.Log("NULL at once on MariaDB. Neither is a bound, and the MySQL half is the worse failure")
	t.Log("available here: a migr that never gives up and never says why. Options.Waits already")
	t.Log("substitutes the default for a non-positive wait, so this is the second line of defense")
	t.Log("rather than the first -- PostgreSQL's millisecondsCeil in this engine's units.")
}

func TestASecondAcquireOnOneSessionIsRefusedRatherThanCounted(t *testing.T) {
	c := &mysqlConn{lock: namespaceLock("app", "migr_migrations"), locked: true}

	_, err := c.Lock(t.Context())
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("a second acquire returned %v, want %v", err, ErrLockHeld)
	}

	if !errors.Is(err, ErrLock) {
		t.Errorf("the refusal does not carry %v", ErrLock)
	}

	if !strings.Contains(err.Error(), lockPrefix) {
		t.Errorf("the refusal does not name the lock: %v", err)
	}

	t.Log("Measured: GET_LOCK on a name this session already holds succeeds and counts, and after")
	t.Log("one RELEASE_LOCK the lock is still held -- IS_USED_LOCK still answered the holder's own")
	t.Log("connection id, on both servers. So a second acquire behind one deferred release would")
	t.Log("leave the lock held for the session's life, which is why this refuses rather than")
	t.Log("counting. PostgreSQL refuses for the same reason and SQLite for a third.")
}

func TestReleasingASessionThatNeverHeldTheLockIsNotAFailure(t *testing.T) {
	c := &mysqlConn{lock: namespaceLock("app", "migr_migrations")}

	if err := c.releaser()(t.Context()); err != nil {
		t.Errorf("releasing an unheld lock returned %v", err)
	}
}

func TestAReleaseThatCannotRunBecauseTheSessionIsGoneIsTheReleaseItAlreadyWas(t *testing.T) {
	c := &mysqlConn{lock: namespaceLock("app", "migr_migrations")}

	for _, err := range []error{sqlErrConnDone(), badConn()} {
		if got := c.classifyRelease(err); got != nil {
			t.Errorf("a release on a lost session returned %v, want nil", got)
		}
	}

	if got := c.classifyRelease(errors.New("Table 'x' doesn't exist")); got == nil {
		t.Error("a release the server answered with a failure was reported as success")
	}

	t.Log("A user-level lock belongs to a session and the server drops it when the session ends:")
	t.Log("measured by killing a holder, after which IS_USED_LOCK read free and the next acquire")
	t.Log("succeeded immediately. So nothing is left for a release to do once the connection is")
	t.Log("gone, and reporting a failure would attach an error an operator cannot act on to the one")
	t.Log("that ended the run -- engine joins the two. The release is set in motion rather than")
	t.Log("done: measured, the server dropped the lock about three seconds later, when the")
	t.Log("statement the client had abandoned finished and it next touched the closed socket.")
}

func TestAHolderWhoseSessionThisConnectionCannotSeeIsStillNamed(t *testing.T) {
	held := &driver.LockHolder{Process: "connection 42"}

	message := held.String()
	if !strings.Contains(message, "connection 42") {
		t.Fatalf("the holder renders as %q and names no connection", message)
	}

	if !strings.Contains(message, "not permitted to read") {
		t.Errorf("the holder renders as %q and does not say the age was withheld", message)
	}

	t.Log("This is the production rendering, not the degraded one: IS_USED_LOCK needs no privilege")
	t.Log("and information_schema.PROCESSLIST hides another session's row from a user without")
	t.Log("PROCESS -- measured, zero rows on both servers for a user holding only SELECT on one")
	t.Log("database, while IS_USED_LOCK answered the connection id to that same user. So a missing")
	t.Log("row degrades the report to the connection id rather than to nothing, which is where this")
	t.Log("shape differs from PostgreSQL's: there one row is the holder and no row means none.")
	t.Log("Throwing the id away would lose the one thing an operator can KILL.")
}

func TestTheAccountRendersWhoConnectedAndNothingWhenTheServerSaidNothing(t *testing.T) {
	if got := account("migr", "10.0.0.4:51234"); got != "migr@10.0.0.4:51234" {
		t.Errorf("the account renders as %q", got)
	}

	if got := account("", ""); got != "" {
		t.Errorf("an empty row renders as %q, and LockHolder renders empty as \"did not name "+
			"itself\"", got)
	}

	t.Log("MySQL has no application_name. This driver does send a program_name connection attribute")
	t.Log("(ADR 0057) and an ordinary user cannot read it back -- a 1142 denial on both servers --")
	t.Log("so the account and host are what a report can carry. They are also what an operator")
	t.Log("recognizes: which credential and which host, rather than which binary.")
}

func TestASecondsCountTheServerWouldNotGiveIsNoDurationRatherThanAWrongOne(t *testing.T) {
	for _, c := range []struct {
		name    string
		seconds *int64
		want    time.Duration
	}{
		{name: "withheld", seconds: nil, want: 0},
		{name: "a negative the server should never send", seconds: ptrTo(int64(-1)), want: 0},
		{name: "three seconds", seconds: ptrTo(int64(3)), want: 3 * time.Second},
		{name: "a state entered this instant", seconds: ptrTo(int64(0)), want: 0},
	} {
		if got := stateAge(c.seconds); got != c.want {
			t.Errorf("%s: stateAge = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestTheDurationThisDriverReportsSaysItIsNotASessionAge(t *testing.T) {
	if strings.Contains(measuredState, "session") {
		t.Errorf("this driver labels its duration %q, and it is not the session's", measuredState)
	}

	held := &driver.LockHolder{Process: "connection 42", Elapsed: 3 * time.Second,
		Measured: measuredState}

	if !strings.Contains(held.String(), measuredState) {
		t.Errorf("the holder renders as %q and does not say what it measured", held)
	}

	t.Log("information_schema.PROCESSLIST.TIME is seconds in the thread's current state, measured")
	t.Log("against a holder three seconds into a statement, which read 3. MySQL records a session's")
	t.Log("start time nowhere at all, so there is no better number -- and a holder running short")
	t.Log("statements reads near zero however old its session is. Rendering that as a session age")
	t.Log("would be the one thing ADR 0020 forbids: a report asserting more than it observed.")
}

// ptrTo returns a pointer to a value, since the server sends a nullable and a test needs both cases.
func ptrTo[T any](v T) *T {
	return &v
}

// sqlErrConnDone is the error a session something closed outright answers with.
func sqlErrConnDone() error {
	return fmt.Errorf("releasing: %w", sql.ErrConnDone)
}

// badConn is what the library answers on a connection it closed because a context was done.
func badConn() error {
	return fmt.Errorf("releasing: %w", sqldriver.ErrBadConn)
}
