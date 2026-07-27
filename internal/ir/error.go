package ir

import (
	"fmt"
	"strings"
)

// OpError attributes a failure to one recorded operation, per CLAUDE.md invariant #9.
type OpError struct {
	Migration string
	Direction Direction
	Index     int
	Kind      Kind
	Source    Source
	Err       error
}

// Error renders the failure location-first, in the form editors and humans both parse.
func (e *OpError) Error() string {
	var b strings.Builder

	if loc := e.Source.String(); loc != "" {
		b.WriteString(loc)
		b.WriteString(": ")
	}

	fmt.Fprintf(&b, "%s (%s) op %d (%s): %v", e.Migration, e.Direction, e.Index, e.Kind, e.Err)

	return b.String()
}

// Unwrap returns the underlying failure so errors.Is and errors.As still reach it.
func (e *OpError) Unwrap() error {
	return e.Err
}

// attribute wraps a failure with everything known about where the operation came from.
func attribute(migration string, d Direction, entry Entry, err error) error {
	if err == nil {
		return nil
	}

	return &OpError{
		Migration: migration,
		Direction: d,
		Index:     entry.Index,
		Kind:      kindOf(entry.Op),
		Source:    entry.Source,
		Err:       err,
	}
}

// kindOf reports an entry's operation kind, tolerating an entry that has none yet.
func kindOf(op Operation) Kind {
	if op == nil {
		return ""
	}

	return op.Kind()
}
