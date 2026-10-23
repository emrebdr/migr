package remedy

import "errors"

// Nexter is an error that knows what a person should do about it.
type Nexter interface {
	// Next says what to do next, as one imperative sentence naming what to run or change.
	Next() string
}

// Error is an error with what to do next about it attached.
type Error struct {
	// Err is the failure itself, whose message this error keeps.
	Err error
	// Remedy is what to do next about it.
	Remedy string
}

// With attaches what to do next to err, and returns nil for a nil error.
func With(err error, next string) error {
	if err == nil {
		return nil
	}

	return &Error{Err: err, Remedy: next}
}

// Fallback attaches what to do next to err only where nothing beneath it already says what to do.
func Fallback(err error, next string) error {
	if err == nil || Of(err) != "" {
		return err
	}

	return With(err, next)
}

// Error reports the wrapped failure's message unchanged, the remedy being rendered apart from it.
func (e *Error) Error() string { return e.Err.Error() }

// Unwrap returns the failure, so errors.Is and errors.As see through the remedy.
func (e *Error) Unwrap() error { return e.Err }

// Next reports what to do next.
func (e *Error) Next() string { return e.Remedy }

// Of reports the outermost remedy in err's chain, and nothing where none was attached.
func Of(err error) string {
	var n Nexter

	for err != nil {
		if !errors.As(err, &n) {
			return ""
		}

		if next := n.Next(); next != "" {
			return next
		}

		err = errors.Unwrap(n.(error))
	}

	return ""
}
