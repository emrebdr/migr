package config

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestALockRetryDefaultsToABackoffAndZeroRetriesTurnsItOff(t *testing.T) {
	cfg, err := loadSeeding(t, "", "")
	if err != nil {
		t.Fatalf("a config naming no safety block must load: %v", err)
	}

	if cfg.Safety.Retries() != driver.DefaultLockRetries ||
		cfg.Safety.LockBackoff.Duration() != driver.DefaultLockBackoff {
		t.Errorf("retrying defaulted to %d retries from %s, want %d from %s", cfg.Safety.Retries(),
			cfg.Safety.LockBackoff, driver.DefaultLockRetries, driver.DefaultLockBackoff)
	}

	off, err := loadSeeding(t, "safety:\n  lock_retries: 0\n", "")
	if err != nil {
		t.Fatalf("lock_retries: 0 was refused: %v", err)
	}

	if off.Safety.Retries() != 0 {
		t.Errorf("lock_retries: 0 resolved to %d, want 0", off.Safety.Retries())
		t.Log("Until M13-T2 nothing read this field, and zero was defaulted to five -- so a project")
		t.Log("could not ask for a statement to be tried only once.")
	}
}

func TestANegativeRetryCountOrABackoffOfNothingIsRefused(t *testing.T) {
	cases := map[string]string{
		"safety.lock_retries": "safety:\n  lock_retries: -1\n",
		"safety.lock_backoff": "safety:\n  lock_backoff: -5ms\n",
	}

	for path, block := range cases {
		_, err := loadSeeding(t, block, "")
		if _, found := fieldProblem(err, path); !found {
			t.Errorf("%q loaded, or failed without naming %s: %v", block, path, err)
		}
	}
}
