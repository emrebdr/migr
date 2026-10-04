package migr

import "context"

// Migration is one schema change, and Up is the only method a migration must have.
type Migration interface {
	// Up performs the change, emitting operations through the Schema it is given.
	Up(ctx context.Context, s *Schema) error
}

// Downer is a migration that authors its own undo, which is always preferred to a derived one.
type Downer interface {
	// Down undoes what Up did, emitting the operations that put the schema back.
	Down(ctx context.Context, s *Schema) error
}

// Irreversible is a migration whose author states it cannot be undone, and says why.
type Irreversible interface {
	// Irreversible returns the reason a rollback halts with, which invariant #10 requires be given.
	Irreversible() string
}

// Validator is a migration that checks itself, which migr validate runs and which emits nothing.
type Validator interface {
	// Validate reports what is wrong with this migration, reaching no database.
	Validate() error
}

// Dependent is a migration that must run after others, declaring them rather than computing them.
type Dependent interface {
	// Dependencies returns the versions this migration must follow, as a fixed list.
	Dependencies() []string
}

// Undo is how a migration is undone, which docs/protocol.md §7.4 closes to three answers.
type Undo string

// The three answers, and a migration gives exactly one of them.
const (
	// UndoAuthored is a migration that writes its own down, which no derived inverse replaces.
	UndoAuthored Undo = "authored"
	// UndoDeclared is a migration whose author states it cannot be undone, and says why.
	UndoDeclared Undo = "declared"
	// UndoDerived is a migration that authors none, whose inverse the engine derives from its up.
	UndoDerived Undo = "derived"
)

// Undone reports how a migration is undone, and the reason where its author declares one.
func Undone(m Migration) (Undo, string) {
	if reason, states := declared(m); states {
		return UndoDeclared, reason
	}

	if _, writes := authored(m); writes {
		return UndoAuthored, ""
	}

	return UndoDerived, ""
}

// authored reports whether a migration writes its own down, which no derived one replaces.
func authored(m Migration) (Downer, bool) {
	down, ok := m.(Downer)

	return down, ok
}

// declared reports the reason a migration gives for having no undo, and whether it gives one.
func declared(m Migration) (string, bool) {
	stated, ok := m.(Irreversible)
	if !ok {
		return "", false
	}

	return stated.Irreversible(), true
}

// dependencies returns what a migration declares it must follow, which is nothing by default.
func dependencies(m Migration) []string {
	dependent, ok := m.(Dependent)
	if !ok {
		return []string{}
	}

	declared := dependent.Dependencies()
	if declared == nil {
		return []string{}
	}

	return declared
}

// check runs a migration's own validation, which a migration that has none passes.
func check(m Migration) error {
	validator, ok := m.(Validator)
	if !ok {
		return nil
	}

	return validator.Validate()
}
