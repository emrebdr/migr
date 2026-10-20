package validate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/rule"
	"github.com/emrebdr/migr/internal/schema"
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
	// Schema is what the target database holds now, and is nil for a run that opened nothing.
	Schema *schema.Snapshot
	// Rules are the lint rules this project runs, and nil is a run that performs no lint pass.
	Rules *rule.Set
	// Ceiling is safety.max_lock_impact for a run asked to gate on it, and empty runs no lock check.
	Ceiling capability.LockImpact
	// Online is how each migration declares it runs against a live table, keyed by version.
	Online migration.Online
	// Shadows reports a connected session that performs shadow-table migration.
	Shadows bool
}

// check reports what a validation run was not given, before it reports on anything else.
func (o Options) check() error {
	if o.Planner.Compiler == nil {
		return plan.ErrNoCompiler
	}

	if o.Source == nil {
		return ErrNoSource
	}

	if err := o.checkCapabilities(); err != nil {
		return err
	}

	if o.Ceiling != "" && o.Capabilities == nil {
		return ErrCeilingOffline
	}

	return o.checkRules()
}

// checkCapabilities refuses a declaration no driver built, which would refuse every operation.
func (o Options) checkCapabilities() error {
	if o.Capabilities == nil || o.Capabilities.Driver() != "" {
		return nil
	}

	return ErrUnbuiltCapabilities
}

// checkRules refuses a rule set nothing built, which would silently run no rule at all.
func (o Options) checkRules() error {
	if o.Rules == nil || !o.Rules.Zero() {
		return nil
	}

	return ErrUnbuiltRules
}

// Run checks every migration and reports everything wrong with them, opening no database.
func Run(ctx context.Context, opts Options) (Report, error) {
	if err := opts.check(); err != nil {
		return Report{}, err
	}

	run := &runner{
		opts:        opts,
		projections: projectionsFor(opts.Schema),
		report:      Report{checked: len(opts.Migrations)},
	}

	run.report.projections = startingPoints(run.projections)

	for _, m := range opts.Migrations {
		run.migration(ctx, m)
	}

	return run.report, nil
}

// startingPoints names what the reference check was able to project from, which a report states.
func startingPoints(held []*projection) []string {
	named := make([]string, 0, len(held))
	for _, one := range held {
		named = append(named, one.from)
	}

	return named
}

// runner holds one validation run's inputs and the problems it has found so far.
type runner struct {
	// opts is what this run was given.
	opts Options
	// projections are the running folds the reference check judges against, one per starting point.
	projections []*projection
	// stopped reports whether a raw operation has ended the projection, which no later one revives.
	stopped bool
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
	r.compile(m, ir.Up, up.Operations)
	r.lock(m, up.Operations)
	r.shadowable(m)

	down := r.undo(ctx, m, up)

	r.reference(m, up.Operations, down.emitted.Operations)
	r.lint(m, up, down)
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

// undone is what a run found that undoes a migration: the half itself, and where it came from.
type undone struct {
	// emitted is what the down half does, which a migration nothing undoes has none of.
	emitted engine.Emitted
	// from is where that came from, since authoring a down and deriving one are different facts.
	from rule.Undo
}

// undo checks what undoes a migration: what its author wrote, what its author declares, or an inverse.
func (r *runner) undo(ctx context.Context, m migration.Migration, up engine.Emitted) undone {
	down, err := r.ask(ctx, m, ir.Down)

	switch {
	case err == nil:
		r.compile(m, ir.Down, down.Operations)

		return undone{emitted: down, from: rule.UndoAuthored}
	case errors.Is(err, engine.ErrDeclaredIrreversible):
		return undone{from: rule.UndoDeclared}
	case errors.Is(err, engine.ErrNoDownAuthored):
		return r.derive(ctx, m, up)
	default:
		r.unanswered(m, ir.Down, err)

		return undone{from: rule.UndoUnread}
	}
}

// derive inverts what a migration does, reporting every operation nothing can undo rather than the first.
func (r *runner) derive(ctx context.Context, m migration.Migration, up engine.Emitted) undone {
	if len(up.Direct) > 0 {
		r.report.add(directProblem(m, up.Direct))

		return undone{from: rule.UndoNone}
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
		return undone{from: rule.UndoNone}
	}

	slices.Reverse(inverse)
	r.compile(m, ir.Down, inverse)

	return undone{emitted: engine.Emitted{Operations: inverse}, from: rule.UndoDerived}
}

// lint reports what every enabled rule found about a migration whose halves have both been read.
func (r *runner) lint(m migration.Migration, up engine.Emitted, down undone) {
	if r.opts.Rules == nil {
		return
	}

	for _, judged := range r.opts.Rules.Judge(subjectOf(m, up, down)) {
		problem := lintProblem(m, judged)

		switch judged.Severity {
		case rule.SeverityError:
			r.report.add(problem)
		case rule.SeverityWarn:
			r.report.warn(problem)
		case rule.SeverityOff:
		}
	}
}

// subjectOf renders one migration as the rules see it, which is both halves and what undoes it.
func subjectOf(m migration.Migration, up engine.Emitted, down undone) rule.Subject {
	return rule.Subject{
		Migration: m,
		Up:        rule.Half{Operations: up.Operations, Direct: up.Direct},
		Down:      rule.Half{Operations: down.emitted.Operations, Direct: down.emitted.Direct},
		Undo:      down.from,
	}
}

// lintProblem renders one judgement as a problem, naming the rule that made it.
func lintProblem(m migration.Migration, judged rule.Judgement) Problem {
	return Problem{
		Migration: m,
		Check:     CheckLint,
		Rule:      judged.Rule,
		Direction: judged.Direction,
		Operation: judged.Operation,
		Kind:      judged.Kind,
		Detail:    judged.Detail,
	}
}

// compile turns one half's operations into statements, reporting every one anything refuses.
func (r *runner) compile(m migration.Migration, dir ir.Direction, ops ir.Operations) {
	var index int32

	for _, op := range ops {
		index++

		r.operation(m, dir, index, op)
	}
}

// operation reports what the compiler refuses about one operation, and then what the server does.
func (r *runner) operation(m migration.Migration, dir ir.Direction, index int32, op ir.Operation) {
	err := r.errorFrom(m, index, op)

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

// shadowable reports a migration declaring a shadow table that the connected server cannot run as one.
func (r *runner) shadowable(m migration.Migration) {
	if r.opts.Capabilities == nil || r.opts.Online[m.Version] != migration.ShadowTable {
		return
	}

	reason := r.opts.Capabilities.Capture().Reason()
	if reason == "" && !r.opts.Shadows {
		reason = fmt.Sprintf("the %s driver does not perform shadow-table migration",
			r.opts.Capabilities.Driver())
	}

	if reason == "" {
		return
	}

	r.report.add(Problem{Migration: m, Check: CheckCapability, Direction: ir.Up,
		Detail: "it declares it runs through a shadow table, and " + reason})
}

// The lock check judges the up half alone, for invariant #7's reason: the inverse of every
// create_table is a drop_table, so judging a down would flag every project that ever made a table.
// It compiles the half as a run would -- the session's rewrites, and an operation on a table this
// migration creates left as declared -- so an operation is judged at the cost it would run at. An
// emulated operation is marked rather than expanded, being judged on its verdict alone, and a half
// that does not compile is left to the compile check, which has already reported it.

// lock reports every operation in a migration's up half that costs more than the ceiling allows.
func (r *runner) lock(m migration.Migration, ops ir.Operations) {
	if r.opts.Ceiling == "" {
		return
	}

	planner := r.opts.Planner
	planner.Capabilities, planner.Expander = r.opts.Capabilities, unexpanded{}

	compiled, err := planner.Compile(m, ir.Up, ops)
	if err != nil {
		return
	}

	for _, found := range engine.Exceeding(compiled, r.opts.Ceiling) {
		r.report.add(Problem{
			Migration: m,
			Check:     CheckLock,
			Direction: ir.Up,
			Operation: found.Operation,
			Kind:      found.Kind,
			Detail:    describeBreach(found, r.opts.Ceiling),
		})
	}
}

// describeBreach says what an operation costs a live database, against the ceiling it exceeds.
func describeBreach(found engine.Breach, ceiling capability.LockImpact) string {
	detail := fmt.Sprintf("costs a live database %s on %q, above safety.max_lock_impact (%s)",
		found.Lock, found.Subject, ceiling)

	if found.Rewrite != "" {
		detail += fmt.Sprintf(" even as rewritten by safety.rewrites.%s", found.Rewrite)
	}

	return detail
}

// unexpanded marks an emulated operation deferred for the lock check, which reads its verdict and never expands it.
type unexpanded struct{}

// Expand refuses, since the lock check judges an emulation by what the server declares it costs.
func (unexpanded) Expand(context.Context, ir.Operation) ([]driver.Statement, error) {
	return nil, driver.ErrNeedsSession
}

// errorFrom compiles one operation and reports what stopped it, discarding the statements.
func (r *runner) errorFrom(m migration.Migration, index int32, op ir.Operation) error {
	_, err := r.opts.Planner.Step(m, index, op)

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
