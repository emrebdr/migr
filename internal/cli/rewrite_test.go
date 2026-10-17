package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
)

// checkedTable is the statement the fake session rewrites, standing in for an add_check.
const checkedTable = "ALTER TABLE users ADD CONSTRAINT positive CHECK (id > 0);"

// rewritten stages a project whose one migration the fake session performs as a safer sequence.
func rewritten(t *testing.T, safety string) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, stagedMigration{name: "check_users", up: checkedTable,
		down: "ALTER TABLE users DROP CONSTRAINT positive;"})
	f.rewrites = checkedTable

	if safety != "" {
		path := filepath.Join(dir, config.DefaultFilename)

		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading migr.yaml: %v", err)
		}

		if err := os.WriteFile(path, append(body, []byte(safety)...), 0o600); err != nil {
			t.Fatalf("writing migr.yaml: %v", err)
		}
	}

	return dir, f, drivers
}

func TestPlanShowsThatOneDeclaredOperationBecameSeveralStatements(t *testing.T) {
	dir, _, drivers := rewritten(t, "")

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, want := range []string{
		"\u21b3 rewritten (safety.rewrites.not_valid): 1 operation, 2 statements instead of 1",
		"the rows are checked without blocking writes",
		"-- validated",
		"[no transaction]",
		"marked \u21b3 above: 1 operation",
		"safety.rewrites.not_valid: off",
		"safety.auto_rewrite: false",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not say %q:\n%s", want, stdout)
		}
	}

	t.Log("Never silent: an operator reads that one declared operation became two statements,")
	t.Log("why, and the one key that runs it as declared instead (ADR 0085).")
}

func TestPlanJSONCarriesTheRewriteBesideTheStatements(t *testing.T) {
	dir, _, drivers := rewritten(t, "")

	stdout, _, err := runWith(t, dir, noon, drivers, "plan", "--json")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	var doc struct {
		Migrations []struct {
			Operations []struct {
				Statements []string `json:"statements"`
				Rewrite    *struct {
					Name  string `json:"name"`
					Plain int    `json:"declared_statements"`
				} `json:"rewrite"`
			} `json:"operations"`
		} `json:"migrations"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decoding: %v\n%s", err, stdout)
	}

	op := doc.Migrations[0].Operations[0]
	if op.Rewrite == nil || op.Rewrite.Name != "not_valid" || op.Rewrite.Plain != 1 || len(op.Statements) != 2 {
		t.Errorf("the plan document reads %+v, want the not_valid rewrite of 1 statement into 2", op)
	}
}

func TestUpAndItsDryRunCountTheOperationsRewritten(t *testing.T) {
	dir, f, drivers := rewritten(t, "")

	dry, _, err := runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil || !strings.Contains(dry, "1 operation rewritten") {
		t.Errorf("up --dry-run returned %v and printed:\n%s", err, dry)
	}

	ran, _, err := runWith(t, dir, noon, drivers, "up")
	if err != nil || !strings.Contains(ran, "1 operation rewritten") {
		t.Errorf("up returned %v and printed:\n%s", err, ran)
	}

	if !slices.Contains(f.statements, "-- validated") {
		t.Error("the run never sent the rewritten sequence's second statement")
	}
}

func TestARewriteSwitchedOffRunsTheOperationAsDeclared(t *testing.T) {
	for name, safety := range map[string]string{
		"one off": "safety:\n  rewrites:\n    not_valid: off\n",
		"all off": "safety:\n  auto_rewrite: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir, _, drivers := rewritten(t, safety)

			stdout, _, err := runWith(t, dir, noon, drivers, "plan")
			if err != nil {
				t.Fatalf("plan: %v", err)
			}

			if strings.Contains(stdout, "rewritten") || strings.Contains(stdout, "-- validated") {
				t.Errorf("a rewrite switched off still shows in the plan:\n%s", stdout)
			}
		})
	}
}
