package plan

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// ErrNoCompiler marks a planner asked to compile with no driver to compile through, for errors.Is.
var ErrNoCompiler = errors.New("a planner needs a compiler")

// ErrNoOperation marks a hole in an operation list, which is a caller's bug rather than a driver's.
var ErrNoOperation = errors.New("this is not an operation")

// ErrNoExpander marks a deferred step handed to a planner with no session to expand it through.
var ErrNoExpander = errors.New("a deferred operation needs a session to compile against")

// ErrNoChunkCompiler marks a chunked step whose driver cannot render a chunk, for errors.Is.
var ErrNoChunkCompiler = errors.New("this driver cannot render a chunk of a data operation")

// ErrNothingCompiled marks an operation that compiled to nothing which would run, for errors.Is.
var ErrNothingCompiled = errors.New("this operation compiled to nothing that would run")

// CompileError names the migration and the operation a plan could not be built from.
type CompileError struct {
	// Migration is the migration whose operation could not be compiled.
	Migration migration.Migration
	// Index is the operation's place in that migration, counting from one.
	Index int32
	// Kind is the operation that failed, blank when the list held no operation there at all.
	Kind ir.Kind
	// Err is what the compiler reported.
	Err error
}

// Error names the migration, the operation and its position, and what went wrong with it.
func (e *CompileError) Error() string {
	return fmt.Sprintf("%s: operation %d (%s): %v",
		e.Migration.Filename(), e.Index, describeKind(e.Kind), e.Err)
}

// Unwrap reports the cause, so errors.Is and errors.As reach what the compiler returned.
func (e *CompileError) Unwrap() error {
	return e.Err
}

// describeKind renders an operation kind, naming the hole when the list held no operation.
func describeKind(kind ir.Kind) string {
	if kind == "" {
		return "no operation"
	}

	return kind.String()
}
