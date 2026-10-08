// Package source turns operations back into the migration source a language writes them as.
package source

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// Chain is one statement of a migration: what it starts from, and the calls made on it in order.
type Chain struct {
	// Root is the call the chain starts from, which names the object every link acts on.
	Root Call
	// Links are the calls made on it in order, the last of which is what emits the operation.
	Links []Call
}

// Call is one method call: what it is called, and the arguments it is given in order.
type Call struct {
	// Method is the call's name in the neutral spelling, which each language recases.
	Method string
	// Args are its arguments in order, each of a shape a language knows how to write.
	Args []Arg
}

// Arg is one argument of a call, in a shape each language's writer knows how to spell.
type Arg interface {
	isArg()
}

// Name is an identifier an operation names, which every language writes as a quoted string.
type Name string

func (Name) isArg() {}

// Names is a list of identifiers, which a language writes as a variadic list or an array.
type Names []string

func (Names) isArg() {}

// Logical is one of the logical types, which each language writes as its own constructor call.
type Logical schema.Type

func (Logical) isArg() {}

// Literal is a default value as text, which is what distinguishes an empty string from no default.
type Literal string

func (Literal) isArg() {}

// Function is one of the neutral default functions, which each language names its own way.
type Function schema.Function

func (Function) isArg() {}

// Action is one of the referential actions, which each language names its own way.
type Action schema.ReferentialAction

func (Action) isArg() {}

// Record is one of the recorded-state payloads, written as whatever a language calls a struct.
type Record struct {
	// Shape names which payload it is, since each language spells the type and the fields its way.
	Shape Shape
	// Fields are its members in the order the payload declares them, absent ones omitted.
	Fields []Field
}

func (Record) isArg() {}

// Field is one member of a record: the neutral member name and what it holds.
type Field struct {
	// Member is the field's neutral name, which each language recases.
	Member string
	// Value is what it holds, which is an argument of its own.
	Value Arg
}

// List is a sequence of records, which is what a recorded table definition's columns are.
type List []Record

func (List) isArg() {}

// Flag is a boolean a payload states, which only ever appears where stating it is the point.
type Flag bool

func (Flag) isArg() {}

// Shape names one of the record payloads a language has to be able to write.
type Shape string

// The record shapes, which are the five recorded-state payloads plus the column a table is built of.
const (
	// ShapeColumn is one column, as a create or an add states it and a drop records it.
	ShapeColumn Shape = "column"
	// ShapeTableDef is a dropped table's recorded definition.
	ShapeTableDef Shape = "table_def"
	// ShapeColumnsDef is a dropped index, unique or primary key's recorded columns.
	ShapeColumnsDef Shape = "columns_def"
	// ShapeCheckDef is a dropped check constraint's recorded expression.
	ShapeCheckDef Shape = "check_def"
	// ShapeForeignKeyDef is a dropped foreign key's recorded shape.
	ShapeForeignKeyDef Shape = "foreign_key_def"
	// ShapeDefault is a default value, which a column states and a column state records.
	ShapeDefault Shape = "default"
)

// ErrNotRenderable marks an operation no language writes, which is the one the IR has no builder for.
var ErrNotRenderable = fmt.Errorf("no migration source expresses it")

// A migration migr diff writes has to be reversible by derivation, which is ADR 0070's own stated
// payoff -- so every recorded-state payload the differ filled in is rendered too. A renderer that
// dropped them would write migrations this project's own migr validate rejects, and the reason the
// differ declines to emit a drop it cannot record would have been spent for nothing.
//
// One neutral chain with a writer per language rather than a renderer per language, because the Go
// and TypeScript builders are the same shape with different spelling: the method names recase, the
// type constructors carry a different prefix, and a record is a struct literal or an object. What
// is not shared is only ever how something is written down.

// Chains turns one migration's operations into the calls a language writes them as, in order.
func Chains(ops ir.Operations) ([]Chain, error) {
	chains := make([]Chain, 0, len(ops))

	for i, op := range ops {
		chain, err := chainOf(op)
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", i+1, op.Kind(), err)
		}

		chains = append(chains, chain)
	}

	return chains, nil
}

// table starts a chain from a table that already exists, which is what most operations act on.
func table(name string, links ...Call) Chain {
	return Chain{Root: Call{Method: "Table", Args: []Arg{Name(name)}}, Links: links}
}

// call builds one link of a chain.
func call(method string, args ...Arg) Call {
	return Call{Method: method, Args: args}
}

// recorded appends the Was call a recorded-state payload is carried by, and nothing for an absent one.
func recorded(links []Call, shape Shape, fields []Field, present bool) []Call {
	if !present {
		return links
	}

	return append(links, call("Was", Record{Shape: shape, Fields: fields}))
}
