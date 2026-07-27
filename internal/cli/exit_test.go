package cli

import (
	"errors"
	"fmt"
	"testing"
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

func TestExitErrorUnwrapsToCause(t *testing.T) {
	sentinel := errors.New("cause")

	err := fmt.Errorf("wrapped: %w", NewExitError(ExitLockImpact, sentinel))
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find the underlying cause through ExitError")
	}
}
