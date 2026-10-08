package fold

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// Refusal is one operation this projection could not fold, and what stopped it.
type Refusal struct {
	// Index is the operation's place in the list it came from, counting from one.
	Index int32
	// Kind is the operation it was.
	Kind ir.Kind
	// Subject is what it would have acted on, named the way the operation names it.
	Subject string
	// Reason states what stopped it, in terms an operator can act on.
	Reason string
}

// Describe renders one refusal as a line, naming the operation before what stopped it.
func (r Refusal) Describe() string {
	if r.Subject == "" {
		return fmt.Sprintf("operation %d (%s): %s", r.Index, r.Kind, r.Reason)
	}

	return fmt.Sprintf("operation %d (%s on %q): %s", r.Index, r.Kind, r.Subject, r.Reason)
}

// Unfoldable reports whether a refusal is the one that makes the whole projection unknowable.
func (r Refusal) Unfoldable() bool {
	return r.Kind == ir.KindRaw
}

// refuse records one operation this projection could not fold.
func (f *folder) refuse(at int32, kind ir.Kind, subject, reason string) {
	f.refusals = append(f.refusals, Refusal{Index: at, Kind: kind, Subject: subject, Reason: reason})
}

// qualified names a column the way an operator reads one, since a column name alone names many.
func qualified(table, column string) string {
	return table + "." + column
}
