package runtime

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// dialed builds the database a fixture reaches, failing the test rather than the language.
func dialed(t *testing.T) *conn {
	t.Helper()

	c, err := newConn("postgres")
	if err != nil {
		t.Fatalf("newConn: %v", err)
	}

	return c
}

func TestTheSessionAnswersReadsInTheOrderTheFixtureStates(t *testing.T) {
	c := dialed(t)
	c.stage([]Read{
		{SQL: "SELECT a", Columns: []string{"n"}, Rows: [][]*string{{text("first")}}},
		{SQL: "SELECT b", Columns: []string{"n"}, Rows: [][]*string{{text("second")}}},
	}, nil)

	for _, expected := range []struct{ sql, value string }{
		{sql: "SELECT a", value: "first"},
		{sql: "SELECT b", value: "second"},
	} {
		rows, err := c.Query(t.Context(), driver.Query{SQL: expected.sql, Args: []string{}})
		if err != nil {
			t.Fatalf("%s: %v", expected.sql, err)
		}

		if *rows.Values[0][0] != expected.value {
			t.Errorf("%s was answered %q, want %q", expected.sql, *rows.Values[0][0], expected.value)
		}
	}

	if problems := c.settled("here", false); len(problems) != 0 {
		t.Errorf("two reads made in order were reported: %v", problems)
	}

	t.Log("Answering out of order would hand a branching migration the wrong row, which is a")
	t.Log("difference in emitted IR that says nothing about the SDK.")
}

func TestTheSessionReportsAStatementTheFixtureNeverStated(t *testing.T) {
	for _, mismatched := range []struct {
		why    string
		reads  []Read
		writes []Write
		sent   driver.Query
		read   bool
		says   string
	}{
		{why: "the SQL is not the SQL stated", read: true,
			reads: []Read{{SQL: "SELECT a"}},
			sent:  driver.Query{SQL: "SELECT b", Args: []string{}},
			says:  "would not read the same thing"},
		{why: "the arguments are not the ones stated", read: true,
			reads: []Read{{SQL: "SELECT a", Args: []string{"1"}}},
			sent:  driver.Query{SQL: "SELECT a", Args: []string{"2"}},
			says:  "bound [2] where the fixture states [1]"},
		{why: "the read was never stated at all", read: true,
			sent: driver.Query{SQL: "SELECT a", Args: []string{}},
			says: "and this fixture states 0 reads"},
		{why: "the statement is not the statement stated",
			writes: []Write{{SQL: "UPDATE t SET a = 1", RowsAffected: "1"}},
			sent:   driver.Query{SQL: "DELETE FROM t", Args: []string{}},
			says:   "would not read the same thing"},
		{why: "the statement was never stated at all",
			sent: driver.Query{SQL: "DELETE FROM t", Args: []string{}},
			says: "and this fixture states 0 statements"},
	} {
		t.Run(mismatched.why, func(t *testing.T) {
			c := dialed(t)
			c.stage(mismatched.reads, mismatched.writes)

			if mismatched.read {
				_, _ = c.Query(t.Context(), mismatched.sent)
			} else {
				_, _ = c.Perform(t.Context(), mismatched.sent)
			}

			assertReports(t, c.settled("here", !mismatched.read), mismatched.says)
		})
	}
}

func TestTheSessionReportsACallTheFixtureStatedAndTheMigrationNeverMade(t *testing.T) {
	c := dialed(t)
	c.stage([]Read{{SQL: "SELECT a"}}, []Write{{SQL: "UPDATE t SET a = 1"}})

	assertReports(t, c.settled("here", true), "made 0 of the 1 reads")
	assertReports(t, c.settled("here", true), "sent 0 of the 1 statements")
}

func TestTheSessionReportsAPreviewThatReachedTheDatabase(t *testing.T) {
	c := dialed(t)
	c.stage(nil, []Write{{SQL: "UPDATE t SET a = 1", RowsAffected: "1"}})

	if _, err := c.Perform(t.Context(),
		driver.Query{SQL: "UPDATE t SET a = 1", Args: []string{}}); err != nil {
		t.Fatalf("performing: %v", err)
	}

	assertReports(t, c.settled("here", false), "a preview reached the database 1 times")

	if problems := c.settled("here", true); len(problems) != 0 {
		t.Errorf("a run that sent exactly what it stated was reported: %v", problems)
	}

	t.Log("ADR 0032 and ADR 0036: under mode plan the engine records host.db.exec rather than")
	t.Log("running it, so a preview that reaches the database has already changed something.")
}

func TestTheSessionReportsWhatItWasToldAStatementChanged(t *testing.T) {
	c := dialed(t)
	c.stage(nil, []Write{{SQL: "UPDATE t SET a = 1", RowsAffected: "7"}})

	changed, err := c.Perform(t.Context(), driver.Query{SQL: "UPDATE t SET a = 1", Args: []string{}})
	if err != nil {
		t.Fatalf("performing: %v", err)
	}

	if changed != 7 {
		t.Errorf("the statement reported %d rows, and the fixture states 7", changed)
	}
}

func TestEveryOperationIsAnsweredNativeSoNoFixtureIsRefusedByTheDatabase(t *testing.T) {
	c := dialed(t)

	if driverName := c.Capabilities().Driver(); driverName != "postgres" {
		t.Errorf("the session declares capabilities for %q", driverName)
	}

	if c.ServerVersion() != serverVersion {
		t.Errorf("the session reports server %q", c.ServerVersion())
	}
}

// text returns a pointer to a value a read returns, since a null one is written as nil.
func text(v string) *string {
	return &v
}
