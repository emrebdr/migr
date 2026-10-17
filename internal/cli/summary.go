package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// stamp renders a recorded instant in a table, in UTC because a history row is read anywhere.
const stamp = "2006-01-02 15:04:05 UTC"

// elapsed is a run's duration, rendered as milliseconds for a script and as itself for a person.
type elapsed time.Duration

// MarshalJSON renders the duration in milliseconds, which is the unit a script wants to compare.
func (e elapsed) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(e).Seconds() * 1000)
}

// String renders the duration the way Go prints one, rounded to something a person reads.
func (e elapsed) String() string {
	return time.Duration(e).Round(time.Microsecond).String()
}

// ran is one migration an invocation performed, and what it cost.
type ran struct {
	// Ordinal is its position within the batch, counting from one.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp, which is what --to and a history row name.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Statements is how many statements it executed.
	Statements int32 `json:"statements"`
	// Chunks is how many chunks it ran a data-only migration in, absent where it ran statements.
	Chunks int64 `json:"chunks,omitempty"`
	// Written is how many rows those chunks changed or inserted.
	Written int64 `json:"written,omitempty"`
	// Resumed reports a run that carried on from the checkpoint an earlier run committed.
	Resumed bool `json:"resumed,omitempty"`
	// Retries is how many times a statement or chunk was tried again after giving up on a lock.
	Retries int `json:"lock_retries,omitempty"`
	// Duration is how long the run took.
	Duration elapsed `json:"duration_ms"`
}

// ranOf renders one run an invocation performed.
func ranOf(run engine.Run) ran {
	return ran{
		Ordinal:    run.Ordinal,
		Version:    run.Migration.Version,
		File:       run.Migration.Filename(),
		Statements: run.Statements,
		Chunks:     run.Chunks,
		Written:    run.Written,
		Resumed:    run.Resumed,
		Retries:    run.Retries,
		Duration:   elapsed(run.Duration),
	}
}

// describeWork says what a run executed: its statements, or the chunks a data-only one ran in.
func describeWork(run ran) string {
	return describeExecuted(run) + describeRetries(run.Retries)
}

// describeExecuted says what a run executed: its statements, or the chunks a data-only one ran in.
func describeExecuted(run ran) string {
	if run.Chunks == 0 {
		return describeStatements(int(run.Statements))
	}

	described := fmt.Sprintf("%s, %s written", describeChunks(run.Chunks),
		describeWritten(run.Written))
	if run.Resumed {
		described += ", resumed"
	}

	return described
}

// describeRetries says how often a run was tried again after giving up on a lock, and nothing if never.
func describeRetries(retries int) string {
	switch retries {
	case 0:
		return ""
	case 1:
		return ", after 1 lock retry"
	default:
		return fmt.Sprintf(", after %d lock retries", retries)
	}
}

// planned is one migration an invocation would have performed, compiled but never executed.
type planned struct {
	// Ordinal is the position within the batch it would have been recorded under.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Statements is how many statements it would execute, of the operations anything here compiled.
	Statements int `json:"statements"`
	// Deferred is how many of its operations this engine compiles only as each one runs.
	Deferred int `json:"deferred,omitempty"`
	// Chunked is how many of its operations run in chunks, whose statements are not counted.
	Chunked int `json:"chunked,omitempty"`
	// Parts is how many parts it runs as where a statement in it cannot run inside a transaction.
	Parts int `json:"parts,omitempty"`
	// Refused is how many of its operations destroy what nothing permits, which migr up stops at.
	Refused int `json:"refused,omitempty"`
	// Unsupported is how many this server declares it cannot perform, which migr up also stops at.
	Unsupported int `json:"unsupported,omitempty"`
}

// left is one migration an invocation did not act on, and the state it was left in.
type left struct {
	// Version is the migration's UTC timestamp, which a version only the database carries still has.
	Version string `json:"version"`
	// File is the filename it was found in, empty when only the history names it.
	File string `json:"file,omitempty"`
	// State is what reconciliation resolved it to.
	State string `json:"state"`
}

// runSummary is what one invocation did against one database, in the form --json reports it.
type runSummary struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Direction is which way the invocation ran.
	Direction ir.Direction `json:"direction"`
	// Batch is what every run in this invocation was recorded under, zero when nothing ran.
	Batch int64 `json:"batch,omitempty"`
	// Runs is one entry per migration performed, in the order they were performed.
	Runs []ran `json:"runs"`
	// Skipped is one entry per migration this invocation did not perform.
	Skipped []left `json:"skipped,omitempty"`
	// Duration is how long every run in this invocation took together.
	Duration elapsed `json:"duration_ms"`
}

// planSummary is what one invocation would have done, having taken the lock and executed nothing.
type planSummary struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Direction is which way the invocation would have run.
	Direction ir.Direction `json:"direction"`
	// DryRun is always true, so a recorded document says for itself that nothing happened.
	DryRun bool `json:"dry_run"`
	// Recorded reports whether this database has a migration history table at all.
	Recorded bool `json:"history_table"`
	// Batch is what these runs would have been recorded under.
	Batch int64 `json:"batch,omitempty"`
	// Steps is one entry per migration that would run, in the order they would run.
	Steps []planned `json:"steps"`
	// Skipped is one entry per migration this direction would not run.
	Skipped []left `json:"skipped,omitempty"`
}

// summaryOf renders what an invocation did, and is built even when the invocation also failed.
func summaryOf(database string, dir ir.Direction, result engine.Result) runSummary {
	summary := runSummary{
		Database:  database,
		Direction: dir,
		Batch:     result.Batch,
		Runs:      make([]ran, 0, len(result.Runs)),
		Skipped:   leftBehind(result.Skipped),
	}

	var total time.Duration

	for _, run := range result.Runs {
		total += run.Duration
		summary.Runs = append(summary.Runs, ranOf(run))
	}

	summary.Duration = elapsed(total)

	return summary
}

// planOf renders what an invocation would have done, and what it found standing in the way.
func planOf(s *session, previewed engine.Previewed) planSummary {
	allowed, caps := s.options.Allow, s.conn.Capabilities()

	summary := planSummary{
		Database:  s.database,
		Direction: previewed.Direction,
		DryRun:    true,
		Recorded:  previewed.Recorded,
		Batch:     previewed.Batch,
		Steps:     make([]planned, 0, len(previewed.Steps)),
		Skipped:   leftBehind(previewed.Skipped),
	}

	for _, step := range previewed.Steps {
		summary.Steps = append(summary.Steps, planned{
			Ordinal:     step.Ordinal,
			Version:     step.Plan.Migration.Version,
			File:        step.Plan.Migration.Filename(),
			Statements:  statementsOf(step.Plan),
			Deferred:    int(step.Plan.Deferred()),
			Chunked:     int(step.Plan.Chunked()),
			Parts:       partsOf(step.Plan),
			Refused:     refusedIn(allowed, step.Plan),
			Unsupported: unsupportedIn(caps, step.Plan),
		})
	}

	return summary
}

// refusedIn counts the operations in one compiled migration that destroy what nothing permits.
func refusedIn(allowed engine.Allowance, compiled plan.Plan) int {
	refused := 0

	for _, step := range compiled.Steps {
		if !allowed.Permits(step.Op) {
			refused++
		}
	}

	return refused
}

// unsupportedIn counts the operations in one compiled migration this server declares it cannot perform.
func unsupportedIn(caps capability.Set, compiled plan.Plan) int {
	return len(engine.Unsupported(operationsIn(compiled), caps))
}

// operationsIn returns what a compiled migration emitted, since a plan holds them beside statements.
func operationsIn(compiled plan.Plan) ir.Operations {
	ops := make(ir.Operations, 0, len(compiled.Steps))
	for _, step := range compiled.Steps {
		ops = append(ops, step.Op)
	}

	return ops
}

// leftBehind renders the migrations an invocation did not act on, in the order it reported them.
func leftBehind(skipped []engine.Skipped) []left {
	out := make([]left, 0, len(skipped))

	for _, s := range skipped {
		out = append(out, left{
			Version: s.Version,
			File:    describeFile(s.Migration),
			State:   string(s.State),
		})
	}

	return out
}

// describeFile names the file a migration was found in, which one only the database names has none of.
func describeFile(m migration.Migration) string {
	if m.Version == "" {
		return ""
	}

	return m.Filename()
}

// String renders what ran, one migration per line, then what the whole invocation cost.
func (s runSummary) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s — database %q%s\n", s.Direction, s.Database, describeBatch(s.Batch))

	if len(s.Runs) == 0 {
		fmt.Fprintf(&b, "\nnothing to do%s\n", describeSkipped(s.Skipped))

		return b.String()
	}

	b.WriteString("\n")

	for _, run := range s.Runs {
		fmt.Fprintf(&b, "  %d  %-*s  %s  %s\n", run.Ordinal, widest(s.Runs, fileOfRun), run.File,
			describeWork(run), run.Duration)
	}

	fmt.Fprintf(&b, "\n%s %s in %s%s\n", pastTense(s.Direction), describeMigrations(len(s.Runs)),
		s.Duration, describeSkipped(s.Skipped))

	return b.String()
}

// String renders what would run, one migration per line, and says plainly that none of it did.
func (s planSummary) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s — database %q (dry run)%s\n", s.Direction, s.Database, describeBatch(s.Batch))

	if !s.Recorded {
		fmt.Fprintf(&b, "\nthis database has no migration history table yet; migr %s would create it\n",
			s.Direction)
	}

	if len(s.Steps) == 0 {
		fmt.Fprintf(&b, "\nnothing to do%s\n", describeSkipped(s.Skipped))

		return b.String()
	}

	b.WriteString("\n")

	for _, step := range s.Steps {
		fmt.Fprintf(&b, "  %d  %-*s  %s%s\n", step.Ordinal, widest(s.Steps, fileOfStep), step.File,
			describePlannedWork(step), describeDeferred(step.Deferred))
	}

	fmt.Fprintf(&b, "\nwould %s %s%s. nothing was executed and nothing was recorded.\n%s\n",
		presentTense(s.Direction), describeMigrations(len(s.Steps)), describeSkipped(s.Skipped),
		describePlanCommand(s.Direction))

	s.writeUnsupported(&b)
	s.writeRefused(&b)

	return b.String()
}

// writeUnsupported says what this server will not perform, which migr up refuses before sending anything.
func (s planSummary) writeUnsupported(b *strings.Builder) {
	unsupported := 0
	for _, step := range s.Steps {
		unsupported += step.Unsupported
	}

	if unsupported == 0 {
		return
	}

	fmt.Fprintf(b, "\n%s this server declares it cannot perform, so migr %s refuses the migration "+
		"they are in. `migr plan` names them and gives the driver's reason for each.\n",
		describeUnpermitted(unsupported), s.Direction)
}

// writeRefused says what migr up would refuse, since a dry run that said nothing would mislead.
func (s planSummary) writeRefused(b *strings.Builder) {
	refused := 0
	for _, step := range s.Steps {
		refused += step.Refused
	}

	if refused == 0 {
		return
	}

	fmt.Fprintf(b, "\n%s that nothing has permitted, so migr %s refuses the migration they are "+
		"in. `migr plan` names them; --allow-destructive permits them for one run, and "+
		"validation.allow_destructive for the project.\n",
		describeUnpermitted(refused), s.Direction)
}

// describePlanCommand points at the command that prints the statements this one only counted.
func describePlanCommand(dir ir.Direction) string {
	if dir == ir.Down {
		return "run `migr plan --down` to see the statements themselves."
	}

	return "run `migr plan` to see the statements themselves."
}

// describeBatch names the batch an invocation used, which one that ran nothing never allocated.
func describeBatch(batch int64) string {
	if batch == 0 {
		return ""
	}

	return fmt.Sprintf(", batch %d", batch)
}

// describeDeferred says how many of a migration's operations are compiled only as each one runs.
func describeDeferred(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return ", and 1 operation compiled as it runs"
	}

	return fmt.Sprintf(", and %d operations compiled as they run", n)
}

// statementsOf counts what a plan would execute as statements, which a chunked one counts as none.
func statementsOf(compiled plan.Plan) int {
	if compiled.Chunked() > 0 {
		return 0
	}

	return len(compiled.Statements())
}

// describePlannedWork says what a migration would execute: its statements, or its chunked operations.
func describePlannedWork(step planned) string {
	if step.Chunked > 0 {
		return describeOperations(step.Chunked) + " in chunks"
	}

	return describeStatements(step.Statements) + describeParts(step.Parts)
}

// describeParts says that a migration runs in parts, each committing on its own, where it does.
func describeParts(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return " outside any transaction"
	}

	return fmt.Sprintf(" in %d parts", n)
}

// describeStatements counts statements with the right noun.
func describeStatements(n int) string {
	if n == 1 {
		return "1 statement"
	}

	return fmt.Sprintf("%d statements", n)
}

// describeMigrations counts migrations with the right noun.
func describeMigrations(n int) string {
	if n == 1 {
		return "1 migration"
	}

	return fmt.Sprintf("%d migrations", n)
}

// pastTense says what a direction did, since "up 2 migrations" is not a sentence.
func pastTense(dir ir.Direction) string {
	if dir == ir.Down {
		return "rolled back"
	}

	return "applied"
}

// presentTense says what a direction would do, for a run that has not done it.
func presentTense(dir ir.Direction) string {
	if dir == ir.Down {
		return "roll back"
	}

	return "apply"
}

// describeSkipped summarizes what was left alone, by the state that left it there.
func describeSkipped(skipped []left) string {
	if len(skipped) == 0 {
		return ""
	}

	counts := make(map[string]int, len(skipped))
	for _, s := range skipped {
		counts[s.State]++
	}

	states := make([]string, 0, len(counts))
	for state, n := range counts {
		states = append(states, fmt.Sprintf("%d %s", n, state))
	}

	sort.Strings(states)

	return " (" + fmt.Sprintf("%d skipped: ", len(skipped)) + strings.Join(states, ", ") + ")"
}

// fileOfRun names the file one performed migration was found in.
func fileOfRun(r ran) string { return r.File }

// fileOfStep names the file one previewed migration was found in.
func fileOfStep(p planned) string { return p.File }

// widest returns the column width the longest rendering of these rows needs.
func widest[T any](rows []T, of func(T) string) int {
	width := 0

	for _, row := range rows {
		width = max(width, len(of(row)))
	}

	return width
}
