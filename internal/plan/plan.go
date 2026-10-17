package plan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Planner compiles a migration's operations to statements for one driver.
type Planner struct {
	// Compiler is the driver that turns operations into statements, which is not the session.
	Compiler driver.Compiler
	// Expander is the session an emulated operation is compiled against, and is nil where none is.
	Expander driver.Expander
	// ChunkRows is how many rows a chunk of a data-only migration covers, and zero runs none in chunks.
	ChunkRows int
	// Rewriter is the session that performs an operation more safely, and is nil where none is.
	Rewriter driver.Rewriter
	// Rewrites are the rewrites safety.rewrites leaves on, which is none where auto_rewrite is off.
	Rewrites []driver.RewriteName
}

// Plan is one migration compiled to statements, in the order they must run.
type Plan struct {
	// Migration is the migration these steps came from, so every message can name it.
	Migration migration.Migration
	// Direction is which way the migration was compiled.
	Direction ir.Direction
	// Steps are the emitted operations in order, each with what it compiled to.
	Steps []Step
}

// Step is one operation together with the statements it became, or the note that it has none yet.
type Step struct {
	// Index is the operation's place in the migration, counting from one.
	Index int32
	// Op is the operation as the migration emitted it, before any driver saw it.
	Op ir.Operation
	// Statements are what the driver compiled it to, in the order they must run.
	Statements []driver.Statement
	// Deferred reports an operation compiled against the database immediately before it runs.
	Deferred bool
	// Chunked reports a data operation run a keyed chunk at a time, each chunk committed on its own.
	Chunked bool
	// Rewrite is how this operation was rewritten into a safer sequence, and nil where it was not.
	Rewrite *Rewritten
}

// Rewritten is what a rewrite did to one operation, so a plan can say so beside the statements.
type Rewritten struct {
	// Name is the rewrite, the key under safety.rewrites that switches it off.
	Name driver.RewriteName
	// Reason says in one sentence what the sequence avoids.
	Reason string
	// Lock is what the sequence costs a live database.
	Lock capability.LockImpact
	// Plain is how many statements the operation compiles to without it.
	Plain int
}

// An operation the compiler answers with driver.ErrNeedsSession becomes a deferred step rather than
// an expansion, because an expansion is rendered from the catalog and the operations before it have
// not run yet (ADR 0071). Expand is what fills one in, and the caller is what knows when -- which is
// what puts compilation back to needing no database at all, and says so in the signature.

// Compile turns one migration's operations into an ordered plan, naming any it cannot compile.
func (p Planner) Compile(m migration.Migration, dir ir.Direction,
	ops ir.Operations,
) (Plan, error) {
	if p.Compiler == nil {
		return Plan{}, fmt.Errorf("%w to compile %s through", ErrNoCompiler, m.Filename())
	}

	steps := make([]Step, 0, len(ops))
	created := map[string]bool{}

	var index int32

	for _, op := range ops {
		index++

		step, err := p.Step(m, index, op)
		if err != nil {
			return Plan{}, err
		}

		steps = append(steps, p.rewrite(step, created))
		noteCreated(created, op)
	}

	return Plan{Migration: m, Direction: dir, Steps: p.chunk(steps)}, nil
}

// A migration runs in chunks only where it holds nothing but data operations (ADR 0078). Each chunk
// commits on its own, so a migration that also changed the schema could no longer be the one
// transaction it runs in -- and splitting one around its data is M13-T4's. A data-only migration
// needs no such split: no step of it reads what another left uncommitted.

// chunk marks every step of a data-only migration as run in chunks, and leaves any other plan alone.
func (p Planner) chunk(steps []Step) []Step {
	if p.ChunkRows < 1 || !DataOnly(steps) {
		return steps
	}

	for i := range steps {
		steps[i].Chunked = true
	}

	return steps
}

// DataOnly reports a migration that holds at least one operation and nothing but data operations.
func DataOnly(steps []Step) bool {
	if len(steps) == 0 {
		return false
	}

	for _, step := range steps {
		if !slices.Contains(ir.DataKinds(), step.Op.Kind()) {
			return false
		}
	}

	return true
}

// A rewrite replaces what one operation compiles to with a safer sequence the session answers with
// (ADR 0085). It is skipped on a table this migration created: nothing can be waiting on a table that
// did not exist when the migration began, and the bare statements a rewrite adds would split the
// migration for nothing but lost atomicity.

// rewrite gives a step the safer sequence its session performs it as, where one is on and worth taking.
func (p Planner) rewrite(step Step, created map[string]bool) Step {
	if p.Rewriter == nil || step.Deferred || created[tableOf(step.Op)] {
		return step
	}

	safer, ok := p.Rewriter.Rewrite(step.Op)
	if !ok || !slices.Contains(p.Rewrites, safer.Name) || checkCompiled(safer.Statements) != nil {
		return step
	}

	step.Rewrite = &Rewritten{Name: safer.Name, Reason: safer.Reason, Lock: safer.Lock,
		Plain: len(step.Statements)}
	step.Statements = safer.Statements

	return step
}

// tableOf names the existing table an operation a driver may rewrite changes, and nothing for any other.
func tableOf(op ir.Operation) string {
	switch o := op.(type) {
	case ir.AddCheck:
		return o.Table
	case ir.AddForeignKey:
		return o.Table
	case ir.AlterColumn:
		return o.Table
	case ir.AddIndex:
		return o.Table
	default:
		return ""
	}
}

// noteCreated records a table this migration made, following it through a rename.
func noteCreated(created map[string]bool, op ir.Operation) {
	switch o := op.(type) {
	case ir.CreateTable:
		created[o.Table] = true
	case ir.RenameTable:
		if created[o.From] {
			created[o.To] = true
		}
	}
}

// Rewritten returns how many of the plan's operations were rewritten into a safer sequence.
func (p Plan) Rewritten() int {
	n := 0

	for _, step := range p.Steps {
		if step.Rewrite != nil {
			n++
		}
	}

	return n
}

// Step compiles one operation, holding the driver to statements that would actually run.
func (p Planner) Step(m migration.Migration, index int32, op ir.Operation) (Step, error) {
	if op == nil {
		return Step{}, &CompileError{Migration: m, Index: index, Err: ErrNoOperation}
	}

	if err := op.Validate(); err != nil {
		return Step{}, &CompileError{Migration: m, Index: index, Kind: op.Kind(), Err: err}
	}

	stmts, deferred, err := p.statements(op)
	if err == nil && !deferred {
		err = checkCompiled(stmts)
	}

	if err != nil {
		return Step{}, &CompileError{Migration: m, Index: index, Kind: op.Kind(), Err: err}
	}

	return Step{Index: index, Op: op, Statements: stmts, Deferred: deferred}, nil
}

// statements compiles one operation, reporting the ones this planner must come back for.
func (p Planner) statements(op ir.Operation) ([]driver.Statement, bool, error) {
	stmts, err := p.Compiler.Compile(op)
	if errors.Is(err, driver.ErrNeedsSession) && p.Expander != nil {
		return nil, true, nil
	}

	return stmts, false, err
}

// Expand compiles one deferred step against the database, which is the only place its statements are.
func (p Planner) Expand(ctx context.Context, m migration.Migration, step Step) (Step, error) {
	if p.Expander == nil {
		return Step{}, &CompileError{Migration: m, Index: step.Index, Kind: step.Op.Kind(),
			Err: ErrNoExpander}
	}

	stmts, err := p.Expander.Expand(ctx, step.Op)
	if err == nil {
		err = checkCompiled(stmts)
	}

	if err != nil {
		return Step{}, &CompileError{Migration: m, Index: step.Index, Kind: step.Op.Kind(), Err: err}
	}

	return Step{Index: step.Index, Op: step.Op, Statements: stmts}, nil
}

// CompileChunk renders the statements one chunk of a step is, keyed by the given columns.
func (p Planner) CompileChunk(step Step, key []string) ([]driver.Statement, error) {
	chunker, chunks := p.Compiler.(driver.ChunkCompiler)
	if !chunks {
		return nil, ErrNoChunkCompiler
	}

	return chunker.CompileChunk(step.Op, key)
}

// checkCompiled refuses a compilation that reports success while changing nothing.
func checkCompiled(stmts []driver.Statement) error {
	if len(stmts) == 0 {
		return fmt.Errorf("%w: the driver returned no statements", ErrNothingCompiled)
	}

	for _, stmt := range stmts {
		if strings.TrimSpace(stmt.SQL) == "" {
			return fmt.Errorf("%w: one of its statements is blank", ErrNothingCompiled)
		}
	}

	return nil
}

// Statements returns every statement the plan compiles to, flattened across its steps in order.
func (p Plan) Statements() []driver.Statement {
	var stmts []driver.Statement

	for _, step := range p.Steps {
		stmts = append(stmts, step.Statements...)
	}

	return stmts
}

// Deferred returns how many of the plan's operations are still to be compiled against the database.
func (p Plan) Deferred() int32 {
	var pending int32

	for _, step := range p.Steps {
		if step.Deferred {
			pending++
		}
	}

	return pending
}

// Chunked returns how many of the plan's operations run in chunks, each committed on its own.
func (p Plan) Chunked() int32 {
	var chunked int32

	for _, step := range p.Steps {
		if step.Chunked {
			chunked++
		}
	}

	return chunked
}

// Len returns how many statements the plan has compiled to so far, which is what a run counts through.
func (p Plan) Len() int32 {
	var total int32

	for _, step := range p.Steps {
		for range step.Statements {
			total++
		}
	}

	return total
}

// At returns the operation the statement at a one-based position was compiled from.
func (p Plan) At(position int32) (ir.Operation, bool) {
	if position < 1 {
		return nil, false
	}

	var seen int32

	for _, step := range p.Steps {
		for range step.Statements {
			seen++

			if seen == position {
				return step.Op, true
			}
		}
	}

	return nil, false
}
