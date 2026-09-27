package cli

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/typescript"
)

// decodeRun reads the document --json promised of a run, failing the test rather than the command.
func decodeRun(t *testing.T, stdout string) runSummary {
	t.Helper()

	var summary runSummary
	if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
		t.Fatalf("migr up --json emitted %v:\n%s", err, stdout)
	}

	return summary
}

// versionsRan lists the migrations a run performed, in the order it performed them.
func versionsRan(summary runSummary) []string {
	ran := make([]string, 0, len(summary.Runs))
	for _, run := range summary.Runs {
		ran = append(ran, run.Version)
	}

	return ran
}

// versionsPlanned lists the migrations a plan would perform, in the order it would perform them.
func versionsPlanned(plan compiledPlan) []string {
	planned := make([]string, 0, len(plan.Migrations))
	for _, m := range plan.Migrations {
		planned = append(planned, m.Version)
	}

	return planned
}

// declaredOrder is the sequence two migrations run in when the earlier one declares the later one.
func declaredOrder() []string {
	return []string{versionAt(1), versionAt(0)}
}

func TestADeclaredDependencyRunsBeforeTheMigrationThatDeclaresIt(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users", "add_teams")

	stdout, _, err := runDeclaring(t, dir, actEmits, declaresNext, drivers, "--json", "up")
	if err != nil {
		t.Fatalf("up: %v", err)
	}

	summary := decodeRun(t, stdout)

	if got := versionsRan(summary); !slices.Equal(got, declaredOrder()) {
		t.Errorf("up ran %v, want %v", got, declaredOrder())
		t.Log("The earlier-timestamped migration declares the later one, which ADR 0018 makes the")
		t.Log("merged-branch case rather than a mistake. migration.Order has overlaid that since")
		t.Log("M4-T2 and nothing passed it a declaration until M7-T11.")
	}

	if len(summary.Runs) > 0 && summary.Runs[0].Ordinal != 1 {
		t.Errorf("the first run is ordinal %d, want 1", summary.Runs[0].Ordinal)
	}
}

func TestADeclarationMayNameAMigrationAnotherRuntimeLoaded(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users")
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_teams"+typescript.Ext),
		"export const up = 0;\n")

	stdout, _, err := runDeclaring(t, dir, actEmits, declaresNext, drivers, "--json", "up")
	if err != nil {
		t.Fatalf("up over a project needing two runtimes: %v", err)
	}

	if got := versionsRan(decodeRun(t, stdout)); !slices.Equal(got, declaredOrder()) {
		t.Errorf("up ran %v, want %v", got, declaredOrder())
		t.Log("A declaration names a version, never a file this runtime owns, so the .go migration")
		t.Log("declares a .ts one its own runtime never loaded. Each session reports its own half")
		t.Log("and the run resolves the graph over both (ADR 0048, ADR 0049).")
	}
}

func TestPlanPrintsTheOrderTheRunWouldUse(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users", "add_teams")

	stdout, _, err := runDeclaring(t, dir, actEmits, declaresNext, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if got := versionsPlanned(decodePlan(t, stdout)); !slices.Equal(got, declaredOrder()) {
		t.Errorf("plan printed %v, want %v", got, declaredOrder())
		t.Log("A preview starts the same runtimes a run does, so it resolves the same overlay --")
		t.Log("which is what makes it the answer to `what order exactly` that a report cannot give.")
	}
}

func TestValidateReadsMigrationsInTheDeclaredOrderToo(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users", "add_teams")

	stdout, _, err := runDeclaring(t, dir, actReads, declaresNext, drivers, "--json", "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	report := decodeValidation(t, stdout)

	var files []string

	for _, half := range report.Unchecked {
		if !slices.Contains(files, half.File) {
			files = append(files, half.File)
		}
	}

	want := []string{versionAt(1) + "_add_teams" + golang.Ext, versionAt(0) + "_add_users" + golang.Ext}
	if !slices.Equal(files, want) {
		t.Errorf("validate reported %v, want %v", files, want)
		t.Log("validate.Options.Migrations is documented as the order a run would perform them, and")
		t.Log("a CI gate reporting a different sequence from the run it gates is the thing")
		t.Log("invariant #8 exists to prevent.")
	}
}

func TestACycleRefusesEveryCommandThatSettlesAnOrder(t *testing.T) {
	for _, command := range []string{"up", "down", "plan", "validate"} {
		t.Run(command, func(t *testing.T) {
			dir, f, drivers := goProject(t, "add_users", "add_teams")

			_, _, err := runDeclaring(t, dir, actEmits, declaresCycle, drivers, command)
			if err == nil {
				t.Fatalf("%s ran a project whose declarations cannot be ordered", command)
			}

			if got := ExitCode(err); got != ExitValidation {
				t.Errorf("%s exited %d, want %d: a cycle is fixed by editing a migration",
					command, got, ExitValidation)
			}

			for _, want := range []string{"_add_users" + golang.Ext, "_add_teams" + golang.Ext} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}

			if len(f.statements) != 0 {
				t.Errorf("%s ran %v before refusing", command, f.statements)
			}
		})
	}
}

func TestADeclarationNamingNoMigrationIsRefused(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users")

	_, _, err := runDeclaring(t, dir, actEmits, declaresUnknown, drivers, "up")
	if err == nil {
		t.Fatal("up ran a project declaring a dependency on a migration nothing carries")
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("up exited %d, want %d", got, ExitValidation)
	}

	for _, want := range []string{strayVersion, "_add_users" + golang.Ext} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	t.Log("An edge naming nobody is dropped before the sort, so the order still resolves -- and")
	t.Log("reporting it is what keeps a typo in a version from silently ordering nothing (ADR 0018).")
}

func TestStatusSaysItsOrderIsVersionOrderWhenARuntimeOwnsTheDeclarations(t *testing.T) {
	dir, _, drivers := goProject(t, "add_users", "add_teams")

	if _, _, err := runDeclaring(t, dir, actEmits, declaresNext, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	report := decodeStatus(t, stdout)

	if report.Order != orderVersion || report.Unread != 2 {
		t.Errorf("status reported order %q over %d unread declarations, want %q over 2",
			report.Order, report.Unread, orderVersion)
	}

	if got := versionsOfStatus(report); !slices.Equal(got, []string{versionAt(0), versionAt(1)}) {
		t.Errorf("status reported %v, want version order", got)
	}

	text, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(text, "version order") {
		t.Errorf("status claims an order it cannot know:\n%s", text)
		t.Log("The run applied these in the opposite order, and no runtime was started here to say")
		t.Log("so -- which is why the report names its basis rather than claiming the run's")
		t.Log("(ADR 0027, ADR 0049). This ran over the adapter that refuses to start anything, so")
		t.Log("a status that had asked would have failed rather than merely been slower.")
	}
}

func TestStatusSaysItsOrderIsTheRunOrderWhenNothingCanReorderIt(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	if report := decodeStatus(t, stdout); report.Order != orderResolved || report.Unread != 0 {
		t.Errorf("status reported order %q over %d unread declarations, want %q over none",
			report.Order, report.Unread, orderResolved)
	}

	text, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if strings.Contains(text, "version order") {
		t.Errorf("status qualified an order nothing could have moved:\n%s", text)
		t.Log("A .sql migration declares nothing -- that format has no syntax for it -- so this")
		t.Log("report's sequence is the run's, and every M5-era project's output is unchanged.")
	}
}

// versionsOfStatus lists the versions a report resolved, in the order it printed them.
func versionsOfStatus(report statusReport) []string {
	found := make([]string, 0, len(report.Statuses))
	for _, row := range report.Statuses {
		found = append(found, row.Version)
	}

	return found
}
