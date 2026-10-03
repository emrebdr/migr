package validate

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// emulating is a compiler that performs one operation only against a database it was not given.
type emulating struct{}

// Compile reports that alter_column is emulated, and compiles everything else.
func (emulating) Compile(op ir.Operation) ([]driver.Statement, error) {
	if op.Kind() == ir.KindAlterColumn {
		return nil, driver.NeedsSession("fake", op.Kind(), "it is performed by a table rebuild")
	}

	return []driver.Statement{{SQL: "-- " + op.Kind().String()}}, nil
}

// emulated runs a validation whose compiler emulates one operation, with no session to expand it.
func emulated(t *testing.T, ops ir.Operations) Report {
	t.Helper()

	m := migrationAt(1, "alter_email")

	report, err := Run(t.Context(), Options{
		Migrations: []migration.Migration{m},
		Source:     source{m.Version: {up: ops, down: ops}},
		Planner:    plan.Planner{Compiler: emulating{}},
		Allow:      engine.Allowance{All: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	return report
}

// retype is an alteration a driver would perform by rebuilding the table.
func retype(t *testing.T) ir.AlterColumn {
	t.Helper()

	nullable := true

	return ir.AlterColumn{Table: "users", Column: "email", Nullable: &nullable}
}

func TestAnEmulatedOperationIsReportedUncheckedRatherThanAsAProblem(t *testing.T) {
	report := emulated(t, ir.Operations{retype(t)})

	if !report.Clean() {
		t.Errorf("an emulated operation was reported as a problem: %v", report.Problems())
		t.Log("It is not one. The driver performs it; what it needs is a database, and this")
		t.Log("command deliberately opens none -- so the honest answer is that nothing judged it.")
	}

	unchecked := report.Unchecked()
	if len(unchecked) == 0 {
		t.Fatal("nothing was reported unchecked, so a half nothing judged reads as one that passed")
	}

	if unchecked[0].Kind != ir.KindAlterColumn || unchecked[0].Operation != 1 {
		t.Errorf("the entry names %s operation %d", unchecked[0].Kind, unchecked[0].Operation)
	}

	if !strings.Contains(unchecked[0].Reason, "table rebuild") {
		t.Errorf("the reason does not say what needs the database: %q", unchecked[0].Reason)
	}

	t.Log("This is ADR 0040's shape reused: a Go migration that reads the database is reported")
	t.Log("unchecked rather than failed, and an operation the driver compiles against one is the")
	t.Log("same claim about a different half of the same command.")
}

func TestOneEmulatedOperationDoesNotMakeTheRestUnchecked(t *testing.T) {
	report := emulated(t, ir.Operations{
		ir.DropTable{Table: "users"},
		retype(t),
		ir.DropView{View: "listing"},
	})

	if got := len(report.Unchecked()); got != 2 {
		t.Errorf("%d entries were reported unchecked, want one per direction", got)
	}

	for _, entry := range report.Unchecked() {
		if entry.Operation != 2 {
			t.Errorf("the entry names operation %d, want the one that is emulated", entry.Operation)
		}
	}

	t.Log("An operation is the unit here, not the half: the two either side of it were compiled")
	t.Log("and judged, and saying otherwise would understate what the command actually checked.")
}
