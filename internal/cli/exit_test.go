package cli

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

func TestExitCode(t *testing.T) {
	sentinel := errors.New("boom")

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil is success", err: nil, want: ExitOK},
		{name: "untagged error is runtime", err: sentinel, want: ExitRuntime},
		{name: "tagged validation", err: NewExitError(ExitValidation, sentinel), want: ExitValidation},
		{name: "tagged drift", err: NewExitError(ExitDrift, sentinel), want: ExitDrift},
		{
			name: "found through a wrap",
			err:  fmt.Errorf("applying migration: %w", NewExitError(ExitLock, sentinel)),
			want: ExitLock,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Errorf("ExitCode = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGivingUpOnTheLockExitsWithTheLockContentionCode(t *testing.T) {
	busy := &driver.LockBusyError{
		Driver: "postgres",
		Wait:   30 * time.Second,
		Holder: &driver.LockHolder{Process: "pid 4711"},
	}

	tests := []struct {
		name string
		err  error
	}{
		{name: "on its own", err: busy},
		{name: "found through a wrap", err: fmt.Errorf("taking the migration lock: %w", busy)},
		{name: "as the bare sentinel", err: driver.ErrLockTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != ExitLock {
				t.Errorf("ExitCode = %d, want %d", got, ExitLock)
				t.Log("CLAUDE.md §5 assigns 3 to lock contention. internal/cli cannot name a driver")
				t.Log("(invariant #1), so the contract's sentinel is the only thing it can read.")
			}
		})
	}
}

func TestAnEditedMigrationExitsWithTheChecksumDriftCode(t *testing.T) {
	drift := &engine.DriftError{
		Version:   "20260727T153000",
		Direction: ir.Up,
		Recorded:  checksum.Checksum{Source: checksum.OfSource([]byte("as applied"))},
		Source:    checksum.OfSource([]byte("as edited")),
	}

	tests := []struct {
		name string
		err  error
	}{
		{name: "on its own", err: drift},
		{name: "found through a wrap", err: fmt.Errorf("applying migrations: %w", drift)},
		{name: "as the bare sentinel", err: engine.ErrDrifted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != ExitDrift {
				t.Errorf("ExitCode = %d, want %d", got, ExitDrift)
				t.Log("CLAUDE.md §5 assigns 4 to checksum drift, and invariant #6 makes a mismatch")
				t.Log("a hard error no flag repairs. The engine refuses before anything runs, so")
				t.Log("the sentinel is the only thing internal/cli has to read.")
			}
		})
	}
}

func TestAnUnreadableChecksumIsNotDrift(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "a checksum that will not parse",
			err: &history.CorruptError{
				Table: "migr_migrations", Field: "checksum", Err: checksum.ErrMalformed,
			},
		},
		{
			name: "a row written under a scheme this build cannot compute",
			err: &history.CorruptError{
				Table: "migr_migrations",
				Field: "checksum",
				Err:   &checksum.SchemeError{Received: "migr2", Supported: checksum.Supported()},
			},
		},
		{name: "the bare corrupt sentinel", err: history.ErrCorrupt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != ExitRuntime {
				t.Errorf("ExitCode = %d, want %d", got, ExitRuntime)
				t.Log("A row this build cannot read needs manual repair and one under a future")
				t.Log("scheme needs a newer migr. Neither is someone editing an applied migration,")
				t.Log("and reporting either as exit 4 is how operators learn to ignore exit 4.")
			}
		})
	}
}

func TestAnExplicitExitCodeStillBeatsTheDriftSentinel(t *testing.T) {
	drift := &engine.DriftError{Version: "20260727T153000"}

	err := NewExitError(ExitValidation, drift)
	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("ExitCode = %d, want %d", got, ExitValidation)
		t.Log("A command that classified a failure itself has more context than this fallback,")
		t.Log("and migr validate will classify drift as one problem among several it reports.")
	}
}

func TestAnExplicitExitCodeStillBeatsTheLockSentinel(t *testing.T) {
	busy := &driver.LockBusyError{Driver: "postgres", Wait: time.Second}

	err := NewExitError(ExitValidation, busy)
	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("ExitCode = %d, want %d", got, ExitValidation)
		t.Log("A command that classified a failure itself has more context than this fallback.")
	}
}

func TestExitErrorUnwrapsToCause(t *testing.T) {
	sentinel := errors.New("cause")

	err := fmt.Errorf("wrapped: %w", NewExitError(ExitLockImpact, sentinel))
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find the underlying cause through ExitError")
	}
}
