package migr

import "context"

// The seam a test reaches a migration through, which is the one the session reaches it through.
//
// A migration is handed a *Schema and nothing else, so the thing that can close the window has to
// be something else again -- otherwise a migration could seal its own call and emit afterwards.
// TypeScript says that in the type system, where Window extends Schema with one more member
// (ADR 0042); Go says it by holding the Schema rather than embedding it, so Seal is reachable only
// from what NewWindow returned.
//
// Where TypeScript takes a Call describing the three facts, this takes them as parameters: Call is
// already the name of the default-expression constructor in column.go, and a public API does not get
// to rename one of its own exports to make a second one read nicely.

// Host is what a callback goes through: this side's own numbering, framing, and pending table.
type Host interface {
	// Call sends one request and waits for its answer, decoding the result into target.
	Call(ctx context.Context, method string, params, target any) error
	// Notify sends one notification, which nothing answers and nothing waits on.
	Notify(method string, params any) error
}

// Window is a Schema whoever opened it can close, which a migration is never handed.
type Window struct {
	// schema is what the migration acts through, and all of this a migration ever sees.
	schema *Schema
}

// NewWindow opens the window one migration.execute acts through, over the Host it is given.
func NewWindow(migration string, dir Direction, mode Mode, h Host) *Window {
	return &Window{schema: newSchema(h, migration, dir, mode)}
}

// Schema returns what a migration is handed, which cannot close the call it is running in.
func (w *Window) Schema() *Schema {
	return w.schema
}

// Seal ends the call, reporting how many operations it recorded and what it never applied.
func (w *Window) Seal() (int, error) {
	return w.schema.seal()
}
