package validate

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
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
	// Allow is the destruction a run would be permitted, and an unset one permits none of it.
	Allow engine.Allowance
	// Capabilities is what the connected server declares, and is nil for a run that opened nothing.
	Capabilities *capability.Set
}

// check reports what a validation run was not given, before it reports on anything else.
func (o Options) check() error {
	if o.Planner.Compiler == nil {
		return plan.ErrNoCompiler
	}

	if o.Source == nil {
		return ErrNoSource
	}

	return o.checkCapabilities()
}

// checkCapabilities refuses a declaration no driver built, which would refuse every operation.
func (o Options) checkCapabilities() error {
	if o.Capabilities == nil || o.Capabilities.Driver() != "" {
		return nil
	}

	return ErrUnbuiltCapabilities
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

	r.destructive(m, up.Operations)
	r.compile(ctx, m, ir.Up, up.Operations)
	r.undo(ctx, m, up)
}

// destructive reports every operation in a migration's up half that destroys what nothing permits.
func (r *runner) destructive(m migration.Migration, ops ir.Operations) {
	for _, found := range engine.Destructive(ops, r.opts.Allow) {
		r.report.add(Problem{
			Migration: m,
			Check:     CheckDestructive,
			Direction: ir.Up,
			Operation: found.Operation,
			Kind:      found.Kind,
			Detail:    found.Detail,
		})
	}
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
		r.compile(ctx, m, ir.Down, down.Operations)
	case errors.Is(err, engine.ErrDeclaredIrreversible):
		return
	case errors.Is(err, engine.ErrNoDownAuthored):
		r.derive(ctx, m, up)
	default:
		r.unanswered(m, ir.Down, err)
	}
}

// derive inverts what a migration does, reporting every operation nothing can undo rather than the first.
func (r *runner) derive(ctx context.Context, m migration.Migration, up engine.Emitted) {
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
	r.compile(ctx, m, ir.Down, inverse)
}

// compile turns one half's operations into statements, reporting every one anything refuses.
func (r *runner) compile(ctx context.Context, m migration.Migration, dir ir.Direction,
	ops ir.Operations,
) {
	var index int32

	for _, op := range ops {
		index++

		r.operation(ctx, m, dir, index, op)
	}
}

// operation reports what the compiler refuses about one operation, and then what the server does.
func (r *runner) operation(ctx context.Context, m migration.Migration, dir ir.Direction,
	index int32, op ir.Operation,
) {
	err := r.errorFrom(ctx, m, index, op)

	switch {
	case err == nil:
	case errors.Is(err, driver.ErrNeedsSession):
		r.report.skip(uncheckedOperation(m, dir, index, op, err))
	default:
		r.report.add(compileProblem(m, dir, err))

		return
	}

	r.capable(m, dir, index, op)
}

// capable reports an operation the connected server declares it will not perform, where one answered.
func (r *runner) capable(m migration.Migration, dir ir.Direction, index int32, op ir.Operation) {
	if r.opts.Capabilities == nil {
		return
	}

	verdict := r.opts.Capabilities.Resolve(op)
	if verdict.Allowed() {
		return
	}

	r.report.add(Problem{
		Migration: m,
		Check:     CheckCapability,
		Direction: dir,
		Operation: index,
		Kind:      op.Kind(),
		Detail:    verdict.Reason(),
	})
}

// errorFrom compiles one operation and reports what stopped it, discarding the statements.
func (r *runner) errorFrom(ctx context.Context, m migration.Migration, index int32,
	op ir.Operation,
) error {
	_, err := r.opts.Planner.Step(ctx, m, index, op)

	return err
}

// uncheckedOperation reports an operation this run could not judge because it opened no database.
func uncheckedOperation(m migration.Migration, dir ir.Direction, index int32, op ir.Operation,
	err error,
) Unchecked {
	return Unchecked{Migration: m, Direction: dir, Operation: index, Kind: op.Kind(),
		Reason: detailOf(m, causeOf(err))}
}

// causeOf unwraps a plan's own framing, since an unchecked operation already names its position.
func causeOf(err error) error {
	var failed *plan.CompileError
	if errors.As(err, &failed) {
		return failed.Err
	}

	return err
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
