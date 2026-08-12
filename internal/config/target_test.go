package config

import (
	"errors"
	"strings"
	"testing"
)

// withDatabases returns a config defining the named databases and nothing else that matters here.
func withDatabases(names ...string) *Config {
	cfg := &Config{Databases: make(map[string]Database, len(names))}
	for _, name := range names {
		cfg.Databases[name] = Database{Driver: "postgres", DSN: Secret("postgres://localhost/" + name)}
	}

	return cfg
}

func TestTargetPrefersTheDatabaseThatWasNamed(t *testing.T) {
	name, db, err := withDatabases("default", "replica").Target("replica")
	if err != nil {
		t.Fatalf("Target: %v", err)
	}

	if name != "replica" || !strings.HasSuffix(db.DSN.Reveal(), "replica") {
		t.Errorf("Target(replica) = %q, %+v", name, db)
	}
}

func TestTargetFallsBackToTheDefaultKey(t *testing.T) {
	name, _, err := withDatabases("analytics", "default", "replica").Target("")
	if err != nil {
		t.Fatalf("Target: %v", err)
	}

	if name != DefaultDatabase {
		t.Errorf("Target(\"\") = %q, want %q", name, DefaultDatabase)
		t.Log("Map iteration order is random, so a config with several databases and no rule would")
		t.Log("migrate a different one on every invocation (invariant #8).")
	}
}

func TestTargetTakesTheOnlyDatabaseWhenThereIsOne(t *testing.T) {
	name, _, err := withDatabases("primary").Target("")
	if err != nil {
		t.Fatalf("Target: %v", err)
	}

	if name != "primary" {
		t.Errorf("Target(\"\") = %q, want the only database defined", name)
	}
}

func TestTargetRefusesToGuessBetweenSeveralDatabases(t *testing.T) {
	_, _, err := withDatabases("production", "staging").Target("")
	if !errors.Is(err, ErrAmbiguousDatabase) {
		t.Fatalf("Target(\"\") = %v, want a refusal", err)
	}

	for _, want := range []string{"production", "staging", "--database"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	t.Log("A migr.yaml holding a development target and a production one is an ordinary file.")
	t.Log("Fanning out across both, or picking whichever the map yields first, is a command")
	t.Log("reaching production because nobody typed a flag.")
}

func TestTargetRefusesANameNothingCarries(t *testing.T) {
	_, _, err := withDatabases("default").Target("replica")
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Target(replica) = %v, want a refusal", err)
	}

	if !strings.Contains(err.Error(), "default") {
		t.Errorf("the refusal does not list what is defined: %v", err)
	}
}

func TestTargetRefusesAConfigDefiningNone(t *testing.T) {
	if _, _, err := withDatabases().Target(""); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("Target(\"\") = %v, want a refusal", err)
	}
}
