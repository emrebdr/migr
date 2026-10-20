package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// statementIndent is how far a statement sits under the operation it was compiled from.
const statementIndent = "          "

// newPlanCmd builds the `migr plan` subcommand.
func newPlanCmd(opts *options) *cobra.Command {
	narrow := &narrowing{}
	safe := &safety{}

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Print the statements a run would execute, without executing any of them",
		Long: "Compiles every migration a run would perform and prints the statements it would\n" +
			"send, grouped by migration and by operation. It takes the migration lock and reads\n" +
			"the history, but writes nothing at all — not even the history table.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPlan(cmd, opts, *narrow, *safe)
		},
	}

	narrow.selectors(cmd, "plan only the first N migrations the direction would act on")
	cmd.Flags().BoolVar(&narrow.down, "down", false, "plan the rollback rather than the pending run")
	cmd.Flags().BoolVar(&safe.shown, "safety", false,
		"print each operation's lock verdict, and which exceed safety.max_lock_impact")
	registerRequireSafe(cmd, &safe.required)
	registerRelease(cmd, opts)

	return cmd
}

// safety is what migr plan was asked about the lock axis: whether to print it, and whether to gate on it.
type safety struct {
	// shown prints each operation's lock verdict beside its support verdict.
	shown bool
	// required fails the plan, with exit code 5, where an operation exceeds safety.max_lock_impact.
	required bool
}

// printed reports whether the lock axis is printed, which gating on it implies.
func (s safety) printed() bool {
	return s.shown || s.required
}

// registerRequireSafe binds the gate flag, on the two commands that read it and no others.
func registerRequireSafe(cmd *cobra.Command, required *bool) {
	cmd.Flags().BoolVar(required, "require-safe", false,
		"exit 5 if any operation costs a live database more than safety.max_lock_impact allows")
}

// runPlan compiles what a direction would run against this database and prints every statement.
func runPlan(cmd *cobra.Command, opts *options, narrow narrowing, safe safety) (err error) {
	dir := narrow.direction()

	scope, err := narrow.scope(dir)
	if err != nil {
		return NewExitError(ExitValidation, err)
	}

	s, err := open(cmd.Context(), opts, scope)
	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, s.close(context.WithoutCancel(cmd.Context())))
	}()

	return report(cmd, opts, s, dir, safe)
}

// report compiles the direction and prints what it got, which a refusal partway leaves worth printing.
func report(cmd *cobra.Command, opts *options, s *session, dir ir.Direction, safe safety) error {
	previewed, planErr := s.preview(cmd.Context(), dir)

	if planErr != nil && len(previewed.Steps) == 0 {
		return planErr
	}

	warnUnassessed(opts, previewed)

	rendered := compiledOf(s, previewed)
	rendered.safety = safe.printed()

	written := writeResult(cmd.OutOrStdout(), opts, rendered)
	if planErr != nil || written != nil || !safe.required {
		return errors.Join(planErr, written)
	}

	return gate(s.ceiling, previewed)
}

// warnUnassessed logs every migration whose verdicts stayed at their worst because a table could not be read.
func warnUnassessed(opts *options, previewed engine.Previewed) {
	for _, planned := range previewed.Steps {
		if planned.Unassessed != nil {
			opts.log.Warn("a lock verdict stays at its worst: the table it depends on could not be read",
				"migration", planned.Plan.Migration.Filename(), "error", planned.Unassessed)
		}
	}
}

// gate fails a preview holding an operation that costs more than the ceiling, and passes any other.
func gate(ceiling capability.LockImpact, previewed engine.Previewed) error {
	failed := &engine.LockImpactError{Ceiling: ceiling}

	for _, planned := range previewed.Steps {
		found := engine.Exceeding(planned.Plan, ceiling)
		if len(found) > 0 {
			failed.Exceeded = append(failed.Exceeded, engine.Exceeded{Migration: planned.Plan.Migration,
				Direction: planned.Plan.Direction, Found: found})
		}
	}

	if len(failed.Exceeded) == 0 {
		return nil
	}

	return failed
}

// operation is one operation a migration emitted, beside what the driver would do with it.
type operation struct {
	// Index is the operation's place in the migration, counting from one.
	Index int32 `json:"index"`
	// Kind is the operation the migration emitted, before any driver saw it.
	Kind ir.Kind `json:"kind"`
	// Support is whether this engine performs the operation directly or achieves it another way.
	Support capability.Support `json:"support"`
	// Strategy is how an emulation is achieved, and nothing for an operation performed directly.
	Strategy string `json:"strategy,omitempty"`
	// Reason is why this server refuses the operation, and nothing for one it will perform.
	Reason string `json:"reason,omitempty"`
	// Destroys is what this operation would lose, and nothing for one that loses nothing.
	Destroys string `json:"destroys,omitempty"`
	// Rows says which rows a data operation writes, since its statements write them all at once.
	Rows string `json:"rows,omitempty"`
	// Refused reports an operation nothing permits, which migr up would stop at rather than run.
	Refused bool `json:"refused,omitempty"`
	// Deferred reports an operation this engine compiles only as it runs, which has no statements here.
	Deferred bool `json:"deferred,omitempty"`
	// Chunks is how a data-only migration's operation runs a keyed chunk at a time, absent otherwise.
	Chunks *chunks `json:"chunks,omitempty"`
	// Whole says why a data operation runs as one statement where it could have run in chunks.
	Whole string `json:"whole,omitempty"`
	// Statements are what it compiled to, in the order they would run.
	Statements []string `json:"statements"`
	// NoTransaction lists, counting from one, which of those run outside any transaction.
	NoTransaction []int `json:"no_transaction,omitempty"`
	// Rewrite is how the operation was rewritten into a safer sequence, absent where it runs as declared.
	Rewrite *rewriteDoc `json:"rewrite,omitempty"`
	// Lock is what the operation costs a live database as it would run, a rewrite's cost where it has one.
	Lock capability.LockImpact `json:"lock"`
	// Declared is what it would cost as declared, present only where a rewrite made it cheaper.
	Declared capability.LockImpact `json:"declared_lock,omitempty"`
	// RewritesTable reports an operation that writes every row of its table to new storage.
	RewritesTable bool `json:"rewrites_table,omitempty"`
	// Assessed reports a verdict the session read the table for, rather than one declared at its worst.
	Assessed bool `json:"assessed,omitempty"`
	// NewTable reports an operation on a table this migration creates, which blocks nobody whatever it takes.
	NewTable bool `json:"new_table,omitempty"`
	// OverCeiling reports an operation costing more than safety.max_lock_impact allows.
	OverCeiling bool `json:"over_ceiling,omitempty"`
}

// rewriteDoc is one operation a run performs as a safer sequence, as a plan renders it.
type rewriteDoc struct {
	// Name is the rewrite, the key under safety.rewrites that switches it off.
	Name driver.RewriteName `json:"name"`
	// Reason says in one sentence what the sequence avoids.
	Reason string `json:"reason"`
	// Plain is how many statements the operation compiles to as declared.
	Plain int `json:"declared_statements"`
}

// chunks is how one operation runs a keyed chunk at a time, as a preview could render it.
type chunks struct {
	// Table is the table whose primary key the chunks page through.
	Table string `json:"table"`
	// Key names that key's columns, empty where the table has none.
	Key []string `json:"key,omitempty"`
	// Rows is how many rows each chunk covers, as migr.yaml paces it.
	Rows int `json:"rows"`
	// Pause is how long a run waits between two chunks, as migr.yaml paces it.
	Pause string `json:"pause,omitempty"`
	// ReplicaLag is the lag above which a run holds the next chunk back, absent where none is read.
	ReplicaLag string `json:"replica_lag_threshold,omitempty"`
	// Refusal is why migr up would refuse the operation, or why this preview could not render it.
	Refusal string `json:"refusal,omitempty"`
}

// direct is one statement a migration sends itself, which this preview held rather than ran.
type direct struct {
	// SQL is the statement text, as the migration wrote it.
	SQL string `json:"sql"`
	// Args are the parameters bound to it, which are sent separately and never interpolated.
	Args []string `json:"args,omitempty"`
}

// compiledMigration is one migration a plan would perform, compiled for this engine and never run.
type compiledMigration struct {
	// Ordinal is the position within the batch it would be recorded under.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Direct are the statements it runs itself, before any operation and outside every transaction.
	Direct []direct `json:"direct,omitempty"`
	// Operations are what it emitted, each beside the statements it became.
	Operations []operation `json:"operations"`
	// Unrestorable is what its record and this database's own schema disagree about, where judged.
	Unrestorable []disagreed `json:"unrestorable,omitempty"`
	// Resume is where a stopped chunked run committed up to, which this one would carry on after.
	Resume *checkpoint `json:"resume,omitempty"`
	// Parts is how many parts it runs as where a statement in it cannot run inside a transaction.
	Parts int `json:"parts,omitempty"`
	// Unassessed is why its verdicts stayed at their worst where a table they depend on could not be read.
	Unassessed string `json:"unassessed,omitempty"`
}

// checkpoint is how far a chunked operation got, as the run that stopped committed it.
type checkpoint struct {
	// Operation is the operation it stopped in, counting from one.
	Operation int32 `json:"operation"`
	// Chunks is how many of that operation's chunks committed.
	Chunks int64 `json:"chunks"`
	// Written is how many rows those chunks changed or inserted.
	Written int64 `json:"written"`
	// Cursor is the key of the last row the last of them covered.
	Cursor []string `json:"cursor"`
}

// checkpointOf renders a checkpoint, and nothing where there is none.
func checkpointOf(cp *history.Checkpoint) *checkpoint {
	if cp == nil {
		return nil
	}

	cursor := cp.Cursor
	if cursor == nil {
		cursor = []string{}
	}

	return &checkpoint{Operation: cp.Step + 1, Chunks: cp.Chunks, Written: cp.Written, Cursor: cursor}
}

// disagreed is one operation whose record of the schema this database contradicts, as fields.
type disagreed struct {
	// Operation is its 1-based place in the half the migration emitted.
	Operation int32 `json:"operation"`
	// Kind is the operation at fault.
	Kind ir.Kind `json:"kind"`
	// Subject is the object it acts on, named the way the operation names it.
	Subject string `json:"subject"`
	// Sense is which way it and the live schema disagree: stated or implied.
	Sense engine.Sense `json:"sense"`
	// Detail states the disagreement in terms the migration's author can act on.
	Detail string `json:"detail"`
}

// disagreementsOf renders what a migration's record and the live schema disagree about.
func disagreementsOf(found []engine.Disagreement) []disagreed {
	if len(found) == 0 {
		return nil
	}

	rendered := make([]disagreed, 0, len(found))
	for _, one := range found {
		rendered = append(rendered, disagreed{
			Operation: one.Operation, Kind: one.Kind, Subject: one.Subject,
			Sense: one.Sense, Detail: one.Detail,
		})
	}

	return rendered
}

// compiledPlan is what one direction would send to one database, in the form --json reports it.
type compiledPlan struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Direction is which way the plan was compiled.
	Direction ir.Direction `json:"direction"`
	// Recorded reports whether this database has a migration history table at all.
	Recorded bool `json:"history_table"`
	// Batch is what these runs would be recorded under, had they run.
	Batch int64 `json:"batch,omitempty"`
	// Migrations is one entry per migration that would run, in the order they would run.
	Migrations []compiledMigration `json:"migrations"`
	// Skipped is one entry per migration this direction would not run.
	Skipped []left `json:"skipped,omitempty"`
	// Statements is how many statements the whole plan compiles to.
	Statements int `json:"statements"`
	// Direct is how many statements the migrations would run themselves, outside the plan.
	Direct int `json:"direct_statements"`
	// Chunked is how many operations run a keyed chunk at a time, whose statements are not counted.
	Chunked int `json:"chunked_operations"`
	// Ceiling is safety.max_lock_impact, which every operation's lock is judged against.
	Ceiling capability.LockImpact `json:"max_lock_impact"`
	// Held is the step the run would stop at because it waits for a later release.
	Held *heldDoc `json:"held,omitempty"`
	// Unreleased is why migr up would refuse this run for naming no release, absent where it would not.
	Unreleased string `json:"unreleased,omitempty"`
	// safety prints the lock axis in the text rendering, which a document always carries.
	safety bool
}

// compiledOf renders what a direction would send, asking the session what it makes of each operation.
func compiledOf(s *session, previewed engine.Previewed) compiledPlan {
	rendered := compiledPlan{
		Database:   s.database,
		Direction:  previewed.Direction,
		Recorded:   previewed.Recorded,
		Batch:      previewed.Batch,
		Migrations: make([]compiledMigration, 0, len(previewed.Steps)),
		Skipped:    leftBehind(previewed.Skipped),
		Ceiling:    s.ceiling,
		Held:       heldOf(previewed.Held),
		Unreleased: describeUnreleased(previewed.Unreleased),
	}

	pace := paceOf(s.options)

	for _, step := range previewed.Steps {
		rendered.Migrations = append(rendered.Migrations, heldOnLag(migrationOf(s.ceiling,
			s.options.Allow, pace, step), lagOf(s.options)))
		rendered.Direct += len(step.Direct)

		if chunked := step.Plan.Chunked(); chunked > 0 {
			rendered.Chunked += int(chunked)

			continue
		}

		rendered.Statements += int(step.Plan.Len())
	}

	return rendered
}

// migrationOf renders one migration's operations, each with the verdict this engine gives it.
func migrationOf(ceiling capability.LockImpact, allowed engine.Allowance, pace engine.Steady,
	planned engine.Planned) compiledMigration {
	over := breachedAt(engine.Exceeding(planned.Plan, ceiling))

	ops := make([]operation, 0, len(planned.Plan.Steps))
	for i, step := range planned.Plan.Steps {
		op := operationOf(allowed, step)
		op.OverCeiling = over[step.Index]
		chunked(&op, pace, planned, i)
		ops = append(ops, op)
	}

	return compiledMigration{
		Ordinal:      planned.Ordinal,
		Version:      planned.Plan.Migration.Version,
		File:         planned.Plan.Migration.Filename(),
		Direct:       directOf(planned.Direct),
		Operations:   ops,
		Unrestorable: disagreementsOf(planned.Unrestorable),
		Resume:       checkpointOf(planned.Resume),
		Parts:        partsOf(planned.Plan),
		Unassessed:   describeUnassessed(planned.Unassessed),
	}
}

// breachedAt indexes the operations over the ceiling by their place in the migration.
func breachedAt(found []engine.Breach) map[int32]bool {
	over := make(map[int32]bool, len(found))
	for _, b := range found {
		over[b.Operation] = true
	}

	return over
}

// describeUnassessed renders why a migration's verdicts stayed at their worst, and nothing where none did.
func describeUnassessed(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// partsOf counts the parts a migration runs as, and nothing for one that is one transaction.
func partsOf(compiled plan.Plan) int {
	for _, step := range compiled.Steps {
		for _, stmt := range step.Statements {
			if stmt.NoTransaction {
				return engine.Parts(compiled)
			}
		}
	}

	return 0
}

// paceOf reads how a session's runs size and space their chunks, which a Steady pacer states.
func paceOf(options engine.Options) engine.Steady {
	switch pacer := options.Pacer.(type) {
	case engine.Steady:
		return pacer
	case engine.Lagged:
		return pacer.Steady
	default:
		return engine.Steady{Rows: options.Planner.ChunkRows}
	}
}

// heldOnLag says, on every operation of a migration that runs in chunks, the lag that holds them back.
func heldOnLag(m compiledMigration, lag time.Duration) compiledMigration {
	if lag <= 0 {
		return m
	}

	for i := range m.Operations {
		if m.Operations[i].Chunks != nil {
			m.Operations[i].Chunks.ReplicaLag = lag.String()
		}
	}

	return m
}

// lagOf is the replica lag a run holds chunks back above, and nothing for a run that reads none.
func lagOf(options engine.Options) time.Duration {
	if lagged, ok := options.Pacer.(engine.Lagged); ok {
		return lagged.Threshold
	}

	return 0
}

// chunked renders how a data operation runs: in chunks with one chunk's statements, or why as one.
func chunked(op *operation, pace engine.Steady, planned engine.Planned, at int) {
	step := planned.Plan.Steps[at]
	if !slices.Contains(ir.DataKinds(), step.Op.Kind()) {
		return
	}

	if !step.Chunked {
		op.Whole = describeWhole(pace.Rows)

		return
	}

	shape := shapeFor(planned.Chunks, step.Index)
	op.Chunks = &chunks{Table: shape.Table, Key: shape.Key, Rows: pace.Rows}

	if pace.Pause > 0 {
		op.Chunks.Pause = pace.Pause.String()
	}

	op.Statements = []string{}

	if shape.Err != nil {
		op.Chunks.Refusal = shape.Err.Error()

		return
	}

	for _, stmt := range shape.Statements {
		op.Statements = append(op.Statements, stmt.SQL)
	}
}

// shapeFor finds the rendered chunk of one operation, which a preview renders for every chunked one.
func shapeFor(shapes []engine.Chunking, index int32) engine.Chunking {
	for _, shape := range shapes {
		if shape.Index == index {
			return shape
		}
	}

	return engine.Chunking{Index: index, Err: errors.New("the preview rendered no chunk of it")}
}

// describeWhole says why a data operation runs as one statement over the whole table.
func describeWhole(rows int) string {
	if rows < 1 {
		return "runs as one statement over the whole table: safety.chunk_rows is 0"
	}

	return "runs as one statement in the migration's transaction, because the migration also " +
		"changes the schema -- put it in a migration of its own to run it in chunks"
}

// directOf renders the statements a migration sends itself, which host.db.exec held under plan mode.
func directOf(held []driver.Query) []direct {
	if len(held) == 0 {
		return nil
	}

	statements := make([]direct, 0, len(held))
	for _, q := range held {
		statements = append(statements, direct{SQL: q.SQL, Args: q.Args})
	}

	return statements
}

// operationOf renders one operation, its verdict, and the statements the driver compiled it to.
func operationOf(allowed engine.Allowance, step plan.Step) operation {
	verdict := step.Verdict

	stmts := make([]string, 0, len(step.Statements))

	var standalone []int

	for i, stmt := range step.Statements {
		stmts = append(stmts, stmt.SQL)

		if stmt.NoTransaction {
			standalone = append(standalone, i+1)
		}
	}

	return operation{
		Index:         step.Index,
		Kind:          step.Op.Kind(),
		Support:       verdict.Support(),
		Strategy:      verdict.Strategy(),
		Reason:        verdict.Reason(),
		Destroys:      destroyedSubject(step.Op),
		Rows:          rowsOf(step.Op),
		Refused:       !allowed.Permits(step.Op),
		Deferred:      step.Deferred,
		Statements:    stmts,
		NoTransaction: standalone,
		Rewrite:       rewriteOf(step.Rewrite),
		Lock:          step.Lock(),
		Declared:      declaredOf(step),
		RewritesTable: step.Lock() == capability.TableRewrite,
		Assessed:      step.Assessed,
		NewTable:      step.Fresh,
	}
}

// declaredOf is what a rewritten operation would have cost as declared, and nothing where that is its cost.
func declaredOf(step plan.Step) capability.LockImpact {
	if step.Rewrite == nil || step.Fresh || step.Verdict.Lock() == step.Lock() {
		return ""
	}

	return step.Verdict.Lock()
}

// rewriteOf renders how a step was rewritten, and nothing for one that runs as declared.
func rewriteOf(r *plan.Rewritten) *rewriteDoc {
	if r == nil {
		return nil
	}

	return &rewriteDoc{Name: r.Name, Reason: r.Reason, Plain: r.Plain}
}

// rowsOf says which rows a data operation writes, and nothing for one that writes schema.
func rowsOf(op ir.Operation) string {
	switch o := op.(type) {
	case ir.DataCopy:
		return fmt.Sprintf("copies every row of %q into %q, %s", o.From, o.To,
			describeCopied(len(o.Columns)))
	case ir.DataBackfill:
		return fmt.Sprintf("fills the NULLs of %q with %s, and no other row",
			o.Table+"."+o.Column, describeValue(o.Value))
	case ir.DataTransform:
		return fmt.Sprintf("rewrites %q in every row, from %q through %s", o.Table+"."+o.Column,
			o.From, o.Function)
	default:
		return ""
	}
}

// describeCopied counts the columns a copy writes, with the right noun.
func describeCopied(n int) string {
	if n == 1 {
		return "1 column"
	}

	return fmt.Sprintf("%d columns", n)
}

// describeValue names what a backfill writes, a literal quoted and a function by name.
func describeValue(value *ir.Default) string {
	if value.Literal != nil {
		return fmt.Sprintf("%q", *value.Literal)
	}

	return string(value.Function) + "()"
}

// destroyedSubject names what an operation would lose, and nothing for raw, whose subject is its SQL.
func destroyedSubject(op ir.Operation) string {
	if !op.Destructive() || op.Kind() == ir.KindRaw {
		return ""
	}

	return engine.Subject(op)
}

// String renders every statement the plan would send, grouped by migration and by operation.
func (p compiledPlan) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "plan — database %q, %s%s\n", p.Database, p.Direction, describeBatch(p.Batch))

	if !p.Recorded {
		fmt.Fprintf(&b, "\nthis database has no migration history table yet; migr %s would create it\n",
			p.Direction)
	}

	if len(p.Migrations) == 0 {
		fmt.Fprintf(&b, "\nnothing to do%s\n", describeSkipped(p.Skipped))
		p.Held.write(&b)
		writeUnreleased(&b, p.Unreleased)

		return b.String()
	}

	for _, m := range p.Migrations {
		m.write(&b, p.shown())
	}

	fmt.Fprintf(&b, "\n%s, %s, %s%s. nothing was executed and nothing was recorded.\n",
		describeMigrations(len(p.Migrations)), describeOperations(p.countOperations()),
		p.describeCompiled(), describeSkipped(p.Skipped))

	p.writeDirectNote(&b)
	p.writeSplitNote(&b)
	p.writeRewriteNote(&b)
	p.writeChunkedNote(&b)
	p.writeDeferredNote(&b)
	p.writeUnsupportedNote(&b)
	p.writeUnrestorableNote(&b)
	p.writeRefusedNote(&b)
	p.writeSafetyNote(&b)
	p.Held.write(&b)
	writeUnreleased(&b, p.Unreleased)

	return b.String()
}

// shown is the ceiling the text rendering marks operations against, and nothing where it prints no lock axis.
func (p compiledPlan) shown() capability.LockImpact {
	if !p.safety {
		return ""
	}

	return p.Ceiling
}

// writeSafetyNote says where the lock verdicts came from and how many exceed the ceiling.
func (p compiledPlan) writeSafetyNote(b *strings.Builder) {
	if !p.safety {
		return
	}

	fmt.Fprintf(b, "\nlock verdicts are what this server declares for each operation, at the cost a "+
		"rewrite runs it at where one applies. The first migration's are read against its tables "+
		"where this driver can; every later one's are declared at their worst, since a migration "+
		"before it may change the table first. An operation on a table its own migration creates "+
		"is lock_free, nothing being able to wait on it. unknown, which a raw statement is, counts "+
		"as table_rewrite.\n")

	over := p.countOverCeiling()
	if over == 0 {
		fmt.Fprintf(b, "no operation exceeds safety.max_lock_impact (%s).\n", p.Ceiling)

		return
	}

	fmt.Fprintf(b, "marked %s above: %s safety.max_lock_impact (%s), so migr plan --require-safe "+
		"exits 5 on this plan.\n", overCeilingMarker, describeExceeding(over), p.Ceiling)
}

// countOverCeiling counts the operations costing more than safety.max_lock_impact allows.
func (p compiledPlan) countOverCeiling() int {
	n := 0

	for _, m := range p.Migrations {
		for _, op := range m.Operations {
			if op.OverCeiling {
				n++
			}
		}
	}

	return n
}

// describeExceeding counts the operations over the ceiling with a verb that agrees.
func describeExceeding(n int) string {
	if n == 1 {
		return "1 operation exceeds"
	}

	return fmt.Sprintf("%d operations exceed", n)
}

// overCeilingMarker sits beneath an operation that costs more than safety.max_lock_impact allows.
const overCeilingMarker = "\u25b2"

// describeCompiled counts the statements, saying so where that is not every statement a run sends.
func (p compiledPlan) describeCompiled() string {
	if p.Statements == 0 && p.Chunked > 0 {
		return describeOperations(p.Chunked) + " run in chunks"
	}

	described := describeStatements(p.Statements)
	if p.countDeferred() > 0 {
		described += " from the operations it could compile"
	}

	if p.Chunked > 0 {
		described += fmt.Sprintf(", and %s run in chunks", describeOperations(p.Chunked))
	}

	return described
}

// writeRewriteNote names every rewrite this plan applies and the key that switches each one off.
func (p compiledPlan) writeRewriteNote(b *strings.Builder) {
	used := map[driver.RewriteName]int{}

	for _, m := range p.Migrations {
		for _, op := range m.Operations {
			if op.Rewrite != nil {
				used[op.Rewrite.Name]++
			}
		}
	}

	if len(used) == 0 {
		return
	}

	keys := make([]string, 0, len(used))
	total := 0

	for _, name := range driver.RewriteNames() {
		if used[name] > 0 {
			keys = append(keys, fmt.Sprintf("%s: off", rewriteKey(name)))
			total += used[name]
		}
	}

	fmt.Fprintf(b, "\nmarked \u21b3 above: %s this engine performs as a safer sequence than was declared, "+
		"so the statements shown are what migr %s sends. Each runs as declared instead with %s, "+
		"or every one with safety.auto_rewrite: false.\n", describeUnpermitted(total), p.Direction,
		strings.Join(keys, ", "))
}

// writeSplitNote says what a statement outside any transaction does to the migration it is in.
func (p compiledPlan) writeSplitNote(b *strings.Builder) {
	split := 0

	for _, m := range p.Migrations {
		if m.Parts > 0 {
			split++
		}
	}

	if split == 0 {
		return
	}

	fmt.Fprintf(b, "\nmarked %s above: statements that cannot run inside a transaction, in %s. "+
		"migr %s runs each on its own and the statements around it in transactions of their "+
		"own, recording how far it got after each part commits -- a failure partway leaves the "+
		"migration dirty, and a statement outside a transaction that gives up on a lock is not "+
		"tried again.\n", noTransactionMarker, describeMigrations(split), p.Direction)
}

// writeChunkedNote says what the statements of a chunked operation are, and what a stopped one does.
func (p compiledPlan) writeChunkedNote(b *strings.Builder) {
	if p.Chunked == 0 {
		return
	}

	fmt.Fprintf(b, "\nrun in chunks above: each chunk is its own transaction, committed with a checkpoint "+
		"saying how far it got, and the statements shown are one chunk between two keys with "+
		"every bound argument a placeholder. A run that stops partway leaves the migration dirty, "+
		"and the next migr up carries on after the last committed chunk.\n")
}

// writeDeferredNote says which operations this plan could not compile, and why that is not a failure.
func (p compiledPlan) writeDeferredNote(b *strings.Builder) {
	deferred := p.countDeferred()
	if deferred == 0 {
		return
	}

	fmt.Fprintf(b, "\nmarked \u2192 above: %s this engine compiles against the database "+
		"immediately before each one runs, so their statements are rendered from the schema the "+
		"operations ahead of them leave. A preview applies nothing, so only the first operation of "+
		"the first migration can be compiled exactly, and it is -- migr %s expands each of the "+
		"rest as it reaches it.\n", describeUnpermitted(deferred), p.Direction)
}

// countDeferred returns how many operations in the whole plan are compiled only as they run.
func (p compiledPlan) countDeferred() int {
	total := 0

	for _, m := range p.Migrations {
		for _, op := range m.Operations {
			if op.Deferred {
				total++
			}
		}
	}

	return total
}

// writeUnsupportedNote says what this server will not perform, since a report finds without failing.
func (p compiledPlan) writeUnsupportedNote(b *strings.Builder) {
	unsupported := p.countUnsupported()
	if unsupported == 0 {
		return
	}

	fmt.Fprintf(b, "\nmarked × above: %s this server declares it cannot perform, with the reason "+
		"beneath each. migr %s refuses the migration they are in before sending anything; this plan "+
		"prints what it would have sent. The verdict is resolved against the connected server, so "+
		"another server may answer differently.\n", describeUnpermitted(unsupported), p.Direction)
}

// writeUnrestorableNote says what this database's schema contradicts, since a report finds without failing.
func (p compiledPlan) writeUnrestorableNote(b *strings.Builder) {
	found := p.countUnrestorable()
	if found == 0 {
		return
	}

	fmt.Fprintf(b, "\nmarked ~ above: %s whose record of the schema this database contradicts, "+
		"which migr %s refuses the migration they are in over. Only the first migration is judged: "+
		"a preview applies nothing, so the schema every later one would run against is the one its "+
		"predecessors leave.\n", describeUnpermitted(found), p.Direction)
}

// countUnrestorable returns how many disagreements the whole plan found.
func (p compiledPlan) countUnrestorable() int {
	total := 0

	for _, m := range p.Migrations {
		total += len(m.Unrestorable)
	}

	return total
}

// countUnsupported returns how many operations in the whole plan this server declares it cannot perform.
func (p compiledPlan) countUnsupported() int {
	total := 0

	for _, m := range p.Migrations {
		for _, op := range m.Operations {
			if op.Reason != "" {
				total++
			}
		}
	}

	return total
}

// writeRefusedNote says what migr up would refuse, since a report finds a problem without failing.
func (p compiledPlan) writeRefusedNote(b *strings.Builder) {
	refused := p.countRefused()
	if refused == 0 {
		return
	}

	fmt.Fprintf(b, "\nmarked ? above: %s that nothing has permitted. migr %s refuses the migration "+
		"they are in; this plan reports them. Permit them for one run with --allow-destructive, or "+
		"for this project by naming their kinds in migr.yaml under validation.allow_destructive.\n",
		describeUnpermitted(refused), p.Direction)
}

// countRefused returns how many operations in the whole plan destroy what nothing has permitted.
func (p compiledPlan) countRefused() int {
	total := 0

	for _, m := range p.Migrations {
		for _, op := range m.Operations {
			if op.Refused {
				total++
			}
		}
	}

	return total
}

// describeUnpermitted counts the operations a run would be refused over, with the right noun.
func describeUnpermitted(n int) string {
	if n == 1 {
		return "1 operation"
	}

	return fmt.Sprintf("%d operations", n)
}

// writeDirectNote says what a preview could not hold back, since migr up commits those on their own.
func (p compiledPlan) writeDirectNote(b *strings.Builder) {
	if p.Direct == 0 {
		return
	}

	fmt.Fprintf(b, "\nmarked ! above: %s sent through the escape hatch rather than the plan. "+
		"migr %s commits each on its own, before the transaction its migration's operations "+
		"run in, and a later failure does not undo it.\n", describeStatements(p.Direct), p.Direction)
}

// countOperations returns how many operations the whole plan emitted, across every migration.
func (p compiledPlan) countOperations() int {
	total := 0
	for _, m := range p.Migrations {
		total += len(m.Operations)
	}

	return total
}

// write renders one migration's heading, what it runs itself, and every operation under it.
func (m compiledMigration) write(b *strings.Builder, ceiling capability.LockImpact) {
	fmt.Fprintf(b, "\n  %d  %s\n", m.Ordinal, m.File)

	m.writeDirect(b)
	m.writeResume(b)
	m.writeParts(b)
	m.writeNothing(b)

	for _, op := range m.Operations {
		op.write(b, ceiling)
	}

	m.writeUnrestorable(b)
}

// writeParts says that a migration runs in parts, each committing on its own, where one does.
func (m compiledMigration) writeParts(b *strings.Builder) {
	switch m.Parts {
	case 0:
		return
	case 1:
		fmt.Fprintf(b, "       \u2016  runs outside any transaction: every statement commits on its own\n")
	default:
		fmt.Fprintf(b, "       \u2016  runs in %d parts, each committing on its own, split around the "+
			"statements marked %s\n", m.Parts, noTransactionMarker)
	}
}

// noTransactionMarker sits above a statement that runs outside any transaction.
const noTransactionMarker = "[no transaction]"

// writeNothing says that a down emitting no operations undoes nothing, which only its author can say.
func (m compiledMigration) writeNothing(b *strings.Builder) {
	if len(m.Operations) > 0 {
		return
	}

	fmt.Fprintf(b, "       \u00b7  nothing to undo -- its down emits no operations, and the "+
		"rollback is still recorded\n")
}

// writeResume says where a stopped chunked run committed up to, which this one carries on after.
func (m compiledMigration) writeResume(b *strings.Builder) {
	if m.Resume == nil {
		return
	}

	fmt.Fprintf(b, "       \u21bb  resumes operation %d after key (%s): the run that stopped "+
		"committed %s and %s of it\n", m.Resume.Operation, strings.Join(m.Resume.Cursor, ", "),
		describeChunks(m.Resume.Chunks), describeWritten(m.Resume.Written))
}

// describeChunks counts chunks with the right noun.
func describeChunks(n int64) string {
	if n == 1 {
		return "1 chunk"
	}

	return fmt.Sprintf("%d chunks", n)
}

// describeWritten counts written rows with the right noun.
func describeWritten(n int64) string {
	if n == 1 {
		return "1 row"
	}

	return fmt.Sprintf("%d rows", n)
}

// writeUnrestorable renders what this migration's record and the live schema disagree about.
func (m compiledMigration) writeUnrestorable(b *strings.Builder) {
	for _, one := range m.Unrestorable {
		fmt.Fprintf(b, "       ~  operation %d (%s) on %q: %s\n",
			one.Operation, one.Kind, one.Subject, one.Detail)
	}
}

// writeDirect renders what a migration sends itself, above the operations because that is when it runs.
func (m compiledMigration) writeDirect(b *strings.Builder) {
	if len(m.Direct) == 0 {
		return
	}

	fmt.Fprintf(b, "       !  %s this migration runs itself, outside the plan and outside "+
		"its transaction\n", describeStatements(len(m.Direct)))

	for _, statement := range m.Direct {
		statement.write(b)
	}
}

// write renders one statement a migration sends itself, and says how many parameters it binds.
func (d direct) write(b *strings.Builder) {
	fmt.Fprintf(b, "%s\n", indented(d.SQL))

	if len(d.Args) > 0 {
		fmt.Fprintf(b, "%s\n", indented(fmt.Sprintf("-- %s bound", describeArguments(len(d.Args)))))
	}
}

// describeArguments counts bound parameters with the right noun.
func describeArguments(n int) string {
	if n == 1 {
		return "1 argument"
	}

	return fmt.Sprintf("%d arguments", n)
}

// write renders one operation's heading, why this server refuses it, and what it compiled to.
func (o operation) write(b *strings.Builder, ceiling capability.LockImpact) {
	fmt.Fprintf(b, "       %d  %s  %s%s%s\n", o.Index, o.Kind, describeVerdict(o), o.describeLock(ceiling),
		o.describeDestroys())

	o.writeOverCeiling(b, ceiling)
	o.writeReason(b)
	o.writeRewrite(b)
	o.writeRows(b)
	o.writeChunks(b)
	o.writeDeferred(b)

	for i, stmt := range o.Statements {
		if slices.Contains(o.NoTransaction, i+1) {
			fmt.Fprintf(b, "%s\n", indented(noTransactionMarker))
		}

		fmt.Fprintf(b, "%s\n", indented(stmt))
	}
}

// describeLock renders what the operation costs a live database, where the lock axis is printed.
func (o operation) describeLock(ceiling capability.LockImpact) string {
	if ceiling == "" || o.Support == capability.SupportUnsupported {
		return ""
	}

	described := "  " + o.Lock.String()

	switch {
	case o.NewTable:
		described += " (on a table this migration creates)"
	case o.Declared != "":
		described += fmt.Sprintf(" (declared %s, rewritten)", o.Declared)
	case o.Assessed:
		described += " (read from the table)"
	}

	if o.RewritesTable {
		described += " -- rewrites the table"
	}

	return described
}

// writeOverCeiling marks an operation that costs more than safety.max_lock_impact allows.
func (o operation) writeOverCeiling(b *strings.Builder, ceiling capability.LockImpact) {
	if ceiling == "" || !o.OverCeiling {
		return
	}

	fmt.Fprintf(b, "%s\n", indented(fmt.Sprintf("%s over safety.max_lock_impact (%s)", overCeilingMarker,
		ceiling)))
}

// writeRewrite says that one declared operation runs as a safer sequence, and which key switches it off.
func (o operation) writeRewrite(b *strings.Builder) {
	if o.Rewrite == nil {
		return
	}

	fmt.Fprintf(b, "%s\n", indented(fmt.Sprintf("\u21b3 rewritten (%s): 1 operation, %s -- %s",
		rewriteKey(o.Rewrite.Name), describeReshaped(len(o.Statements), o.Rewrite.Plain),
		o.Rewrite.Reason)))
}

// describeReshaped says how many statements a rewritten operation became, against how many it was declared as.
func describeReshaped(now, declared int) string {
	if now == declared {
		return describeStatements(now) + ", each sent differently than declared"
	}

	return fmt.Sprintf("%s instead of %d", describeStatements(now), declared)
}

// rewriteKey is the migr.yaml key that switches a rewrite off.
func rewriteKey(name driver.RewriteName) string {
	return "safety.rewrites." + string(name)
}

// writeRows says which rows a data operation writes, above the one statement that writes them.
func (o operation) writeRows(b *strings.Builder) {
	if o.Rows == "" {
		return
	}

	fmt.Fprintf(b, "%s\n", indented("rows: "+o.Rows))
}

// writeChunks says how a data operation runs: a chunk at a time and by which key, or as one statement.
func (o operation) writeChunks(b *strings.Builder) {
	if o.Whole != "" {
		fmt.Fprintf(b, "%s\n", indented(o.Whole))
	}

	if o.Chunks == nil {
		return
	}

	fmt.Fprintf(b, "%s\n", indented("chunks: "+o.Chunks.describe()))

	if o.Chunks.Refusal != "" {
		fmt.Fprintf(b, "%s\n", indented("× "+o.Chunks.Refusal))
	}
}

// describe says how large each chunk is, what it pages by, and how long a run waits between two.
func (c chunks) describe() string {
	described := fmt.Sprintf("%s at a time", describeRows(c.Rows))

	if len(c.Key) > 0 {
		described += fmt.Sprintf(" by %q (%s)", c.Table, strings.Join(c.Key, ", "))
	}

	if c.Pause != "" {
		described += ", pausing " + c.Pause + " between them"
	}

	if c.ReplicaLag != "" {
		described += ", held back while a replica trails by more than " + c.ReplicaLag
	}

	return described + ", each committed on its own"
}

// describeRows counts rows with the right noun.
func describeRows(n int) string {
	if n == 1 {
		return "1 row"
	}

	return fmt.Sprintf("%d rows", n)
}

// writeDeferred stands in for the statements of an operation nothing here can compile yet.
func (o operation) writeDeferred(b *strings.Builder) {
	if !o.Deferred {
		return
	}

	fmt.Fprintf(b, "%s\n", indented("\u2192 compiled against the database when it runs"))
}

// writeReason renders why this server refuses an operation, above the statements it refuses.
func (o operation) writeReason(b *strings.Builder) {
	if o.Reason == "" {
		return
	}

	fmt.Fprintf(b, "%s\n", indented("× "+o.Reason))
}

// describeDestroys says what an operation would lose, and marks the ones nothing has permitted.
func (o operation) describeDestroys() string {
	switch {
	case o.Refused && o.Destroys == "":
		return "  ? migr cannot read what this does"
	case o.Refused:
		return fmt.Sprintf("  ? destroys %q", o.Destroys)
	case o.Destroys != "":
		return fmt.Sprintf("  destroys %q, permitted", o.Destroys)
	}

	return ""
}

// describeVerdict says what this engine makes of an operation, naming the strategy of an emulation.
func describeVerdict(o operation) string {
	if o.Strategy == "" {
		return o.Support.String()
	}

	return o.Support.String() + ": " + o.Strategy
}

// indented lays one statement out under its operation, keeping a multi-line one in its column.
func indented(sql string) string {
	return statementIndent + strings.ReplaceAll(sql, "\n", "\n"+statementIndent)
}

// describeOperations counts emitted operations with the right noun.
func describeOperations(n int) string {
	if n == 1 {
		return "1 operation"
	}

	return fmt.Sprintf("%d operations", n)
}
