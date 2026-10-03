package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/rule"
)

// linting stages a Go project whose one migration creates a keyless table with a text column.
func linting(t *testing.T) (string, *driver.Registry) {
	t.Helper()

	dir, _, drivers := goProject(t, "add_scripted")

	return dir, drivers
}

// configuring adds rule severities to a staged project's own config, whatever else that config says.
func configuring(t *testing.T, dir string, severities map[rule.ID]rule.Severity) {
	t.Helper()

	lines := make([]string, 0, len(severities))
	for _, id := range rule.IDs() {
		if severity, said := severities[id]; said {
			lines = append(lines, "    "+string(id)+": "+string(severity))
		}
	}

	path := filepath.Join(dir, config.DefaultFilename)

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the staged config: %v", err)
	}

	block := "\n  rules:\n" + strings.Join(lines, "\n") + "\n"

	write(t, path, strings.Replace(string(current), allowingRaw, allowingRaw+block, 1))
}

func TestARuleSetToErrorFailsValidationAndOneSetToWarnDoesNot(t *testing.T) {
	dir, drivers := linting(t)

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "validate")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr validate exited %d, want %d: %v", got, ExitValidation, err)
	}

	for _, want := range []string{
		string(rule.MissingPrimaryKey),
		`table "scripted" is created with no primary key`,
		string(rule.UnboundedString),
		"warnings — 1 warning",
		`column "note" is text`,
		"1 problem in 1 migration, 1 warning",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
}

func TestTheRemedyNamesTheRuleAndWhatToSetIt(t *testing.T) {
	dir, drivers := linting(t)

	stdout, _, _ := runScripted(t, dir, actTypes, drivers, "validate")

	for _, want := range []string{
		"A lint rule is worth what this project says it is",
		"error, warn, off",
		"  validation:\n    rules:\n",
		string(rule.MissingPrimaryKey) + ": warn",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the remedy does not say %q:\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, string(rule.UnboundedString)+": warn") {
		t.Errorf("the remedy names a rule that only warned, and a remedy is for what failed:\n%s",
			stdout)
	}
}

func TestEveryRuleTurnedDownLeavesValidationPassingWithWarnings(t *testing.T) {
	dir, drivers := linting(t)
	configuring(t, dir, map[rule.ID]rule.Severity{rule.MissingPrimaryKey: rule.SeverityWarn})

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "validate")
	if err != nil {
		t.Fatalf("migr validate failed over warnings alone: %v", err)
	}

	for _, want := range []string{"no problems found, 2 warnings", "warnings — 2 warnings",
		string(rule.MissingPrimaryKey), string(rule.UnboundedString)} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, "A lint rule is worth") {
		t.Errorf("a run that failed nothing printed a remedy, which is advice to silence "+
			"advice:\n%s", stdout)
	}
}

func TestEveryRuleTurnedOffLeavesNothingToReport(t *testing.T) {
	dir, drivers := linting(t)
	configuring(t, dir, map[rule.ID]rule.Severity{
		rule.MissingPrimaryKey: rule.SeverityOff,
		rule.UnboundedString:   rule.SeverityOff,
	})

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "validate")
	if err != nil {
		t.Fatalf("migr validate failed with every rule off: %v", err)
	}

	for _, unwanted := range []string{string(rule.MissingPrimaryKey), string(rule.UnboundedString),
		"warning"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("the report says %q with every rule off:\n%s", unwanted, stdout)
		}
	}

	if !strings.Contains(stdout, "no problems found.") {
		t.Errorf("the report does not pass cleanly:\n%s", stdout)
	}
}

func TestTheJSONDocumentSeparatesWarningsFromProblemsAndNamesTheRule(t *testing.T) {
	dir, drivers := linting(t)

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "validate", "--json")
	if ExitCode(err) != ExitValidation {
		t.Fatalf("migr validate --json exited %d, want %d: %v", ExitCode(err), ExitValidation, err)
	}

	var document struct {
		Problems []struct {
			Check string `json:"check"`
			Rule  string `json:"rule"`
			Kind  string `json:"kind"`
		} `json:"problems"`
		Warnings []struct {
			Check string `json:"check"`
			Rule  string `json:"rule"`
		} `json:"warnings"`
	}

	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}

	if len(document.Problems) != 1 || len(document.Warnings) != 1 {
		t.Fatalf("the document holds %d problems and %d warnings, want 1 and 1",
			len(document.Problems), len(document.Warnings))
	}

	problem, warning := document.Problems[0], document.Warnings[0]

	if problem.Check != "lint" || problem.Rule != string(rule.MissingPrimaryKey) {
		t.Errorf("the problem is %+v, want check lint and rule %s", problem,
			rule.MissingPrimaryKey)
	}

	if problem.Kind != string(ir.KindCreateTable) {
		t.Errorf("the problem names operation %q, want %q", problem.Kind, ir.KindCreateTable)
	}

	if warning.Check != "lint" || warning.Rule != string(rule.UnboundedString) {
		t.Errorf("the warning is %+v, want check lint and rule %s", warning, rule.UnboundedString)
	}
}

func TestANonRuleProblemCarriesNoRuleInTheDocument(t *testing.T) {
	dir, _, drivers := refusing(t)

	stdout, _, _ := runWith(t, dir, noon, drivers, "validate", "--json")

	var document struct {
		Problems []struct {
			Check string `json:"check"`
			Rule  string `json:"rule"`
		} `json:"problems"`
	}

	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}

	if len(document.Problems) == 0 {
		t.Fatal("the staged project reported no problems at all")
	}

	for _, problem := range document.Problems {
		if problem.Rule != "" {
			t.Errorf("the %s check named rule %q, and only a lint problem has one", problem.Check,
				problem.Rule)
		}
	}
}

func TestACleanRunStillRendersProblemsAsAList(t *testing.T) {
	dir, drivers := linting(t)
	configuring(t, dir, map[rule.ID]rule.Severity{
		rule.MissingPrimaryKey: rule.SeverityOff,
		rule.UnboundedString:   rule.SeverityOff,
	})

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "validate", "--json")
	if err != nil {
		t.Fatalf("migr validate --json failed: %v", err)
	}

	if !strings.Contains(stdout, `"problems": []`) {
		t.Errorf("a clean run renders problems as something other than an empty list:\n%s", stdout)
	}

	if strings.Contains(stdout, `"warnings"`) {
		t.Errorf("a run with no warnings carries the key anyway:\n%s", stdout)
	}
}

func TestAWarningIsPrintedBeneathTheProblemsItDoesNotJoin(t *testing.T) {
	dir, drivers := linting(t)

	stdout, _, _ := runScripted(t, dir, actTypes, drivers, "validate")

	problem := strings.Index(stdout, string(rule.MissingPrimaryKey))
	warnings := strings.Index(stdout, "warnings —")

	if problem < 0 || warnings < 0 {
		t.Fatalf("the report holds neither half:\n%s", stdout)
	}

	if warnings < problem {
		t.Errorf("the warnings are printed above the problems:\n%s", stdout)
	}
}

func TestARuleThisBuildDoesNotShipFailsBeforeAnythingRuns(t *testing.T) {
	dir, drivers := linting(t)

	write(t, filepath.Join(dir, config.DefaultFilename),
		strings.Replace(projectConfig, allowingRaw,
			allowingRaw+"\n  rules:\n    no-such-rule: off\n", 1))

	_, _, err := runScripted(t, dir, actTypes, drivers, "validate")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr validate exited %d, want %d: %v", got, ExitValidation, err)
	}

	if !strings.Contains(err.Error(), "no rule is called") {
		t.Errorf("the failure does not refuse the rule by name: %v", err)
	}
}

func TestNoSeverityCanPermitDestruction(t *testing.T) {
	dir, f, drivers := refusing(t)

	write(t, filepath.Join(dir, config.DefaultFilename),
		strings.Replace(projectConfig, allowingRaw, "allow_destructive: []\n  rules:\n"+
			"    "+string(rule.MissingPrimaryKey)+": off\n"+
			"    "+string(rule.UnboundedString)+": off\n", 1))

	stdout, _, err := runWith(t, dir, noon, drivers, "validate")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr validate exited %d with every rule off, want %d: %v", got, ExitValidation,
			err)
	}

	if !strings.Contains(stdout, string(ir.KindRaw)) ||
		!strings.Contains(stdout, "destructive") {
		t.Errorf("the destructive check reported nothing with every rule off:\n%s", stdout)
	}

	_, _, err = runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitValidation {
		t.Fatalf("migr up exited %d with every rule off, want %d: %v", got, ExitValidation, err)
	}

	if len(f.statements) != 0 {
		t.Errorf("a refused migration ran %d statements anyway", len(f.statements))
	}
}

func TestLintChangesNothingAboutARunOrAPreview(t *testing.T) {
	dir, drivers := linting(t)

	for _, args := range [][]string{{"up"}, {"plan"}, {"up", "--dry-run"}, {"down"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, err := runScripted(t, dir, actTypes, drivers, args...)
			if err != nil {
				t.Fatalf("migr %v failed: %v", args, err)
			}

			for _, unwanted := range []string{string(rule.MissingPrimaryKey),
				string(rule.UnboundedString), "warning"} {
				if strings.Contains(stdout, unwanted) {
					t.Errorf("migr %v says %q, and lint is migr validate's alone:\n%s", args,
						unwanted, stdout)
				}
			}
		})
	}
}

func TestMigrConfigListsEveryRuleAndWhatItIsSetTo(t *testing.T) {
	dir, drivers := linting(t)
	configuring(t, dir, map[rule.ID]rule.Severity{rule.UnboundedString: rule.SeverityOff})

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "config")
	if err != nil {
		t.Fatalf("migr config failed: %v", err)
	}

	for _, want := range []string{
		string(rule.MissingPrimaryKey) + ": error",
		string(rule.UnboundedString) + ": \"off\"",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("migr config does not say %q:\n%s", want, stdout)
		}
	}
}

func TestTheRulesFlagIsRegisteredNowhere(t *testing.T) {
	cmd := newRootCmd()

	cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if strings.Contains(f.Name, "rule") || strings.Contains(f.Name, "lint") {
			t.Errorf("--%s is a persistent flag, and rules are configured in migr.yaml alone",
				f.Name)
		}
	})

	for _, sub := range cmd.Commands() {
		sub.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Name, "rule") || strings.Contains(f.Name, "lint") {
				t.Errorf("migr %s accepts --%s, and rules are configured in migr.yaml alone",
					sub.Name(), f.Name)
			}
		})
	}
}
