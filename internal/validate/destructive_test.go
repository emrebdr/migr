package validate

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// dropping is a migration that destroys three different things, one operation each.
func dropping(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.DropTable{Table: "legacy"},
		ir.DropColumn{Table: "users", Column: "email"},
		ir.DropView{View: "listing"},
	}
}

// nothing is an allowance that permits no destruction at all, which is what an empty config means.
var nothing = engine.Allowance{}

// restoring is what undoes the migration above, which is also what the database held before it.
func restoring(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateView{View: "listing", Query: "SELECT id FROM users"},
		ir.AddColumn{Table: "users", Column: ir.Column{
			Name: "email", Type: logical(t, "string(255)"), Nullable: true,
		}},
		ir.CreateTable{Table: "legacy", Columns: []ir.Column{
			{Name: "id", Type: logical(t, "int64")},
		}},
	}
}

// dropped builds the schema the fixture above destroys, so each of its drops is a drop of something.
func dropped(t *testing.T) *schema.Snapshot {
	t.Helper()

	return holdingOnly(t, ir.Operations{
		ir.CreateTable{Table: "users", Columns: []ir.Column{
			{Name: "id", Type: logical(t, "int64")},
			{Name: "email", Type: logical(t, "string(255)"), Nullable: true},
		}},
		ir.CreateTable{Table: "legacy", Columns: []ir.Column{
			{Name: "id", Type: logical(t, "int64")},
		}},
		ir.CreateView{View: "listing", Query: "SELECT id FROM users"},
	})
}

func TestEveryDestructiveOperationIsReportedWithWhatItDestroys(t *testing.T) {
	m := migrationAt(0, "drop_legacy")

	report := runAgainst(t, nothing, dropped(t),
		source{m.Version: {up: dropping(t), down: restoring(t)}}, compiler{}, m)

	problems := report.Problems()
	if len(problems) != 3 {
		t.Fatalf("the report holds %d problems, want one per destructive operation: %+v",
			len(problems), problems)
	}

	for i, want := range []struct {
		kind    ir.Kind
		subject string
	}{
		{ir.KindDropTable, `"legacy"`},
		{ir.KindDropColumn, `"users.email"`},
		{ir.KindDropView, `"listing"`},
	} {
		problem := problems[i]

		if problem.Check != CheckDestructive || problem.Direction != ir.Up {
			t.Errorf("problem %d came from %q going %s", i+1, problem.Check, problem.Direction)
		}

		if problem.Operation != int32(i)+1 || problem.Kind != want.kind {
			t.Errorf("problem %d names operation %d (%s), want %d (%s)", i+1, problem.Operation,
				problem.Kind, i+1, want.kind)
		}

		if !strings.Contains(problem.Detail, want.subject) {
			t.Errorf("problem %d does not name what it destroys: %q", i+1, problem.Detail)
			t.Log("The done-when asks for the migration, the operation, and the affected object.")
			t.Log("A Problem carries the first two in fields; the object is in the detail.")
		}
	}
}

func TestAPermittedKindIsNotAProblem(t *testing.T) {
	m := migrationAt(0, "drop_legacy")
	staged := source{m.Version: {up: dropping(t), down: restoring(t)}}

	live := dropped(t)

	report := runAgainst(t, engine.Allowance{Kinds: []ir.Kind{ir.KindDropTable, ir.KindDropView}},
		live, staged, compiler{}, m)

	problems := report.Problems()
	if len(problems) != 1 || problems[0].Kind != ir.KindDropColumn {
		t.Log("An allowance names kinds rather than migrations, so permitting drop_table permits")
		t.Log("every one of them and leaves drop_column exactly where it was.")
		t.Fatalf("the report holds %+v, want only the kind nothing permitted", problems)
	}

	if !runAgainst(t, engine.Allowance{All: true}, live, staged, compiler{}, m).Clean() {
		t.Error("--allow-destructive left a destructive operation reported")
	}
}

func TestADerivedInverseIsNotJudgedDestructive(t *testing.T) {
	m := migrationAt(0, "add_users")

	report := runGated(t, nothing, source{m.Version: {up: ir.Operations{table(t, "users")}}},
		compiler{}, m)

	if !report.Clean() {
		t.Errorf("creating a table was reported as destructive: %+v", report.Problems())
		t.Log("ir.CreateTable inverts to DropTable, so a run that judged the down half would")
		t.Log("report every migration that ever created a table, and --allow-destructive would be")
		t.Log("mandatory for migr down outright. The up half is what applying a migration does.")
	}
}

func TestAnAuthoredDownThatDropsSomethingIsNotJudgedEither(t *testing.T) {
	m := migrationAt(0, "add_users")

	report := runGated(t, nothing, source{m.Version: {
		up:   ir.Operations{table(t, "users")},
		down: ir.Operations{ir.DropTable{Table: "users"}},
	}}, compiler{}, m)

	if !report.Clean() {
		t.Errorf("an authored down that drops what its up created was reported: %+v",
			report.Problems())
		t.Log("The same rule, written by hand rather than derived: a rollback undoes what it was")
		t.Log("asked to undo, and a down that dropped nothing would not be an undo at all.")
	}
}

func TestADestructiveOperationStopsNoOtherCheck(t *testing.T) {
	m := migrationAt(0, "drop_legacy")

	report := runAgainst(t, nothing, dropped(t), source{m.Version: {
		up:   ir.Operations{ir.DropTable{Table: "legacy"}},
		down: ir.Operations{raw("CREATE TABLE legacy (id bigint);")},
	}}, compiler{refuseKind: ir.KindDropTable}, m)

	if got := checksFound(report); !slices.Equal(got, []Check{CheckDestructive, CheckCompile}) {
		t.Errorf("the report holds %q, want both the destruction and the refusal", got)
		t.Log("Compiling does not depend on being permitted, so reporting every problem leaves both")
		t.Log("reported: an operator fixing one sees the other in the same pass.")
	}
}

func TestDestructiveIsOneOfTheChecksARunPerforms(t *testing.T) {
	if !slices.Contains(Checks(), CheckDestructive) {
		t.Error("the destructive check is not in Checks()")
	}

	if !CheckDestructive.Valid() {
		t.Error("the destructive check does not report itself as a check")
	}
}
