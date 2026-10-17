package config

import (
	"testing"
	"time"
)

func TestAMigrationIsUnboundedUnlessMigrYAMLBoundsIt(t *testing.T) {
	cfg, err := loadSeeding(t, "", "")
	if err != nil {
		t.Fatalf("a config naming no safety block must load: %v", err)
	}

	if cfg.Safety.Migration() != 0 {
		t.Errorf("an unset migration_timeout resolved to %s, want no bound", cfg.Safety.Migration())
	}

	bounded, err := loadSeeding(t, "safety:\n  migration_timeout: 10m\n", "")
	if err != nil {
		t.Fatalf("migration_timeout: 10m was refused: %v", err)
	}

	if bounded.Safety.Migration() != 10*time.Minute {
		t.Errorf("migration_timeout: 10m resolved to %s", bounded.Safety.Migration())
	}
}

func TestTheTimeoutsAreRefusedOutOfOrder(t *testing.T) {
	cases := map[string]string{
		"safety.statement_timeout": "safety:\n  lock_timeout: 5s\n  statement_timeout: 5s\n",
		"safety.migration_timeout": "safety:\n  statement_timeout: 30s\n  migration_timeout: 10s\n",
	}

	for path, block := range cases {
		_, err := loadSeeding(t, block, "")
		if _, found := fieldProblem(err, path); !found {
			t.Errorf("%q loaded, or failed without naming %s: %v", block, path, err)
		}
	}

	t.Log("A statement bound no longer than the lock wait would stop a statement waiting for its")
	t.Log("lock as too slow, and a stopped statement is never retried -- so the retry M13-T2 built")
	t.Log("would quietly never run. And a migration is at least one statement.")
}

func TestAMigrationTimeoutOfNothingIsRefused(t *testing.T) {
	_, err := loadSeeding(t, "safety:\n  migration_timeout: -1s\n", "")
	if _, found := fieldProblem(err, "safety.migration_timeout"); !found {
		t.Errorf("a negative migration_timeout loaded, or failed without naming it: %v", err)
	}
}
