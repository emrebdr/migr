package rule

import (
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Rule is one judgement a validation run makes about a migration it has already read.
type Rule struct {
	// ID names the rule, and is what migr.yaml configures it by.
	ID ID
	// Default is the severity a project that says nothing about this rule gets.
	Default Severity
	// Summary says in one line what the rule checks, which migr config prints beside the severity.
	Summary string
	// Judge reports everything the rule found, and nothing at all for a migration it approves of.
	Judge func(Subject) []Finding
}

// Undo is what a validation run found that undoes a migration, which is a fact a rule can judge.
type Undo string

// The five answers to "what undoes this migration", each of which some migration really gives.
const (
	// UndoAuthored means the migration authors its own down.
	UndoAuthored Undo = "authored"
	// UndoDerived means it authors none and the engine inverted its up instead.
	UndoDerived Undo = "derived"
	// UndoDeclared means its author declared it irreversible, with a reason.
	UndoDeclared Undo = "declared"
	// UndoNone means nothing can undo it, which the reverse check reports as a problem.
	UndoNone Undo = "none"
	// UndoUnread means its down could not be read, which the format check reports as a problem.
	UndoUnread Undo = "unread"
)

// Half is one direction's worth of what a migration does: the operations, and its own statements.
type Half struct {
	// Operations are what the migration emitted, in the order it emitted them.
	Operations ir.Operations
	// Direct are the statements it sends itself, which no rollback reaches (ADR 0032, ADR 0036).
	Direct []driver.Query
}

// Subject is one migration as a validation run has already read it, which is what rules judge.
type Subject struct {
	// Migration is the migration under judgement, so every finding can name it (invariant #9).
	Migration migration.Migration
	// Up is what it does.
	Up Half
	// Down is what undoes it, which is empty for a migration nothing undoes.
	Down Half
	// Undo is where that down came from, since authoring one and deriving one are different facts.
	Undo Undo
}

// Finding is one thing a rule found, which the set stamps with the rule and its severity.
type Finding struct {
	// Direction is which half the finding is in.
	Direction ir.Direction
	// Operation is its place in that half, counting from one, and zero for a whole half.
	Operation int32
	// Kind is the operation at fault, empty where the finding is not about one.
	Kind ir.Kind
	// Detail states what the rule found, in terms the migration's author can act on.
	Detail string
}

// Judgement is one finding, under the rule that made it and the severity this project gave it.
type Judgement struct {
	// Rule is the rule that found it, which is also what a project configures to change this.
	Rule ID
	// Severity is what this project decided the finding is worth, and is never off.
	Severity Severity
	// Finding is what the rule reported.
	Finding
}

// Numbered returns one half's operations, each carrying where it sits, counting from one.
func (h Half) Numbered(direction ir.Direction) []Operation {
	return numbered(direction, h.Operations)
}

// Operation is one operation together with where in a migration it sits.
type Operation struct {
	// Direction is the half it belongs to.
	Direction ir.Direction
	// Index is its place in that half, counting from one, as a golden file and a report both do.
	Index int32
	// Op is the operation itself.
	Op ir.Operation
}

// Finding returns a finding about this operation, which is what most rules report.
func (o Operation) Finding(detail string) Finding {
	return Finding{Direction: o.Direction, Operation: o.Index, Kind: o.Op.Kind(), Detail: detail}
}

// numbered pairs each of a half's operations with its position, counting from one.
func numbered(direction ir.Direction, ops ir.Operations) []Operation {
	found := make([]Operation, 0, len(ops))

	var index int32

	for _, op := range ops {
		index++

		found = append(found, Operation{Direction: direction, Index: index, Op: op})
	}

	return found
}
