package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// withSafety is a minimal migr.yaml whose safety section a test supplies.
func withSafety(safety string) string {
	return "version: 1\nmigrations:\n  dir: ./migrations\n  language: sql\ndatabases:\n  default:\n" +
		"    driver: postgres\n    dsn: postgres://localhost/db\n" + safety
}

// loadString parses a whole migr.yaml held as a string, failing where it does not load.
func loadString(t *testing.T, src string) *Config {
	t.Helper()

	cfg, err := parseString(src)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	return cfg
}

// parseString parses a whole migr.yaml held as a string, with no environment at all.
func parseString(src string) (*Config, error) {
	return parse([]byte(src), "migr.yaml", func(string) (string, bool) { return "", false })
}

func TestEveryRewriteIsOnUnlessTheDocumentSaysOtherwise(t *testing.T) {
	cfg := loadString(t, withSafety(""))

	if !*cfg.Safety.AutoRewrite || !slices.Equal(cfg.Safety.Enabled(), driver.RewriteNames()) {
		t.Errorf("an unset safety section enables %v with auto_rewrite %v, want every rewrite",
			cfg.Safety.Enabled(), *cfg.Safety.AutoRewrite)
	}

	for _, name := range driver.RewriteNames() {
		if cfg.Safety.Rewrites[name] != SwitchOn {
			t.Errorf("safety.rewrites.%s resolves to %q, want on, so migr config lists it", name,
				cfg.Safety.Rewrites[name])
		}
	}

	t.Log("CREATE INDEX CONCURRENTLY is the PostgreSQL default the task names, so a project that")
	t.Log("never mentions rewrites gets all three -- and migr plan shows each one it applies.")
}

func TestOneRewriteCanBeSwitchedOffAndTheRestStayOn(t *testing.T) {
	cfg := loadString(t, withSafety("safety:\n  rewrites:\n    concurrent_index: off\n"))

	want := []driver.RewriteName{driver.RewriteNotValid, driver.RewriteNotNullCheck}
	if got := cfg.Safety.Enabled(); !slices.Equal(got, want) {
		t.Errorf("enabled %v, want %v", got, want)
	}
}

func TestAutoRewriteOffSwitchesEveryRewriteOff(t *testing.T) {
	cfg := loadString(t, withSafety("safety:\n  auto_rewrite: false\n  rewrites:\n    not_valid: on\n"))

	if got := cfg.Safety.Enabled(); len(got) != 0 {
		t.Errorf("auto_rewrite: false still enables %v", got)
	}
}

func TestAnUnknownRewriteOrValueFailsTheLoad(t *testing.T) {
	for body, want := range map[string]string{
		"safety:\n  rewrites:\n    concurent_index: off\n": "safety.rewrites.concurent_index",
		"safety:\n  rewrites:\n    not_valid: maybe\n":     "safety.rewrites.not_valid",
	} {
		_, err := parseString(withSafety(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("loading %q returned %v, want a refusal naming %s", body, err, want)
		}
	}

	t.Log("A misspelled rewrite read in silence leaves on the very rewrite the operator meant to")
	t.Log("switch off -- which is the line that reads like a decision and does nothing.")
}
