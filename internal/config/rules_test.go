package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/emrebdr/migr/internal/rule"
)

// ruleProject is a minimal config whose validation block one case at a time replaces.
const ruleProject = `
version: 1
migrations:
  dir: ./migrations
  language: sql
databases:
  default:
    driver: postgres
    dsn: postgres://localhost/db
validation:
  allow_destructive: [raw]
`

// loadRules parses a config whose validation block is the given YAML, and reports what it resolved.
func loadRules(t *testing.T, block string) (*Config, error) {
	t.Helper()

	return parse([]byte(ruleProject+block), "migr.yaml",
		func(string) (string, bool) { return "", false })
}

// renderYAML marshals a resolved config the way migr config prints it.
func renderYAML(t *testing.T, cfg *Config) string {
	t.Helper()

	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	return string(out)
}

func TestEveryShippedRuleResolvesToItsDefault(t *testing.T) {
	cfg, err := loadRules(t, "")
	if err != nil {
		t.Fatalf("loading a config that says nothing about rules: %v", err)
	}

	if len(cfg.Validation.Rules) != len(rule.IDs()) {
		t.Fatalf("resolved %d rules and this build ships %d", len(cfg.Validation.Rules),
			len(rule.IDs()))
	}

	for id, want := range rule.Defaults() {
		if got := cfg.Validation.Rules[id]; got != want {
			t.Errorf("rule %q resolved to %q, want its default %q", id, got, want)
		}
	}
}

func TestWhatAProjectSaysAboutARuleWinsOverItsDefault(t *testing.T) {
	cfg, err := loadRules(t, "  rules:\n    "+string(rule.MissingPrimaryKey)+": off\n")
	if err != nil {
		t.Fatalf("loading a config that configures one rule: %v", err)
	}

	if got := cfg.Validation.Rules[rule.MissingPrimaryKey]; got != rule.SeverityOff {
		t.Errorf("the configured rule resolved to %q, want %q", got, rule.SeverityOff)
	}

	other, shipped := rule.Default(rule.UnboundedString)
	if !shipped {
		t.Fatal("the other shipped rule is not shipped")
	}

	if got := cfg.Validation.Rules[rule.UnboundedString]; got != other {
		t.Errorf("the rule nothing said about resolved to %q, want its default %q", got, other)
	}
}

func TestABareOffIsReadAsTheSeverityRatherThanAsFalse(t *testing.T) {
	for _, spelling := range []string{"off", `"off"`, "'off'"} {
		t.Run(spelling, func(t *testing.T) {
			cfg, err := loadRules(t, "  rules:\n    "+string(rule.UnboundedString)+": "+
				spelling+"\n")
			if err != nil {
				t.Fatalf("loading it: %v", err)
			}

			if got := cfg.Validation.Rules[rule.UnboundedString]; got != rule.SeverityOff {
				t.Errorf("%s resolved to %q, want %q", spelling, got, rule.SeverityOff)
			}
		})
	}
}

func TestARuleThisBuildDoesNotShipIsRefused(t *testing.T) {
	_, err := loadRules(t, "  rules:\n    no-such-rule: warn\n")
	if err == nil {
		t.Fatal("a rule nothing ships was accepted")
	}

	for _, phrase := range []string{"validation.rules.no-such-rule", `no rule is called`,
		string(rule.MissingPrimaryKey), string(rule.UnboundedString)} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("error %q does not say %q", err, phrase)
		}
	}
}

func TestAnOperationKindIsNotARuleId(t *testing.T) {
	_, err := loadRules(t, "  rules:\n    drop_table: off\n")
	if err == nil {
		t.Fatal("naming an operation kind under validation.rules was accepted, which would read " +
			"as a second way to permit destruction")
	}

	if !strings.Contains(err.Error(), "no rule is called") {
		t.Errorf("error %q does not refuse it as an unknown rule", err)
	}
}

func TestASeverityNothingRecognizesIsRefused(t *testing.T) {
	for _, c := range []struct {
		given string
		says  string
	}{
		{"fatal", `"fatal" is not a severity`},
		{"OFF", `did you mean "off"`},
		{"no", `"no" is not a severity`},
	} {
		t.Run(c.given, func(t *testing.T) {
			_, err := loadRules(t, "  rules:\n    "+string(rule.UnboundedString)+": "+
				c.given+"\n")
			if err == nil {
				t.Fatal("it was accepted")
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error %q does not say %q", err, c.says)
			}

			if !strings.Contains(err.Error(), "validation.rules."+string(rule.UnboundedString)) {
				t.Errorf("error %q does not name the key it is about", err)
			}
		})
	}
}

func TestEveryRuleProblemIsReportedAtOnce(t *testing.T) {
	_, err := loadRules(t, "  rules:\n    no-such-rule: warn\n    "+
		string(rule.UnboundedString)+": fatal\n")
	if err == nil {
		t.Fatal("two problems were accepted")
	}

	for _, phrase := range []string{"no-such-rule", "fatal"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("error %q does not report %q, and this package reports every problem", err,
				phrase)
		}
	}
}

// settling is how many times a load is repeated before its report is called deterministic.
const settling = 50

func TestEveryRuleProblemIsReportedInTheSameOrderEveryTime(t *testing.T) {
	block := "  rules:\n    delta-imaginary: warn\n    alpha-imaginary: warn\n" +
		"    charlie-imaginary: warn\n    bravo-imaginary: warn\n"

	_, first := loadRules(t, block)
	if first == nil {
		t.Fatal("four rules nothing ships were accepted")
	}

	for range settling {
		_, err := loadRules(t, block)
		if err == nil {
			t.Fatal("the same config was accepted on a later run")
		}

		if err.Error() != first.Error() {
			t.Fatalf("two loads of one config report their problems in different orders:"+
				"\n%v\n%v", first, err)
		}
	}
}

func TestTheResolvedRulesRenderInASortedOrder(t *testing.T) {
	cfg, err := loadRules(t, "")
	if err != nil {
		t.Fatalf("loading it: %v", err)
	}

	rendered := renderYAML(t, cfg)

	previous := -1

	for _, id := range rule.IDs() {
		at := strings.Index(rendered, string(id)+":")
		if at < 0 {
			t.Fatalf("the rendered config does not name rule %q:\n%s", id, rendered)
		}

		if at < previous {
			t.Errorf("rule %q renders before one that sorts above it, so two runs could differ",
				id)
		}

		previous = at
	}
}
