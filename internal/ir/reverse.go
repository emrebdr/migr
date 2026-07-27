package ir

import (
	"errors"
	"fmt"
)

// ErrIrreversible marks an operation that cannot be undone, for errors.Is.
var ErrIrreversible = errors.New("irreversible")

// IrreversibleError reports an operation with no inverse, and why it has none.
type IrreversibleError struct {
	Kind   Kind
	Reason string
}

// Error names the operation and the reason it cannot be reversed.
func (e *IrreversibleError) Error() string {
	return fmt.Sprintf("%s is irreversible: %s", e.Kind, e.Reason)
}

// Unwrap reports the sentinel so errors.Is(err, ErrIrreversible) succeeds.
func (e *IrreversibleError) Unwrap() error {
	return ErrIrreversible
}

// irreversible builds the verdict for an operation that has no inverse.
func irreversible(k Kind, reason string) error {
	return &IrreversibleError{Kind: k, Reason: reason}
}

// missingFrom builds the verdict for an operation whose prior state was not recorded.
func missingFrom(k Kind, what string) error {
	return irreversible(k, fmt.Sprintf(
		"the %s is not recorded; supply \"from\" to make it reversible", what))
}

// ReverseAll inverts every operation and reverses their order, or fails whole.
func ReverseAll(ops Operations) (Operations, error) {
	reversed := make(Operations, len(ops))

	for i, op := range ops {
		inverse, err := op.Reverse()
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", i, op.Kind(), err)
		}

		reversed[len(ops)-1-i] = inverse
	}

	return reversed, nil
}
