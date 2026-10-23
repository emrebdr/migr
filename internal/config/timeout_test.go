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

func TestAStatementThatBlocksNoWriteIsUnboundedUnlessMigrYAMLBoundsIt(t *testing.T) {
	cfg, err := loadSeeding(t, "", "")
	if err != nil || cfg.Safety.Concurrent() != 0 {
		t.Fatalf("an unset concurrent_timeout resolved to %v (%v), want no bound", cfg, err)
	}

	bounded, err := loadSeeding(t, "safety:\n  concurrent_timeout: 2h\n", "")
	if err != nil || bounded.Safety.Concurrent() != 2*time.Hour {
		t.Fatalf("concurrent_timeout: 2h resolved to %v (%v)", bounded, err)
	}

	for _, block := range []string{
		"safety:\n  concurrent_timeout: -1s\n",
		"safety:\n  lock_timeout: 5s\n  concurrent_timeout: 5s\n",
	} {
		_, err := loadSeeding(t, block, "")
		if _, found := fieldProblem(err, "safety.concurrent_timeout"); !found {
			t.Errorf("%q loaded, or failed without naming safety.concurrent_timeout: %v", block, err)
		}
	}

	t.Log("A concurrent build blocks no write, so it is bounded apart from statement_timeout; it")
	t.Log("waits for its lock first, so a bound no longer than lock_timeout could never let it run.")
}

func TestACutoverLagTheCatchUpCouldNotFitIsRefused(t *testing.T) {
	for _, block := range []string{
		"safety:\n  cutover_lag: -1s\n",
		"safety:\n  lock_timeout: 2s\n  cutover_lag: 2s\n",
	} {
		_, err := loadSeeding(t, block, "")
		if _, found := fieldProblem(err, "safety.cutover_lag"); !found {
			t.Errorf("%q loaded, or failed without naming safety.cutover_lag: %v", block, err)
		}
	}

	cfg, err := loadSeeding(t, "safety:\n  cutover_lag: 250ms\n", "")
	if err != nil || cfg.Safety.CutoverLag.Duration() != 250*time.Millisecond {
		t.Fatalf("cutover_lag: 250ms resolved to %v (%v)", cfg, err)
	}

	short, err := loadSeeding(t, "safety:\n  lock_timeout: 500ms\n", "")
	if err != nil || short.Safety.CutoverLag.Duration() != 250*time.Millisecond {
		t.Errorf("an unset cutover_lag under lock_timeout: 500ms resolved to %v (%v), want half of it", short, err)
	}

	t.Log("An unattended cutover replays what the stream trails by under the cutover's lock, which")
	t.Log("gives up at lock_timeout -- so a lag no shorter than that could never be caught up in it.")
}
