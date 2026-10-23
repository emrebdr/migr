package validate

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

func TestEveryMigrationIsReadAsAQuestion(t *testing.T) {
	users := migrationAt(0, "add_users")

	src := &asked{source: source{
		users.Version: {up: ir.Operations{raw("CREATE TABLE users (id bigint);")},
			down: ir.Operations{raw("DROP TABLE users;")}},
	}}

	if _, err := Run(t.Context(), Options{
		Migrations: []migration.Migration{users},
		Source:     src,
		Planner:    plan.Planner{Compiler: compiler{}},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []engine.Mode{engine.ModePlan, engine.ModePlan}
	if !slices.Equal(src.modes, want) {
		t.Errorf("validation read the migration in %v, want %v", src.modes, want)
		t.Log("migr validate opens no database at all, and a check that ran the")
		t.Log("escape hatch would not be one. Both halves are questions, and a source reached")
		t.Log("through a language runtime is the one that can tell the difference.")
	}
}

func TestACleanProjectReportsNothing(t *testing.T) {
	users, teams := migrationAt(0, "add_users"), migrationAt(1, "add_teams")

	report := run(t, source{
		users.Version: {up: ir.Operations{raw("CREATE TABLE users (id bigint);")},
			down: ir.Operations{raw("DROP TABLE users;")}},
		teams.Version: {up: ir.Operations{raw("CREATE TABLE teams (id bigint);")},
			declared: "the rows are gone"},
	}, compiler{}, users, teams)

	if !report.Clean() {
		t.Errorf("a project with an authored down and a declared one reported %+v", report.Problems())
	}

	if report.Checked() != 2 {
		t.Errorf("the report says %d migrations were checked, want both", report.Checked())
	}
}

func TestEveryMigrationIsCheckedRatherThanTheFirstThatFails(t *testing.T) {
	first, second, third := migrationAt(0, "one"), migrationAt(1, "two"), migrationAt(2, "three")

	report := run(t, source{
		first.Version:  {unreadable: "it has no marker"},
		second.Version: {up: ir.Operations{raw("CREATE TABLE b (id bigint);")}},
		third.Version:  {unreadable: "it has two markers"},
	}, compiler{}, first, second, third)

	want := []string{first.Filename(), second.Filename(), third.Filename()}

	if got := filesFound(report); !slices.Equal(got, want) {
		t.Errorf("the report names %q, want a problem in each of the three", got)
		t.Log("A migration that cannot be read tells you nothing about the next one, so a run")
		t.Log("that stopped there would send an operator round the loop once per broken file.")
	}

	if report.Migrations() != 3 {
		t.Errorf("the report counts %d faulted migrations, want three", report.Migrations())
	}
}

func TestAMigrationThatWillNotParseProducesOneProblemRatherThanThree(t *testing.T) {
	broken := migrationAt(0, "broken")

	report := run(t, source{broken.Version: {unreadable: "it has no marker"}}, compiler{}, broken)

	if got := checksFound(report); !slices.Equal(got, []Check{CheckFormat}) {
		t.Errorf("the report holds %q, want the one check that could run", got)
		t.Log("Compiling a file nothing could read, and reversing it, are unanswerable rather")
		t.Log("than wrong -- reporting them would be three complaints about one defect.")
	}
}

func TestEveryRefusedOperationIsReportedRatherThanTheFirst(t *testing.T) {
	both := migrationAt(0, "two_refusals")
	refused := "CREATE TABLE b (id bigint);"

	report := run(t, source{both.Version: {
		up:   ir.Operations{raw(refused), raw(refused)},
		down: ir.Operations{raw("DROP TABLE b;")},
	}}, compiler{refuse: refused}, both)

	if got := len(report.Problems()); got != 2 {
		t.Log("plan.Compile fails a migration whole, which is right for a run and wrong for a")
		t.Log("report: an operator fixing one refusal wants to see the other in the same pass.")
		t.Fatalf("the report holds %d problems, want one per refused operation", got)
	}

	if first := report.Problems()[0]; first.Operation != 1 || first.Kind != ir.KindRaw {
		t.Errorf("the first problem is at %+v, want operation 1 counting from one", first)
	}

	if second := report.Problems()[1]; second.Operation != 2 {
		t.Errorf("the second problem is at operation %d, want 2", second.Operation)
	}

	if report.Migrations() != 1 {
		t.Errorf("the report counts %d faulted migrations, want the one both problems are in",
			report.Migrations())
	}
}

func TestAPartlyReversibleMigrationCompilesNoHalfOfItsDown(t *testing.T) {
	half := migrationAt(0, "half_reversible")

	report := run(t, source{half.Version: {
		up: ir.Operations{table(t, "users"), raw("DROP TABLE legacy;")},
	}}, compiler{refuseKind: ir.KindDropTable}, half)

	if got := checksFound(report); !slices.Equal(got, []Check{CheckReverse}) {
		t.Errorf("the report holds %q, want only the operation nothing can undo", got)
		t.Log("ir.ReverseAll fails a list whole because a rollback must not perform half of one.")
		t.Log("Compiling the half that did invert would report a refusal in a down that could")
		t.Log("never be assembled -- a complaint about something nothing will ever run.")
	}
}

func TestAMigrationNothingCanUndoIsReported(t *testing.T) {
	oneWay := migrationAt(0, "one_way")

	report := run(t, source{oneWay.Version: {
		up: ir.Operations{raw("DROP TABLE legacy;")},
	}}, compiler{}, oneWay)

	problems := report.Problems()

	if len(problems) != 1 || problems[0].Check != CheckReverse {
		t.Fatalf("the report holds %+v, want the migration nothing can undo", problems)
	}

	if problems[0].Direction != ir.Down || problems[0].Operation != 1 {
		t.Errorf("the problem sits at %s operation %d, want the down it could not derive",
			problems[0].Direction, problems[0].Operation)
	}

	if problems[0].Detail == "" {
		t.Errorf("the problem gives no reason the operation has no inverse")
	}
}

func TestEveryOperationWithNoInverseIsReportedRatherThanTheFirst(t *testing.T) {
	oneWay := migrationAt(0, "one_way")

	report := run(t, source{oneWay.Version: {
		up: ir.Operations{raw("DROP TABLE a;"), raw("DROP TABLE b;")},
	}}, compiler{}, oneWay)

	if got := len(report.Problems()); got != 2 {
		t.Errorf("the report holds %d problems, want one per operation with no inverse", got)
		t.Log("ir.ReverseAll fails whole because a rollback must, and stopping at the first is")
		t.Log("right there. A report wants every one, and op.Reverse is the shared primitive.")
	}
}

func TestADeclaredIrreversibleMigrationIsNotAProblem(t *testing.T) {
	declared := migrationAt(0, "declared")

	report := run(t, source{declared.Version: {
		up:       ir.Operations{raw("DROP TABLE legacy;")},
		declared: "the rows are gone; restore from a backup",
	}}, compiler{}, declared)

	if !report.Clean() {
		t.Errorf("a migration whose author declared it one-way reported %+v", report.Problems())
		t.Log("Invariant #10 allows a Down to declare itself irreversible with a reason. A check")
		t.Log("that flagged it anyway would make the declaration a thing nobody could satisfy.")
	}
}

func TestADerivedDownIsCompiledRatherThanAssumed(t *testing.T) {
	derived := migrationAt(0, "derived")

	report := run(t, source{derived.Version: {
		up: ir.Operations{table(t, "users")},
	}}, compiler{refuseKind: ir.KindDropTable}, derived)

	problems := report.Problems()

	if len(problems) != 1 || problems[0].Check != CheckCompile {
		t.Fatalf("the report holds %+v, want the refusal the derived down compiles to", problems)
	}

	if problems[0].Direction != ir.Down {
		t.Errorf("the problem is reported against %s, want the direction it was compiled in",
			problems[0].Direction)
		t.Log("An inverse can be refused by a driver that accepted what it inverts, so deriving")
		t.Log("one and stopping there would pass a rollback nothing can run.")
	}
}

func TestAnAuthoredDownIsCompiledToo(t *testing.T) {
	authored := migrationAt(0, "authored")
	refused := "DROP TABLE users;"

	report := run(t, source{authored.Version: {
		up:   ir.Operations{raw("CREATE TABLE users (id bigint);")},
		down: ir.Operations{raw(refused)},
	}}, compiler{refuse: refused}, authored)

	if got := len(report.Problems()); got != 1 {
		t.Fatalf("the report holds %d problems, want the refusal in the authored down", got)
	}

	if got := report.Problems()[0].Direction; got != ir.Down {
		t.Errorf("the problem is reported against %s, want down", got)
	}
}

func TestAMalformedDownSectionIsAFormatProblem(t *testing.T) {
	broken := migrationAt(0, "broken_down")

	report := run(t, source{broken.Version: {
		up:             ir.Operations{table(t, "users")},
		downUnreadable: "its down section holds SQL above the marker",
	}}, compiler{}, broken)

	problems := report.Problems()

	if len(problems) != 1 || problems[0].Check != CheckFormat {
		t.Fatalf("the report holds %+v, want the down section that could not be read", problems)
	}

	if problems[0].Direction != ir.Down {
		t.Errorf("the problem is reported against %s, want the half it is in", problems[0].Direction)
		t.Log("A down section this source refuses is neither an absent down nor a declared one:")
		t.Log("deriving an inverse for it would answer a question the file did try to answer.")
	}
}

func TestAProblemNeverRepeatsTheMigrationItNames(t *testing.T) {
	broken := migrationAt(0, "broken")

	report := run(t, source{broken.Version: {unreadable: "it has no marker"}}, compiler{}, broken)

	if got := report.Problems()[0].Detail; got != "it has no marker" {
		t.Errorf("the detail is %q, want it without the filename the problem already carries", got)
	}
}

func TestARunWithNothingToCompileThroughIsRefused(t *testing.T) {
	if _, err := Run(t.Context(), Options{Source: source{}}); !errors.Is(err, plan.ErrNoCompiler) {
		t.Errorf("Run with no compiler returned %v, want the planner's refusal", err)
	}

	if _, err := Run(t.Context(), Options{
		Planner: plan.Planner{Compiler: compiler{}},
	}); !errors.Is(err, ErrNoSource) {
		t.Errorf("Run with no source returned %v, want ErrNoSource", err)
	}
}

func TestAReportsOrderIsTheOrderARunWouldPerformThem(t *testing.T) {
	first, second := migrationAt(0, "first"), migrationAt(1, "second")
	staged := source{
		first.Version:  {unreadable: "broken"},
		second.Version: {unreadable: "broken"},
	}

	for range 5 {
		report := run(t, staged, compiler{}, first, second)

		want := []string{first.Filename(), second.Filename()}
		if got := filesFound(report); !slices.Equal(got, want) {
			t.Fatalf("the report ordered %q, want %q every time", got, want)
		}
	}
}

func TestEveryCheckIsNamed(t *testing.T) {
	for _, check := range Checks() {
		if !check.Valid() || check.String() == "" {
			t.Errorf("check %q does not name itself", check)
		}
	}

	if Check("nonsense").Valid() {
		t.Errorf("a check nothing declares reported itself valid")
	}
}

func TestAMigrationThatReachesTheDatabaseItselfCannotHaveItsDownDerived(t *testing.T) {
	backfilled := migrationAt(0, "backfill_status")

	report := run(t, source{backfilled.Version: {
		up:     ir.Operations{table(t, "users")},
		direct: []driver.Query{{SQL: "UPDATE users SET status = 'active'"}},
	}}, compiler{}, backfilled)

	problems := report.Problems()

	t.Log("Every operation here inverts, so nothing else reports a problem -- and a rollback")
	t.Log("would silently revert the schema and leave the UPDATE standing.")

	if len(problems) != 1 || problems[0].Check != CheckReverse {
		t.Fatalf("the report holds %+v, want the migration nothing can undo", problems)
	}

	if !strings.Contains(problems[0].Detail, "UPDATE users SET status") {
		t.Errorf("the problem does not name the statement it cannot invert: %q", problems[0].Detail)
	}
}

func TestAMigrationThatReachesTheDatabaseItselfAndAuthorsADownIsFine(t *testing.T) {
	backfilled := migrationAt(0, "backfill_status")

	report := run(t, source{backfilled.Version: {
		up:     ir.Operations{table(t, "users")},
		down:   ir.Operations{raw("DROP TABLE users;")},
		direct: []driver.Query{{SQL: "UPDATE users SET status = 'active'"}},
	}}, compiler{}, backfilled)

	if problems := report.Problems(); len(problems) != 0 {
		t.Errorf("an authored down was refused for the up's own statements: %+v", problems)
	}
}
