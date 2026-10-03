package validate

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/rule"
)

// lintDetail is what the fake rules below report, which is how a test tells their findings apart.
const lintDetail = "a fake rule found this"

// watched records what the fake rules were handed, so a test can read the subject they judged.
type watched struct {
	// subjects are the migrations the rules were asked about, in the order they were asked.
	subjects []rule.Subject
}

// at returns the one subject these rules were asked about, failing a test that staged more.
func (w *watched) at(t *testing.T, i int) rule.Subject {
	t.Helper()

	if len(w.subjects) <= i {
		t.Fatalf("the rules were asked about %d migrations, wanted at least %d",
			len(w.subjects), i+1)
	}

	return w.subjects[i]
}

// rules returns a set holding one rule per severity given, each reporting one finding per up op.
func (w *watched) rules(t *testing.T, severities map[rule.ID]rule.Severity) *rule.Set {
	t.Helper()

	built := make([]rule.Rule, 0, len(severities))

	for _, id := range slices.Sorted(maps.Keys(severities)) {
		built = append(built, rule.Rule{
			ID:      id,
			Default: severities[id],
			Summary: "a fake rule",
			Judge:   w.judge,
		})
	}

	set, err := rule.NewSet(built, nil)
	if err != nil {
		t.Fatalf("rule.NewSet: %v", err)
	}

	return &set
}

// judge records the subject and reports one finding for the first operation of the up half.
func (w *watched) judge(s rule.Subject) []rule.Finding {
	w.subjects = append(w.subjects, s)

	for _, op := range s.Up.Numbered(ir.Up) {
		return []rule.Finding{op.Finding(lintDetail)}
	}

	return nil
}

// linted stages one reversible migration and validates it under the given rules.
func linted(t *testing.T, rules *rule.Set) Report {
	t.Helper()

	found := migrationAt(0, "add_users")
	src := source{found.Version: {up: ir.Operations{table(t, "users")}}}

	return runOnline(t, Options{Allow: engine.Allowance{All: true}, Rules: rules},
		src, compiler{}, found)
}

func TestARuleSetToErrorIsAProblemAndOneSetToWarnIsNot(t *testing.T) {
	var seen watched

	report := linted(t, seen.rules(t, map[rule.ID]rule.Severity{
		"hard-rule": rule.SeverityError,
		"soft-rule": rule.SeverityWarn,
	}))

	if len(report.Problems()) != 1 {
		t.Fatalf("found %d problems, want 1: %v", len(report.Problems()), report.Problems())
	}

	if len(report.Warnings()) != 1 {
		t.Fatalf("found %d warnings, want 1: %v", len(report.Warnings()), report.Warnings())
	}

	problem, warning := report.Problems()[0], report.Warnings()[0]

	if problem.Rule != "hard-rule" || warning.Rule != "soft-rule" {
		t.Errorf("the problem came from %q and the warning from %q, want hard-rule and soft-rule",
			problem.Rule, warning.Rule)
	}

	for _, found := range []Problem{problem, warning} {
		if found.Check != CheckLint {
			t.Errorf("%q was reported under check %q, want %q", found.Rule, found.Check, CheckLint)
		}

		if found.Detail != lintDetail || found.Operation != 1 ||
			found.Kind != ir.KindCreateTable {
			t.Errorf("%q reported %+v, want the fake finding about operation 1", found.Rule, found)
		}
	}
}

func TestAWarningIsNotAProblemHoweverTheReportIsAsked(t *testing.T) {
	var seen watched

	report := linted(t, seen.rules(t, map[rule.ID]rule.Severity{"soft-rule": rule.SeverityWarn}))

	if !report.Clean() {
		t.Error("a report holding only warnings is not clean, so migr validate would exit 2")
	}

	if report.Migrations() != 0 {
		t.Errorf("%d migrations are reported as faulted by a warning alone", report.Migrations())
	}
}

func TestARuleSetToOffReportsNeitherAndIsNotEvenAsked(t *testing.T) {
	var seen watched

	report := linted(t, seen.rules(t, map[rule.ID]rule.Severity{"quiet-rule": rule.SeverityOff}))

	if !report.Clean() || len(report.Warnings()) != 0 {
		t.Errorf("a rule set to off reported %d problems and %d warnings",
			len(report.Problems()), len(report.Warnings()))
	}

	if len(seen.subjects) != 0 {
		t.Errorf("a rule set to off was asked about %d migrations", len(seen.subjects))
	}
}

func TestARunGivenNoRulesPerformsNoLintPass(t *testing.T) {
	report := linted(t, nil)

	if !report.Clean() || len(report.Warnings()) != 0 {
		t.Error("a run given no rules reported something")
	}
}

func TestARuleSetNothingBuiltIsRefusedRatherThanRunningNothing(t *testing.T) {
	var unbuilt rule.Set

	found := migrationAt(0, "add_users")

	_, err := Run(t.Context(), Options{
		Migrations: []migration.Migration{found},
		Source:     source{found.Version: {up: ir.Operations{table(t, "users")}}},
		Planner:    plan.Planner{Compiler: compiler{}},
		Rules:      &unbuilt,
	})

	if !errors.Is(err, ErrUnbuiltRules) {
		t.Fatalf("Run = %v, want ErrUnbuiltRules", err)
	}
}

func TestTheRulesSeeBothHalvesAndWhatTheMigrationSendsItself(t *testing.T) {
	var seen watched

	found := migrationAt(0, "add_users")
	direct := []driver.Query{{SQL: "INSERT INTO audit VALUES (1)"}}

	src := source{found.Version: {
		up:     ir.Operations{table(t, "users")},
		down:   ir.Operations{ir.DropTable{Table: "users"}},
		direct: direct,
	}}

	runOnline(t, Options{Allow: engine.Allowance{All: true},
		Rules: seen.rules(t, map[rule.ID]rule.Severity{"soft-rule": rule.SeverityWarn})},
		src, compiler{}, found)

	subject := seen.at(t, 0)

	if subject.Migration.Version != found.Version {
		t.Errorf("the subject names migration %q, want %q", subject.Migration.Version,
			found.Version)
	}

	if len(subject.Up.Operations) != 1 || len(subject.Down.Operations) != 1 {
		t.Errorf("the subject holds %d up and %d down operations, want 1 and 1",
			len(subject.Up.Operations), len(subject.Down.Operations))
	}

	if len(subject.Up.Direct) != 1 || subject.Up.Direct[0].SQL != direct[0].SQL {
		t.Errorf("the subject holds %v as the migration's own statements, want %v",
			subject.Up.Direct, direct)
	}
}

func TestTheRulesAreToldWhereTheDownCameFrom(t *testing.T) {
	for _, c := range []struct {
		name   string
		staged staged
		want   rule.Undo
	}{
		{"authored", staged{up: ir.Operations{table(t, "users")},
			down: ir.Operations{ir.DropTable{Table: "users"}}}, rule.UndoAuthored},
		{"derived", staged{up: ir.Operations{table(t, "users")}}, rule.UndoDerived},
		{"declared irreversible", staged{up: ir.Operations{table(t, "users")},
			declared: "it shreds the audit log"}, rule.UndoDeclared},
		{"nothing can undo it", staged{up: ir.Operations{raw("ANALYZE")}}, rule.UndoNone},
		{"it sends its own statements and authors no down",
			staged{up: ir.Operations{table(t, "users")},
				direct: []driver.Query{{SQL: "INSERT INTO audit VALUES (1)"}}}, rule.UndoNone},
		{"its down cannot be read", staged{up: ir.Operations{table(t, "users")},
			downUnreadable: "its down section is not parseable"}, rule.UndoUnread},
	} {
		t.Run(c.name, func(t *testing.T) {
			var seen watched

			found := migrationAt(0, "add_users")

			runOnline(t, Options{Allow: engine.Allowance{All: true},
				Rules: seen.rules(t, map[rule.ID]rule.Severity{"soft-rule": rule.SeverityWarn})},
				source{found.Version: c.staged}, compiler{}, found)

			if got := seen.at(t, 0).Undo; got != c.want {
				t.Errorf("the rules were told the down was %q, want %q", got, c.want)
			}
		})
	}
}

func TestEveryListAReportHandsOutIsACopy(t *testing.T) {
	var seen watched

	report := linted(t, seen.rules(t, map[rule.ID]rule.Severity{
		"hard-rule": rule.SeverityError,
		"soft-rule": rule.SeverityWarn,
	}))

	problems, warnings := report.Problems(), report.Warnings()

	if len(problems) != 1 || len(warnings) != 1 {
		t.Fatalf("found %d problems and %d warnings, want 1 and 1", len(problems), len(warnings))
	}

	problems[0].Detail = "tampered"
	warnings[0].Detail = "tampered"

	if report.Problems()[0].Detail == "tampered" {
		t.Error("Problems() hands out the report's own slice")
	}

	if report.Warnings()[0].Detail == "tampered" {
		t.Error("Warnings() hands out the report's own slice")
	}
}

func TestAMigrationWhoseUpCannotBeReadIsNotLintedAtAll(t *testing.T) {
	var seen watched

	found := migrationAt(0, "add_users")
	src := source{found.Version: {unreadable: "it is not a migration"}}

	report := runOnline(t, Options{Allow: engine.Allowance{All: true},
		Rules: seen.rules(t, map[rule.ID]rule.Severity{"soft-rule": rule.SeverityWarn})},
		src, compiler{}, found)

	if len(seen.subjects) != 0 {
		t.Errorf("the rules judged a migration nothing could read, %d times", len(seen.subjects))
	}

	if len(report.Problems()) != 1 || report.Problems()[0].Check != CheckFormat {
		t.Errorf("the report holds %v, want one format problem", report.Problems())
	}
}

func TestLintRunsAfterEveryCheckThatJudgesTheSameMigration(t *testing.T) {
	var seen watched

	found := migrationAt(0, "drop_users")
	src := source{found.Version: {up: ir.Operations{raw("DROP TABLE users")}}}

	report := runOnline(t, Options{
		Rules: seen.rules(t, map[rule.ID]rule.Severity{"hard-rule": rule.SeverityError}),
	}, src, compiler{}, found)

	var order []Check
	for _, problem := range report.Problems() {
		order = append(order, problem.Check)
	}

	if len(order) == 0 || order[len(order)-1] != CheckLint {
		t.Errorf("the problems came in the order %v, and lint must be the last of them", order)
	}

	if !slices.Contains(order, CheckDestructive) {
		t.Errorf("the destructive check reported nothing about %v", report.Problems())
	}
}

func TestEveryCheckIsListedInTheOrderItRuns(t *testing.T) {
	want := []Check{CheckFormat, CheckDestructive, CheckCompile, CheckCapability, CheckReverse,
		CheckLint}

	if got := Checks(); !slices.Equal(got, want) {
		t.Errorf("Checks() = %v, want %v", got, want)
	}

	for _, check := range want {
		if !check.Valid() {
			t.Errorf("check %q is listed but not valid", check)
		}
	}
}

func TestANonRuleCheckNamesNoRule(t *testing.T) {
	found := migrationAt(0, "add_users")
	src := source{found.Version: {up: ir.Operations{raw("CREATE TABLE users (id bigint)")}}}

	report := runOnline(t, Options{}, src, compiler{}, found)

	for _, problem := range report.Problems() {
		if problem.Check != CheckLint && problem.Rule != "" {
			t.Errorf("the %s check named rule %q, and only a rule has one", problem.Check,
				problem.Rule)
		}
	}

	if !strings.Contains(report.Problems()[0].Detail, "") {
		t.Error("the staged project reported nothing at all")
	}
}
