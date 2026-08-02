package cli

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
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
