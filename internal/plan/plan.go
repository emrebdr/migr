package plan

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Planner compiles a migration's operations to statements for one driver, without a database.
type Planner struct {
	// Compiler is the driver that turns operations into statements, which is not the session.
	Compiler driver.Compiler
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

// Step is one operation together with the statements it became.
type Step struct {
	// Index is the operation's place in the migration, counting from one.
	Index int32
	// Op is the operation as the migration emitted it, before any driver saw it.
	Op ir.Operation
	// Statements are what the driver compiled it to, in the order they must run.
	Statements []driver.Statement
}

// Compile turns one migration's operations into an ordered plan, naming any it cannot compile.
func (p Planner) Compile(m migration.Migration, dir ir.Direction, ops ir.Operations) (Plan, error) {
	if p.Compiler == nil {
		return Plan{}, fmt.Errorf("%w to compile %s through", ErrNoCompiler, m.Filename())
	}

	steps := make([]Step, 0, len(ops))

	var index int32

	for _, op := range ops {
		index++

		step, err := p.Step(m, index, op)
		if err != nil {
			return Plan{}, err
		}

		steps = append(steps, step)
	}

	return Plan{Migration: m, Direction: dir, Steps: steps}, nil
}

// Step compiles one operation, holding the driver to statements that would actually run.
func (p Planner) Step(m migration.Migration, index int32, op ir.Operation) (Step, error) {
	if op == nil {
		return Step{}, &CompileError{Migration: m, Index: index, Err: ErrNoOperation}
	}

	if err := op.Validate(); err != nil {
		return Step{}, &CompileError{Migration: m, Index: index, Kind: op.Kind(), Err: err}
	}

	stmts, err := p.Compiler.Compile(op)
	if err == nil {
		err = checkCompiled(stmts)
	}

	if err != nil {
		return Step{}, &CompileError{Migration: m, Index: index, Kind: op.Kind(), Err: err}
	}

	return Step{Index: index, Op: op, Statements: stmts}, nil
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

// Len returns how many statements the plan compiles to, which is what a run counts through.
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
