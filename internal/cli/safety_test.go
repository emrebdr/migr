package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/validate"
)

// indexedUsers is the second operation of the first staged migration, which the costly cases declare a rewrite.
const indexedUsers = "CREATE INDEX idx_users ON users (id);"

// createdTeams is the only operation of the second staged migration.
const createdTeams = "CREATE TABLE teams (id bigint);"

// withSafety appends a safety section to a project's migr.yaml.
func withSafety(t *testing.T, dir, safety string) {
	t.Helper()

	path := filepath.Join(dir, config.DefaultFilename)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading migr.yaml: %v", err)
	}

	if err := os.WriteFile(path, append(body, []byte(safety)...), 0o600); err != nil {
		t.Fatalf("writing migr.yaml: %v", err)
	}
}

// costly stages the two-table project with one statement the fake server declares rewrites its table.
func costly(t *testing.T, sql string) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	f.costly = sql

	return dir, f, drivers
}

// stagedCostly is the project a gate on the default ceiling fails, for the exit-code matrix.
func stagedCostly(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return costly(t, indexedUsers)
}

// operationAt returns one operation of a decoded plan, failing where the plan has no such operation.
func operationAt(t *testing.T, p compiledPlan, m, op int) operation {
	t.Helper()

	if m >= len(p.Migrations) || op >= len(p.Migrations[m].Operations) {
		t.Fatalf("the plan has no operation %d of migration %d: %+v", op+1, m+1, p.Migrations)
	}

	return p.Migrations[m].Operations[op]
}

func TestPlanPrintsTheLockAxisOnlyWhenAskedYetTheDocumentAlwaysCarriesIt(t *testing.T) {
	dir, _, drivers := costly(t, indexedUsers)

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, impact := range capability.Impacts() {
		if strings.Contains(stdout, impact.String()) {
			t.Errorf("plan printed %q without --safety:\n%s", impact, stdout)
		}
	}

	stdout, _, err = runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	p := decodePlan(t, stdout)

	if got := operationAt(t, p, 0, 1); got.Lock != capability.TableRewrite || !got.RewritesTable ||
		!got.OverCeiling {
		t.Errorf("the document carries %+v for the costly operation", got)
	}

	if p.Ceiling != capability.BriefExclusive {
		t.Errorf("the document judges against %q, want the default brief_exclusive", p.Ceiling)
	}

	t.Log("--safety gates the rendering and nothing else: a document carries every field it has,")
	t.Log("so a CI job reading --json never has to know which flag makes a field appear.")
}

func TestPlanSafetyAnnotatesEachOperationWithItsLockAndMarksWhatExceedsTheCeiling(t *testing.T) {
	dir, _, drivers := costly(t, indexedUsers)

	stdout, _, err := runWith(t, dir, noon, drivers, "plan", "--safety")
	if err != nil {
		t.Fatalf("plan --safety exited %d without --require-safe: %v", ExitCode(err), err)
	}

	for _, want := range []string{
		"raw  native  lock_free",
		"raw  native  table_rewrite (read from the table) -- rewrites the table",
		"\u25b2 over safety.max_lock_impact (brief_exclusive)",
		"unknown, which a raw statement is, counts as table_rewrite",
		"marked \u25b2 above: 1 operation exceeds safety.max_lock_impact (brief_exclusive)",
		"--require-safe exits 5",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan --safety does not say %q:\n%s", want, stdout)
		}
	}
}

func TestPlanSafetySaysNothingExceedsWhenNothingDoes(t *testing.T) {
	dir, _, drivers := project(t, twoTables()...)

	stdout, _, err := runWith(t, dir, noon, drivers, "plan", "--safety")
	if err != nil {
		t.Fatalf("plan --safety: %v", err)
	}

	if !strings.Contains(stdout, "no operation exceeds safety.max_lock_impact (brief_exclusive)") ||
		strings.Contains(stdout, "\u25b2") {
		t.Errorf("a plan with nothing over the ceiling says otherwise:\n%s", stdout)
	}
}

func TestRequireSafeExitsFiveNamingEveryOperationOverTheCeiling(t *testing.T) {
	dir, _, drivers := costly(t, indexedUsers)

	stdout, _, err := runWith(t, dir, noon, drivers, "plan", "--require-safe")

	if !errors.Is(err, engine.ErrLockImpact) || ExitCode(err) != ExitLockImpact {
		t.Fatalf("plan --require-safe returned %v (exit %d), want exit 5", err, ExitCode(err))
	}

	for _, want := range []string{fileAt(0, "add_users"), "operation 2 (raw)", "table_rewrite",
		"safety.max_lock_impact (brief_exclusive)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q: %v", want, err)
		}
	}

	if !strings.Contains(stdout, "\u25b2 over safety.max_lock_impact") {
		t.Errorf("--require-safe did not print the lock axis it gated on:\n%s", stdout)
	}

	t.Log("A CI job blocks the merge on exit 5 and reads, in the plan above the failure, which")
	t.Log("operation would take production down and what it costs.")
}

func TestRequireSafeFailsAsAJSONDocumentNamingTheOperations(t *testing.T) {
	dir, _, drivers := costly(t, indexedUsers)

	_, stderr, err := runWith(t, dir, noon, drivers, "--json", "plan", "--require-safe")
	if ExitCode(err) != ExitLockImpact {
		t.Fatalf("plan --json --require-safe exited %d: %v", ExitCode(err), err)
	}

	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 1 {
		t.Fatalf("--json wrote %d lines to stderr, want one object:\n%s", len(lines), stderr)
	}

	got := decodeFailure(t, lines[0])

	if got.Code != ExitLockImpact || got.Lock == nil || got.Lock.Ceiling != capability.BriefExclusive ||
		len(got.Lock.Operations) != 1 || got.Lock.Operations[0].Index != 2 ||
		got.Lock.Operations[0].Lock != capability.TableRewrite {
		t.Errorf("the failure document is %+v (lock %+v)", got, got.Lock)
	}

	if got.Migration != fileAt(0, "add_users") || got.Operation != "raw" {
		t.Errorf("the failure names %q and %q, want the migration and the operation", got.Migration,
			got.Operation)
	}
}

func TestRequireSafePassesUnderACeilingTheOperationsFit(t *testing.T) {
	dir, _, drivers := costly(t, indexedUsers)
	withSafety(t, dir, "safety:\n  max_lock_impact: table_rewrite\n")

	if _, _, err := runWith(t, dir, noon, drivers, "plan", "--require-safe"); err != nil {
		t.Errorf("plan --require-safe under a table_rewrite ceiling returned %v", err)
	}
}

func TestOnlyTheFirstQueuedMigrationIsJudgedAgainstItsTables(t *testing.T) {
	for name, c := range map[string]struct {
		sql      string
		at       int
		want     capability.LockImpact
		assessed int
	}{
		"first":  {sql: indexedUsers, at: 0, want: capability.BriefExclusive, assessed: 1},
		"second": {sql: createdTeams, at: 1, want: capability.TableRewrite, assessed: 0},
	} {
		t.Run(name, func(t *testing.T) {
			dir, f, drivers := costly(t, c.sql)
			f.assesses = true

			stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
			if err != nil {
				t.Fatalf("plan: %v", err)
			}

			last := len(decodePlan(t, stdout).Migrations[c.at].Operations) - 1
			if got := operationAt(t, decodePlan(t, stdout), c.at, last); got.Lock != c.want ||
				got.Assessed != (c.assessed > 0) {
				t.Errorf("the costly operation reads %s (assessed %v), want %s", got.Lock, got.Assessed, c.want)
			}

			if f.assessed != c.assessed {
				t.Errorf("the session was asked about %d tables, want %d", f.assessed, c.assessed)
			}
		})
	}

	t.Log("A preview applies nothing, so only the first queued migration meets its tables as they")
	t.Log("will be. Every later verdict stays at its declared worst, the ~ marker's rule.")
}

func TestAnAssessedOperationReadsAsSuchAndPassesTheGate(t *testing.T) {
	dir, f, drivers := costly(t, indexedUsers)
	f.assesses = true

	stdout, _, err := runWith(t, dir, noon, drivers, "plan", "--require-safe")
	if err != nil {
		t.Fatalf("plan --require-safe over a table the session read as small: %v", err)
	}

	if !strings.Contains(stdout, "brief_exclusive (read from the table)") {
		t.Errorf("the plan does not say the verdict was read from the table:\n%s", stdout)
	}
}

func TestATableThatCannotBeReadKeepsTheWorstAndSaysSo(t *testing.T) {
	dir, f, drivers := costly(t, indexedUsers)
	f.assesses, f.assessErr = true, errors.New("permission denied for the catalog")

	_, stderr, err := runWith(t, dir, noon, drivers, "plan", "--require-safe")
	if ExitCode(err) != ExitLockImpact {
		t.Fatalf("plan --require-safe exited %d over an unread table, want 5: %v", ExitCode(err), err)
	}

	if !strings.Contains(stderr, "stays at its worst") || !strings.Contains(stderr, "permission denied") {
		t.Errorf("nothing said why the verdict stayed at its worst:\n%s", stderr)
	}
}

func TestARewrittenOperationIsJudgedAtTheCostItRunsAt(t *testing.T) {
	dir, _, drivers := rewritten(t, "")

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan", "--require-safe")
	if err != nil {
		t.Fatalf("plan --require-safe over a rewritten operation: %v", err)
	}

	if got := operationAt(t, decodePlan(t, stdout), 0, 0); got.Lock != capability.BriefExclusive ||
		got.Declared != capability.LongExclusive {
		t.Errorf("the rewritten operation reads %s, declared %s", got.Lock, got.Declared)
	}

	stdout, _, err = runWith(t, dir, noon, drivers, "plan", "--safety")
	if err != nil {
		t.Fatalf("plan --safety: %v", err)
	}

	if !strings.Contains(stdout, "brief_exclusive (declared long_exclusive, rewritten)") {
		t.Errorf("the plan does not say what the rewrite saved:\n%s", stdout)
	}

	t.Log("The gate reads the cost the operation runs at: a check added NOT VALID and validated bare")
	t.Log("is brief, where the same check declared plainly scans the table under its lock.")
}

func TestARewriteSwitchedOffIsJudgedAsDeclared(t *testing.T) {
	dir, _, drivers := rewritten(t, "safety:\n  rewrites:\n    not_valid: off\n")

	_, _, err := runWith(t, dir, noon, drivers, "plan", "--require-safe")
	if ExitCode(err) != ExitLockImpact || !strings.Contains(err.Error(), "long_exclusive") {
		t.Errorf("plan --require-safe with the rewrite off returned %v (exit %d)", err, ExitCode(err))
	}
}

func TestValidateRequireSafeConnectsAndExitsFiveWhenTheCeilingIsAllThatFails(t *testing.T) {
	dir, _, drivers := costly(t, indexedUsers)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "validate", "--require-safe")
	if ExitCode(err) != ExitLockImpact {
		t.Fatalf("validate --require-safe exited %d, want 5: %v", ExitCode(err), err)
	}

	result := decodeValidation(t, stdout)

	if !result.Connected {
		t.Error("--require-safe did not connect, and the lock axis is only the server's to answer")
	}

	if len(result.Problems) != 1 || result.Problems[0].Check != validate.CheckLock ||
		!strings.Contains(result.Problems[0].Detail, "table_rewrite") {
		t.Errorf("validate reported %+v, want the one costly operation", result.Problems)
	}
}

func TestValidateRequireSafeExitsTwoWhereSomethingElseIsWrongToo(t *testing.T) {
	dir, f, drivers := refusing(t)
	f.costly = indexedUsers

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "validate", "--require-safe")
	if ExitCode(err) != ExitValidation {
		t.Fatalf("validate --require-safe exited %d over a broken migration, want 2: %v",
			ExitCode(err), err)
	}

	if overCeiling(decodeValidation(t, stdout).Problems) != 1 {
		t.Errorf("the lock problem went missing beside the others:\n%s", stdout)
	}

	t.Log("A migration that will not do what it says is the argument being wrong, which is fixed")
	t.Log("first; exit 5 is reserved for a project whose only problem is what it would cost.")
}

func TestValidateRequireSafeJudgesTheRewriteAndNotTheDown(t *testing.T) {
	dir, _, drivers := rewritten(t, "")

	if _, _, err := runWith(t, dir, noon, drivers, "validate", "--require-safe"); err != nil {
		t.Errorf("validate --require-safe over a rewritten operation returned %v", err)
	}

	dir, f, drivers := project(t, stagedMigration{name: "add_users", up: createdTeams,
		down: "DROP TABLE teams;"})
	f.costly = "DROP TABLE teams;"

	if _, _, err := runWith(t, dir, noon, drivers, "validate", "--require-safe"); err != nil {
		t.Errorf("validate --require-safe judged a down: %v", err)
	}

	t.Log("The inverse of every create_table is a drop, so judging a down would flag every project")
	t.Log("that ever made a table -- invariant #7's reason, which the destructive check also follows.")
}

func TestRequireSafeIsRefusedWhereNothingReadsIt(t *testing.T) {
	for _, command := range []string{"up", "down", "status", "history"} {
		dir, _, drivers := project(t, twoTables()...)

		_, _, err := runWith(t, dir, noon, drivers, command, "--require-safe")
		if ExitCode(err) != ExitValidation {
			t.Errorf("migr %s --require-safe exited %d, want the flag refused", command, ExitCode(err))
		}
	}
}
