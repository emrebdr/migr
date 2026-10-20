package cli

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/schema"
)

// decodePlan reads the document --json promised, failing the test rather than the command.
func decodePlan(t *testing.T, stdout string) compiledPlan {
	t.Helper()

	var plan compiledPlan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("migr plan --json emitted %v:\n%s", err, stdout)
	}

	return plan
}

// statementsIn returns every statement a decoded plan would send, flattened in the order it holds them.
func statementsIn(plan compiledPlan) []string {
	var sent []string

	for _, m := range plan.Migrations {
		for _, op := range m.Operations {
			sent = append(sent, op.Statements...)
		}
	}

	return sent
}

// verdictsIn returns how the driver answered for each operation, in the order the plan holds them.
func verdictsIn(plan compiledPlan) []string {
	var answered []string

	for _, m := range plan.Migrations {
		for _, op := range m.Operations {
			answered = append(answered, describeVerdict(op))
		}
	}

	return answered
}

func TestPlanPrintsEveryStatementItWouldSend(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	want := []string{
		"CREATE TABLE users (id bigint);",
		"CREATE INDEX idx_users ON users (id);",
		"CREATE TABLE teams (id bigint);",
	}

	if got := statementsIn(decodePlan(t, stdout)); !slices.Equal(got, want) {
		t.Errorf("plan would send %q, want %q", got, want)
		t.Log("These are the same statements migr up runs, off the same pipeline: a preview stops")
		t.Log("after compiling, so there is no second implementation to drift out of sync.")
	}
}

func TestPlanGroupsStatementsUnderTheOperationTheyCameFrom(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	plan := decodePlan(t, stdout)

	if len(plan.Migrations) != 2 {
		t.Fatalf("plan holds %d migrations, want one per pending migration", len(plan.Migrations))
	}

	first := plan.Migrations[0]

	if len(first.Operations) != 2 {
		t.Fatalf("%s holds %d operations, want one per statement in the file",
			first.File, len(first.Operations))
	}

	if first.Operations[1].Index != 2 || len(first.Operations[1].Statements) != 1 {
		t.Errorf("the second operation is %+v, want index 2 carrying its own statement",
			first.Operations[1])
		t.Log("A step is an operation paired with what it became, so a statement is always")
		t.Log("attributable to the operation that produced it (invariant #9).")
	}

	if plan.Statements != 3 {
		t.Errorf("plan counted %d statements across the whole run, want 3", plan.Statements)
	}
}

func TestPlanExecutesNothingAndRecordsNothing(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "plan"); err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, forbidden := range []string{"ensure", "begin", "commit", "record"} {
		if f.asked(forbidden) {
			t.Errorf("plan asked the session for %q: %v", forbidden, f.calls)
		}
	}

	if len(f.statements) != 0 {
		t.Errorf("plan executed %q", f.statements)
	}

	if !f.asked("lock") || !f.asked("unlock") {
		t.Errorf("plan ran without the migration lock: %v", f.calls)
		t.Log("Without touching the database means it writes nothing, not that it connects to")
		t.Log("nothing: knowing which migrations are pending needs a reconciled report, and")
		t.Log("reconciling outside the lock reports on a database another run is changing.")
	}
}

func TestPlanDownCompilesTheRollbackRatherThanTheRun(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan", "--down")
	if err != nil {
		t.Fatalf("plan --down: %v", err)
	}

	plan := decodePlan(t, stdout)

	want := []string{"DROP TABLE teams;", "DROP TABLE users;"}

	if got := statementsIn(plan); !slices.Equal(got, want) {
		t.Errorf("plan --down would send %q, want the undo statements newest first", got)
		t.Log("A plan that queued the rollback and compiled everything forwards would print the")
		t.Log("statements that apply a migration under the heading of undoing it.")
	}

	if plan.Direction != "down" {
		t.Errorf("plan --down reports direction %q", plan.Direction)
	}
}

func TestPlanDownCoversTheLastBatchRatherThanEverythingApplied(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	for _, args := range [][]string{{"up", "--step", "1"}, {"up"}} {
		if _, _, err := runWith(t, dir, noon, drivers, args...); err != nil {
			t.Fatalf("migr %v: %v", args, err)
		}
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan", "--down")
	if err != nil {
		t.Fatalf("plan --down: %v", err)
	}

	if got := statementsIn(decodePlan(t, stdout)); !slices.Equal(got, []string{"DROP TABLE teams;"}) {
		t.Errorf("plan --down would send %q, want only the last batch's undo", got)
		t.Log("A bare rollback acts on the last batch, not on everything applied, and the scope")
		t.Log("only knows that if it is asked for the direction the flags actually chose. With")
		t.Log("the whole project in one batch, All() and LastBatch() agree everywhere.")
	}
}

func TestPlanLabelsAnEmulatedOperationWithTheStrategyItUses(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.emulates = "table rebuild"

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if !strings.Contains(stdout, "emulated: table rebuild") {
		t.Errorf("plan does not say the operation is emulated or how:\n%s", stdout)
		t.Log("CLAUDE.md §4.2: a capability is never silently degraded -- emulation is surfaced in")
		t.Log("migr plan output so the operator sees that a table rebuild is about to happen.")
		t.Log("No driver in this repository emits Emulated yet; SQLite is the first (M8-T6).")
	}
}

func TestPlanShowsOneOperationBecomingSeveralStatements(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.emulates = "table rebuild"

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	plan := decodePlan(t, stdout)

	first := plan.Migrations[0].Operations[0]
	if len(first.Statements) != 2 {
		t.Fatalf("the emulated operation compiles to %d statements, want the rebuild's own",
			len(first.Statements))
	}

	if first.Statements[1] != emulationStep(f.emulates) {
		t.Errorf("the operation's second statement is %q", first.Statements[1])
	}

	if plan.Statements != 4 {
		t.Errorf("plan counted %d statements across 2 operations that emit 2 each", plan.Statements)
		t.Log("An operation and its statements are not one to one, and a count that reads the")
		t.Log("operations would agree with the statements everywhere a .sql project can reach.")
	}
}

func TestPlanLabelsAnOperationTheEnginePerformsDirectly(t *testing.T) {
	dir, _, drivers := project(t, twoTables()[:1]...)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if got := verdictsIn(decodePlan(t, stdout)); !slices.Equal(got, []string{"native", "native"}) {
		t.Errorf("plan answered %q for the operations, want native", got)
		t.Log("The verdict is printed for every operation rather than only the emulated ones: a")
		t.Log("label that appears only when something is unusual is one nobody learns to read.")
	}
}

func TestPlanNarrowsTheSameWayARunDoes(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan", "--step", "1")
	if err != nil {
		t.Fatalf("plan --step 1: %v", err)
	}

	plan := decodePlan(t, stdout)

	if len(plan.Migrations) != 1 || plan.Migrations[0].File != fileAt(0, "add_users") {
		t.Errorf("plan --step 1 covers %d migrations, want only the first", len(plan.Migrations))
	}

	if len(plan.Skipped) != 1 {
		t.Errorf("plan --step 1 skipped %d, want the one the scope left short", len(plan.Skipped))
	}
}

func TestPlanRefusesTwoSelectorsThatNameDifferentSets(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	_, _, err := runWith(t, dir, noon, drivers, "plan", "--step", "1", "--to", versionAt(1))

	if !errors.Is(err, errBothSelectors) || ExitCode(err) != ExitValidation {
		t.Errorf("plan with both selectors returned %v (exit %d)", err, ExitCode(err))
	}
}

func TestPlanPrintsWhatCompiledBeforeTheOperationItCouldNotCompile(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.unsupported = "CREATE TABLE teams (id bigint);"

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err == nil {
		t.Fatal("plan succeeded over an operation the driver refuses")
	}

	if !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("plan returned %v, want the driver's refusal", err)
	}

	if !strings.Contains(stdout, "CREATE TABLE users (id bigint);") {
		t.Errorf("plan printed nothing about the migration that compiled cleanly:\n%s", stdout)
		t.Log("A run that did nothing prints no summary, because nothing happened. A plan that")
		t.Log("compiled two of three migrations produced two thirds of what was asked for, and")
		t.Log("the refusal is the answer that the compiled ones are the context for.")
	}

	if strings.Contains(stdout, "CREATE TABLE teams (id bigint);") {
		t.Errorf("plan printed statements for an operation nothing compiled:\n%s", stdout)
	}
}

func TestPlanPrintsNothingWhenTheFirstMigrationIsRefused(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.unsupported = "CREATE TABLE users (id bigint);"

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err == nil {
		t.Fatal("plan succeeded over an operation the driver refuses")
	}

	if stdout != "" {
		t.Errorf("plan printed a heading above a refusal that compiled nothing:\n%s", stdout)
	}
}

func TestPlanOfAnUnauthoredDownRefusesBeforeAnythingRuns(t *testing.T) {
	dir, _, drivers := project(t, stagedMigration{
		name: "add_users",
		up:   "CREATE TABLE users (id bigint);",
	})

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	_, _, err := runWith(t, dir, noon, drivers, "plan", "--down")
	if err == nil {
		t.Fatal("plan --down succeeded over a migration nothing can reverse")
	}

	if !strings.Contains(err.Error(), fileAt(0, "add_users")) {
		t.Errorf("plan --down refused with %v, want the file named", err)
		t.Log("A .sql migration reaches the database as ir.Raw, which has no inverse, so its")
		t.Log("derived down is a refusal rather than statements. This is where an operator can")
		t.Log("learn that before migr down finds it out newest-first (ADR 0022, ADR 0025).")
	}
}

func TestPlanMarksAStatementThatRunsOutsideAnyTransaction(t *testing.T) {
	dir, _, drivers := project(t, concurrentIndex())

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan returned %v, want the split migration printed", err)
	}

	for _, want := range []string{
		"[no transaction]",
		"runs in 2 parts, each committing on its own",
		"marked [no transaction] above",
		"not tried again",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not say %q:\n%s", want, stdout)
		}
	}

	lines := strings.Split(stdout, "\n")
	for i, line := range lines {
		if strings.Contains(line, "CREATE INDEX CONCURRENTLY") &&
			(i == 0 || strings.TrimSpace(lines[i-1]) != "[no transaction]") {
			t.Errorf("the line above the statement it marks is not the marker:\n%s", stdout)
		}
	}

	t.Log("M5-T4 wrote this marker and deleted it, because the executor refused every migration")
	t.Log("that could carry one. M13-T4 runs them, so the plan says which statement commits alone.")
}

func TestPlanJSONListsTheStatementsThatRunOutsideAnyTransaction(t *testing.T) {
	dir, _, drivers := project(t, concurrentIndex())

	stdout, _, err := runWith(t, dir, noon, drivers, "plan", "--json")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	var doc struct {
		Migrations []struct {
			Parts      int `json:"parts"`
			Operations []struct {
				NoTransaction []int `json:"no_transaction"`
			} `json:"operations"`
		} `json:"migrations"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decoding the plan: %v\n%s", err, stdout)
	}

	m := doc.Migrations[0]
	if m.Parts != 2 || len(m.Operations) != 2 || m.Operations[0].NoTransaction != nil ||
		!slices.Equal(m.Operations[1].NoTransaction, []int{1}) {
		t.Errorf("the plan document reads %+v, want 2 parts and the second operation's one statement", m)
	}
}

// concurrentIndex is a migration whose second statement declares it cannot run inside a transaction.
func concurrentIndex() stagedMigration {
	return stagedMigration{
		name: "add_users",
		up: "CREATE TABLE users (id bigint);\n-- migr:no-transaction\n" +
			"CREATE INDEX CONCURRENTLY idx_users ON users (id);",
		down: "DROP TABLE users;",
	}
}
func TestPlanOverADatabaseWithNoHistoryTableSaysSo(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if !strings.Contains(stdout, "no migration history table yet") {
		t.Errorf("plan did not say the history table is absent:\n%s", stdout)
		t.Log("Planning against a database migr has never touched is the normal first use.")
	}
}

func TestPlanJSONIsValidAndCarriesNothingOnStderr(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, stderr, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	if stderr != "" {
		t.Errorf("plan --json wrote %q to stderr", stderr)
	}

	if got := decodePlan(t, stdout); got.Database != "default" || got.Batch != 1 {
		t.Errorf("plan --json reported %+v, want the target and the batch it would record under", got)
	}
}

func TestAMultiLineStatementStaysInItsColumn(t *testing.T) {
	got := indented("CREATE TABLE users (\n  id bigint\n);")

	want := statementIndent + "CREATE TABLE users (\n" +
		statementIndent + "  id bigint\n" +
		statementIndent + ");"

	if got != want {
		t.Errorf("indented() = %q, want %q", got, want)
	}
}

func TestADryRunPointsAtTheCommandThatPrintsTheStatements(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	up, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if !strings.Contains(up, "migr plan") || strings.Contains(up, "migr plan --down") {
		t.Errorf("the dry run does not point at `migr plan`:\n%s", up)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	down, _, err := runWith(t, dir, noon, drivers, "down", "--dry-run")
	if err != nil {
		t.Fatalf("down --dry-run: %v", err)
	}

	if !strings.Contains(down, "migr plan --down") {
		t.Errorf("the rollback's dry run does not point at `migr plan --down`:\n%s", down)
	}
}

// previewOf compiles one migration's operations the way a preview would, for the rendering tests.
func previewOf(t *testing.T, f *fake, ops ir.Operations, held []driver.Query) engine.Previewed {
	t.Helper()

	m := migration.Migration{Version: "20260727T153000", Name: "backfill_status", Ext: ".sql"}

	compiled, err := plan.Planner{Compiler: &fakeDriver{fake: f, caps: f.capabilities(t)}}.
		Compile(m, ir.Up, ops)
	if err != nil {
		t.Fatalf("compiling the staged operations: %v", err)
	}

	return engine.Previewed{
		Direction: ir.Up, Batch: 1, Recorded: true,
		Steps: []engine.Planned{{Ordinal: 1, Plan: compiled, Direct: held}},
	}
}

// renderedPlan runs the preview through the rendering both migr plan output forms are built from.
func renderedPlan(t *testing.T, held []driver.Query) compiledPlan {
	t.Helper()

	f := &fake{}
	s := &session{conn: &fakeConn{fake: f, caps: f.capabilities(t)}, database: "default"}

	return compiledOf(s, previewOf(t, f, ir.Operations{
		ir.Raw{Driver: fakeName, SQL: "ALTER TABLE users ADD COLUMN status text;"},
	}, held))
}

func TestPlanPrintsTheStatementsAMigrationRunsItself(t *testing.T) {
	rendered := renderedPlan(t, []driver.Query{
		{SQL: "UPDATE users SET status = 'active'", Args: []string{"active"}},
	})

	if rendered.Direct != 1 {
		t.Fatalf("the plan counted %d direct statements, want the one held", rendered.Direct)
	}

	if len(rendered.Migrations[0].Direct) != 1 ||
		rendered.Migrations[0].Direct[0].SQL != "UPDATE users SET status = 'active'" {
		t.Fatalf("the migration carries %+v", rendered.Migrations[0].Direct)
	}

	printed := rendered.String()

	if !strings.Contains(printed, "UPDATE users SET status = 'active'") {
		t.Errorf("the printed plan omits the statement migr up would run:\n%s", printed)
	}

	if !strings.Contains(printed, "outside its transaction") {
		t.Errorf("the printed plan does not say the statement runs outside the plan:\n%s", printed)
	}

	if !strings.Contains(printed, "a later failure does not undo it") {
		t.Errorf("the printed plan does not say it is not undone by a failure:\n%s", printed)
	}
}

func TestPlanPrintsAHeldStatementBeforeTheOperationsItPrecedes(t *testing.T) {
	printed := renderedPlan(t, []driver.Query{{SQL: "UPDATE users SET status = 'active'"}}).String()

	held := strings.Index(printed, "UPDATE users")
	operation := strings.Index(printed, "ALTER TABLE users")

	t.Log("Every callback fires while the migration is deciding what it does, which is before")
	t.Log("the engine has compiled anything or opened a transaction (ADR 0032).")

	if held < 0 || operation < 0 || held > operation {
		t.Errorf("the held statement is not printed above the operations:\n%s", printed)
	}
}

func TestPlanCountsBoundArgumentsRatherThanInterpolatingThem(t *testing.T) {
	printed := renderedPlan(t, []driver.Query{
		{SQL: "UPDATE users SET status = $1", Args: []string{"active"}},
	}).String()

	if !strings.Contains(printed, "-- 1 argument bound") {
		t.Errorf("the plan does not say how many arguments are bound:\n%s", printed)
	}

	t.Log("An argument is sent separately and never interpolated, so rendering it inside the")
	t.Log("statement would print SQL that is not the SQL that runs.")

	if strings.Contains(printed, "SET status = 'active'") {
		t.Errorf("the plan interpolated a bound argument into the statement:\n%s", printed)
	}
}

func TestAPlanWithNothingHeldSaysNothingAboutTheEscapeHatch(t *testing.T) {
	rendered := renderedPlan(t, nil)
	printed := rendered.String()

	if rendered.Direct != 0 || rendered.Migrations[0].Direct != nil {
		t.Errorf("a plan that held nothing carries %+v", rendered.Migrations[0].Direct)
	}

	if strings.Contains(printed, "escape hatch") || strings.Contains(printed, "!") {
		t.Errorf("a plan that held nothing mentioned the escape hatch:\n%s", printed)
	}
}

func TestAHeldStatementIsMarkedInTheTextRendering(t *testing.T) {
	printed := renderedPlan(t, []driver.Query{{SQL: "UPDATE users SET status = 'active'"}}).String()

	t.Log("The footer says \"marked ! above\", so the marker has to actually be above -- a")
	t.Log("test that only looked for a ! anywhere would pass on the footer alone.")

	if !strings.Contains(printed, "!  1 statement this migration runs itself") {
		t.Errorf("nothing marks the held statement apart from the operations:\n%s", printed)
	}
}

func TestALanguageIsRefusedWhenNoRegistryWasBuiltAtAll(t *testing.T) {
	_, err := runnable(config.LanguageGo, nil)
	if err == nil {
		t.Fatal("runnable returned an adapter for a build with no runtime registry")
	}

	t.Log("newRootCmd builds options with neither registry, so a nil one is reachable and")
	t.Log("must say what is missing rather than dereference its way to a panic.")

	if !errors.Is(err, errNoRuntimes) {
		t.Errorf("runnable returned %v, want %v", err, errNoRuntimes)
	}
}

func TestPlanSaysWhichRowsADataOperationWrites(t *testing.T) {
	dir, _, drivers := goProject(t, "plans")

	stdout, _, err := runScripted(t, dir, actBackfills, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if want := `rows: fills the NULLs of "users.plan" with "free", and no other row`; !strings.Contains(stdout, want) {
		t.Errorf("the plan does not say which rows the backfill writes:\n%s", stdout)
		t.Log("A data operation is one statement over a whole table, so its statement alone says")
		t.Log("what it writes and not how much of the table that is.")
	}
}

func TestEveryDataOperationSaysWhichRowsItWrites(t *testing.T) {
	text, err := schema.Parse("text")
	if err != nil {
		t.Fatalf("parsing a type: %v", err)
	}

	cases := map[ir.Kind]struct {
		op   ir.Operation
		want string
	}{
		ir.KindDataCopy: {ir.DataCopy{From: "users", To: "archive", Columns: []ir.CopyColumn{
			{From: "id", To: "id"}, {From: "email", To: "email"}}},
			`copies every row of "users" into "archive", 2 columns`},
		ir.KindDataBackfill: {ir.DataBackfill{Table: "users", Column: "id", Type: text,
			Value: ir.Call(ir.FuncUUID)}, `fills the NULLs of "users.id" with uuid(), and no other row`},
		ir.KindDataTransform: {ir.DataTransform{Table: "users", Column: "key", Type: text,
			From: "email", Function: ir.TransformTrim},
			`rewrites "users.key" in every row, from "email" through trim`},
	}

	for _, kind := range ir.DataKinds() {
		c, held := cases[kind]
		if !held {
			t.Errorf("no case for %s, so nothing holds the plan to saying what it writes", kind)

			continue
		}

		if got := rowsOf(c.op); got != c.want {
			t.Errorf("%s reads %q, want %q", kind, got, c.want)
		}
	}

	if got := rowsOf(ir.DropView{View: "v"}); got != "" {
		t.Errorf("a schema operation says it writes rows: %q", got)
	}
}

func TestUpRunsAMigrationSplitAroundAStatementOutsideAnyTransaction(t *testing.T) {
	dir, _, drivers := project(t, concurrentIndex())

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up returned %v, want the split migration applied", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(stdout, "1 applied") {
		t.Errorf("status after the split migration ran:\n%s", stdout)
		t.Log("The parts before the last each wrote a dirty row, and the last wrote applied, so")
		t.Log("the fold must read the migration as applied rather than as stopped partway.")
	}
}

func TestADryRunSaysAMigrationRunsInParts(t *testing.T) {
	dir, _, drivers := project(t, concurrentIndex())

	stdout, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}

	if !strings.Contains(stdout, "2 statements in 2 parts") {
		t.Errorf("the dry run does not say the migration runs in parts:\n%s", stdout)
	}
}
