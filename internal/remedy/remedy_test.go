package remedy

import (
	"errors"
	"fmt"
	"testing"
)

// errStopped is the failure every case here attaches a remedy to.
var errStopped = errors.New("the run stopped")

// knowing is an error type that knows its own remedy, or says it has none.
type knowing struct{ next string }

// Error reports a fixed message.
func (k knowing) Error() string { return "knowing" }

// Next reports the remedy this case was built with.
func (k knowing) Next() string { return k.next }

func TestWithKeepsTheMessageAndTheChain(t *testing.T) {
	err := fmt.Errorf("applying: %w", With(errStopped, "run migr resolve"))

	if err.Error() != "applying: the run stopped" {
		t.Errorf("the message reads %q, want it unchanged by the remedy", err.Error())
	}

	if !errors.Is(err, errStopped) {
		t.Error("errors.Is no longer finds the failure under its remedy")
	}

	if got := Of(err); got != "run migr resolve" {
		t.Errorf("Of = %q, want the attached remedy", got)
	}
}

func TestTheOutermostRemedyWins(t *testing.T) {
	err := With(fmt.Errorf("wrapped: %w", With(errStopped, "the general advice")), "the command's own")

	if got := Of(err); got != "the command's own" {
		t.Errorf("Of = %q, want the outermost remedy", got)
	}
}

func TestAnErrorTypeMayKnowItsOwnRemedy(t *testing.T) {
	if got := Of(fmt.Errorf("wrapped: %w", knowing{next: "grant REPLICATION"})); got != "grant REPLICATION" {
		t.Errorf("Of = %q, want the type's own remedy", got)
	}
}

func TestAnEmptyRemedyIsLookedPast(t *testing.T) {
	err := With(errStopped, "the deeper one")
	outer := fmt.Errorf("wrapped: %w", &Error{Err: err})

	if got := Of(outer); got != "the deeper one" {
		t.Errorf("Of = %q, want the remedy under the empty one", got)
	}
}

func TestNoRemedyIsNothing(t *testing.T) {
	if got := Of(errStopped); got != "" {
		t.Errorf("Of = %q for an error nothing attached a remedy to", got)
	}

	if Of(nil) != "" || With(nil, "x") != nil {
		t.Error("a nil error gained a remedy or lost being nil")
	}
}

func TestAFallbackNeverHidesAMorePreciseRemedy(t *testing.T) {
	precise := With(errStopped, "install Node 24")

	if got := Of(Fallback(precise, "check the toolchain")); got != "install Node 24" {
		t.Errorf("Of = %q, want the remedy the deeper layer attached", got)
	}

	if got := Of(Fallback(errStopped, "check the toolchain")); got != "check the toolchain" {
		t.Errorf("Of = %q, want the fallback where nothing else said", got)
	}
}
