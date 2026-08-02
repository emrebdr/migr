package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

func TestEveryDurationMigrCanSpellIsOneSettingPostgreSQLAccepts(t *testing.T) {
	tests := []struct {
		name string
		wait time.Duration
		want string
	}{
		{name: "the contract's lock default", wait: 30 * time.Second, want: "30000ms"},
		{name: "the contract's statement default", wait: 3 * time.Second, want: "3000ms"},
		{name: "already milliseconds", wait: 150 * time.Millisecond, want: "150ms"},
		{name: "a minute and a half", wait: 90 * time.Second, want: "90000ms"},
		{name: "two minutes", wait: 2 * time.Minute, want: "120000ms"},
		{name: "an hour", wait: time.Hour, want: "3600000ms"},
		{name: "a day", wait: 24 * time.Hour, want: "86400000ms"},
		{name: "the longest lock_timeout holds", wait: maxLockTimeout, want: "2147483647ms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockTimeoutSetting(tt.wait); got != tt.want {
				t.Errorf("%s encodes to %q, want %q", tt.wait, got, tt.want)
			}
		})
	}
}

func TestGoCanonicalDurationTextIsNotSomethingPostgreSQLWouldRead(t *testing.T) {
	tests := []struct {
		wait     time.Duration
		rendered string
	}{
		{wait: 90 * time.Second, rendered: "1m30s"},
		{wait: 2 * time.Minute, rendered: "2m0s"},
		{wait: time.Hour, rendered: "1h0m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.rendered, func(t *testing.T) {
			if tt.wait.String() != tt.rendered {
				t.Fatalf("Go renders %s as %q, and this test assumed %q", tt.wait, tt.wait,
					tt.rendered)
			}

			if got := lockTimeoutSetting(tt.wait); got == tt.rendered {
				t.Log("PostgreSQL 16 and 17 both answer a multi-unit value with \"invalid value for")
				t.Log("parameter\", and a refused set_config means the bound is LOST rather than")
				t.Log("applied. Every value tried by hand -- 30s, 3s, 150ms -- happens to round-trip,")
				t.Log("which is why passing Duration.String straight through survives testing.")
				t.Errorf("the encoder produced %q, which is Go's spelling and not PostgreSQL's", got)
			}
		})
	}
}

func TestASubMillisecondWaitRoundsUpRatherThanToForever(t *testing.T) {
	tests := []struct {
		name string
		wait time.Duration
		want string
	}{
		{name: "half a millisecond", wait: 500 * time.Microsecond, want: "1ms"},
		{name: "one nanosecond", wait: time.Nanosecond, want: "1ms"},
		{name: "a millisecond and a half", wait: 1500 * time.Microsecond, want: "2ms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lockTimeoutSetting(tt.wait)
			if got != tt.want {
				t.Errorf("%s encodes to %q, want %q", tt.wait, got, tt.want)
				t.Log("Duration.Milliseconds truncates, so this would encode to \"0ms\" -- and")
				t.Log("lock_timeout of 0 is PostgreSQL's spelling for waiting indefinitely. A")
				t.Log("configured bound quietly becoming an unbounded one is the worst failure here.")
			}
		})
	}
}

func TestNoWaitThisDriverCanBeGivenEverMeansWaitForever(t *testing.T) {
	waits := []time.Duration{
		-time.Hour, -time.Nanosecond, 0, time.Nanosecond, time.Microsecond,
		time.Millisecond, time.Second, time.Hour, maxLockTimeout,
	}

	for _, wait := range waits {
		got := lockTimeoutSetting(wait)
		if got == "0ms" || got == "0" || strings.HasPrefix(got, "-") {
			t.Errorf("%s encodes to %q, which PostgreSQL reads as an unbounded wait", wait, got)
		}
	}
}

func TestAWaitPostgreSQLCouldNotStoreIsRefusedAtConstruction(t *testing.T) {
	tests := []struct {
		name string
		opts driver.Options
		key  string
	}{
		{
			name: "the acquire's bound",
			opts: driver.Options{LockWait: 30 * 24 * time.Hour},
			key:  "lock.timeout",
		},
		{
			name: "the per-statement bound",
			opts: driver.Options{StatementLockWait: maxLockTimeout + time.Millisecond},
			key:  "safety.lock_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.opts)
			if !errors.Is(err, ErrWaitTooLong) {
				t.Fatalf("New accepted the wait, reporting %v, want ErrWaitTooLong", err)
			}

			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("the refusal is %q and does not name %s", err, tt.key)
				t.Log("An operator edits a migr.yaml key, so the message names that key rather than")
				t.Log("the GUC underneath it. Failing at acquire time instead would report a config")
				t.Log("typo at the worst possible moment.")
			}
		})
	}
}

func TestTheLongestWaitPostgreSQLHoldsIsAccepted(t *testing.T) {
	if _, err := New(driver.Options{LockWait: maxLockTimeout}); err != nil {
		t.Errorf("New refused the longest wait lock_timeout stores: %v", err)
	}
}
