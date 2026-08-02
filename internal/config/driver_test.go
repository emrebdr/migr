package config

import (
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// optionsFrom loads a fixture and resolves its default database into what a driver is built from.
func optionsFrom(t *testing.T, fixture string) driver.Options {
	t.Helper()

	cfg, err := loadFixture(t, fixture, nil)
	if err != nil {
		t.Fatalf("loading %s: %v", fixture, err)
	}

	db, ok := cfg.Databases["default"]
	if !ok {
		t.Fatalf("%s has no default database", fixture)
	}

	return cfg.DriverOptions(db)
}

func TestMigrYamlsLockBlockReachesTheDriverUnchanged(t *testing.T) {
	opts := optionsFrom(t, "distinct_waits.yaml")

	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "lock.timeout", got: opts.LockWait, want: 45 * time.Second},
		{name: "safety.lock_timeout", got: opts.StatementLockWait, want: 2 * time.Second},
		{name: "lock.strategy", got: opts.LockStrategy, want: "advisory"},
		{name: "history.table", got: opts.HistoryTable, want: "bookings_migrations"},
		{name: "the database's schema", got: opts.Schema, want: "bookings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s reached the driver as %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestTheTwoTimeoutKeysDoNotTradePlacesOnTheWayToADriver(t *testing.T) {
	opts := optionsFrom(t, "distinct_waits.yaml")

	if opts.LockWait == 2*time.Second || opts.StatementLockWait == 45*time.Second {
		t.Errorf("the waits arrived as acquire=%s statement=%s, and the fixture sets 45s and 2s",
			opts.LockWait, opts.StatementLockWait)
		t.Log("lock.timeout bounds the wait for the migration lock; safety.lock_timeout bounds one")
		t.Log("DDL statement's wait, which is what invariant #12 requires. Swapped, the second")
		t.Log("becomes whatever an operator chose for waiting on other migr runs.")
	}
}

func TestConfigAndTheContractAgreeOnEveryDefaultTheyBothName(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	tests := []struct {
		key  string
		got  any
		want any
	}{
		{key: "history.table", got: cfg.History.Table, want: driver.DefaultHistoryTable},
		{key: "lock.timeout", got: cfg.Lock.Timeout.Duration(), want: driver.DefaultLockWait},
		{
			key:  "safety.lock_timeout",
			got:  cfg.Safety.LockTimeout.Duration(),
			want: driver.DefaultStatementLockWait,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s defaults to %v here and to %v on the contract", tt.key, tt.got, tt.want)
				t.Log("Two defaults for one setting means an operator who configures nothing gets")
				t.Log("one bound from migr.yaml and another from a driver built by hand. Taking")
				t.Log("them from the contract is what makes the two unable to drift apart.")
			}
		})
	}
}

func TestADriverBuiltFromAnEmptyConfigStillGetsUsableBounds(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	opts := cfg.DriverOptions(Database{})

	lockWait, statementWait := opts.Waits()
	if lockWait <= 0 || statementWait <= 0 {
		t.Errorf("the bounds resolved to %s and %s, and a non-positive one means forever",
			lockWait, statementWait)
	}
}
