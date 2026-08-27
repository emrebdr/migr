package validate

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// Options is everything a validation run needs, none of which is a database.
type Options struct {
	// Migrations are the discovered migrations, in the order a run would perform them.
	Migrations []migration.Migration
	// Source yields the operations each one emits, and is the only thing here that reads a file.
	Source engine.Source
	// Planner compiles those operations for one engine, and holds a compiler rather than a session.
	Planner plan.Planner
}

// check reports what a validation run was not given, before it reports on anything else.
func (o Options) check() error {
	if o.Planner.Compiler == nil {
		return plan.ErrNoCompiler
	}

	if o.Source == nil {
		return ErrNoSource
	}

	return nil
}

// Run checks every migration and reports everything wrong with them, opening no database.
func Run(ctx context.Context, opts Options) (Report, error) {
	if err := opts.check(); err != nil {
		return Report{}, err
	}

	run := &runner{opts: opts, report: Report{checked: len(opts.Migrations)}}

	for _, m := range opts.Migrations {
		run.migration(ctx, m)
	}

	return run.report, nil
}

// runner holds one validation run's inputs and the problems it has found so far.
type runner struct {
	// opts is what this run was given.
	opts Options
	// report is what it has found, in the order it found it.
	report Report
}

// ask reads one half of a migration as a question, since a check that ran anything would not be one.
func (r *runner) ask(ctx context.Context, m migration.Migration, dir ir.Direction) (engine.Emitted, error) {
	return r.opts.Source.Emit(ctx, m, dir, engine.ModePlan)
}

// migration checks what one migration does, what that compiles to, and whether anything undoes it.
func (r *runner) migration(ctx context.Context, m migration.Migration) {
	up, err := r.ask(ctx, m, ir.Up)
	if err != nil {
		r.unanswered(m, ir.Up, err)

		return
	}

	r.compile(m, ir.Up, up.Operations)
	r.undo(ctx, m, up)
}

// unanswered tells a migration this run judged and found wanting from one it could not judge at all.
func (r *runner) unanswered(m migration.Migration, dir ir.Direction, err error) {
	if errors.Is(err, ErrNotCheckable) {
		r.report.skip(Unchecked{Migration: m, Direction: dir, Reason: detailOf(m, err)})

		return
	}

	r.report.add(Problem{Migration: m, Check: CheckFormat, Direction: dir,
		Detail: detailOf(m, err)})
}

// undo checks what undoes a migration: what its author wrote, what its author declares, or an inverse.
func (r *runner) undo(ctx context.Context, m migration.Migration, up engine.Emitted) {
	down, err := r.ask(ctx, m, ir.Down)

	switch {
	case err == nil:
		r.compile(m, ir.Down, down.Operations)
	case errors.Is(err, engine.ErrDeclaredIrreversible):
		return
	case errors.Is(err, engine.ErrNoDownAuthored):
		r.derive(m, up)
	default:
		r.unanswered(m, ir.Down, err)
	}
}

// derive inverts what a migration does, reporting every operation nothing can undo rather than the first.
func (r *runner) derive(m migration.Migration, up engine.Emitted) {
	if len(up.Direct) > 0 {
		r.report.add(directProblem(m, up.Direct))

		return
	}

	inverse := make(ir.Operations, 0, len(up.Operations))

	var index int32

	for _, op := range up.Operations {
		index++

		reversed, err := op.Reverse()
		if err != nil {
			r.report.add(reverseProblem(m, index, op, err))

			continue
		}

		inverse = append(inverse, reversed)
	}

	if len(inverse) != len(up.Operations) {
		return
	}

	slices.Reverse(inverse)
	r.compile(m, ir.Down, inverse)
}

// compile turns one half's operations into statements, reporting every one the driver refuses.
func (r *runner) compile(m migration.Migration, dir ir.Direction, ops ir.Operations) {
	var index int32

	for _, op := range ops {
		index++

		if _, err := r.opts.Planner.Step(m, index, op); err != nil {
			r.report.add(compileProblem(m, dir, err))
		}
	}
}

// directProblem reports a migration that reaches the database itself and authors nothing to undo it.
func directProblem(m migration.Migration, direct []driver.Query) Problem {
	return Problem{Migration: m, Check: CheckReverse, Direction: ir.Down,
		Detail: (&engine.DirectIrreversibleError{Direct: direct}).Error()}
}

// reverseProblem reports an operation with no inverse, naming what it is and why it has none.
func reverseProblem(m migration.Migration, index int32, op ir.Operation, err error) Problem {
	problem := Problem{Migration: m, Check: CheckReverse, Direction: ir.Down,
		Operation: index, Kind: op.Kind(), Detail: err.Error()}

	var irreversible *ir.IrreversibleError
	if errors.As(err, &irreversible) {
		problem.Detail = irreversible.Reason
	}

	return problem
}

// compileProblem reports an operation the driver refused, reading the position and kind it carries.
func compileProblem(m migration.Migration, dir ir.Direction, err error) Problem {
	problem := Problem{Migration: m, Check: CheckCompile, Direction: dir,
		Detail: detailOf(m, err)}

	var failed *plan.CompileError
	if errors.As(err, &failed) {
		problem.Operation = failed.Index
		problem.Kind = failed.Kind
		problem.Detail = failed.Err.Error()
	}

	return problem
}

// detailOf strips the filename an error leads with, since a problem already names its migration.
func detailOf(m migration.Migration, err error) string {
	return strings.TrimPrefix(err.Error(), m.Filename()+": ")
}
