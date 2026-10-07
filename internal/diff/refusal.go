package diff

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// Refusal is one change this differ will not propose, and what stopped it.
type Refusal struct {
	// Kind is the operation it would have emitted.
	Kind ir.Kind
	// Subject is what that operation would have acted on, named the way the operation names it.
	Subject string
	// Reason states what stopped it, in terms an operator can act on.
	Reason string
}

// Describe renders one refusal as a line, naming the operation before what stopped it.
func (r Refusal) Describe() string {
	return fmt.Sprintf("%s on %q: %s", r.Kind, r.Subject, r.Reason)
}

// refuse records one change this differ will not propose.
func (d *differ) refuse(kind ir.Kind, subject, reason string) {
	d.refusals = append(d.refusals, Refusal{Kind: kind, Subject: subject, Reason: reason})
}

// qualified names a column the way an operator reads one, since a column name alone names many.
func qualified(table, column string) string {
	return table + "." + column
}
