package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// digestPrefix is how much of a digest a table shows, since comparing 64 hex characters is --json's job.
const digestPrefix = 12

// newStatusCmd builds the `migr status` subcommand.
func newStatusCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what each migration reconciled to",
		Long: "Folds this database's migration history against the migrations on disk and prints\n" +
			"one state per migration. It takes no migration lock, creates nothing, and writes\n" +
			"nothing, so it can be run while a migration is running.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd, opts)
		},
	}
}

// runStatus reconciles the migrations on disk against this database's history and prints the result.
func runStatus(cmd *cobra.Command, opts *options) (err error) {
	t, err := resolve(opts)
	if err != nil {
		return err
	}

	found, files, err := t.files(opts)
	if err != nil {
		return err
	}

	s, err := t.connect(cmd.Context(), opts)
	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, s.close(context.WithoutCancel(cmd.Context())))
	}()

	report, err := s.reconcile(cmd.Context(), files)
	if err != nil {
		return err
	}

	cps, err := checkpointsOf(cmd.Context(), s.conn.History())
	if err != nil {
		return err
	}

	states, err := shadowStatesOf(cmd.Context(), s.conn.History())
	if err != nil {
		return err
	}

	return writeResult(cmd.OutOrStdout(), opts, statusOf(s.database, found, report, cps).
		withShadows(states))
}

// checkpointsOf reads how far every chunked migration got, where the history remembers it at all.
func checkpointsOf(ctx context.Context, store history.Store) ([]history.Checkpoint, error) {
	remembering, remembers := store.(history.Checkpointer)
	if !remembers {
		return nil, nil
	}

	cps, err := remembering.Checkpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading how far a chunked migration got: %w", err)
	}

	return cps, nil
}

// recordedRun is the run that left a migration in the state it is in, absent when none did.
type recordedRun struct {
	// Batch is the invocation that run belonged to, which is the batch migr down would act on.
	Batch int64 `json:"batch"`
	// Ordinal is its position within that batch.
	Ordinal int32 `json:"ordinal"`
	// Direction is whether that run applied the migration or rolled it back.
	Direction ir.Direction `json:"direction"`
	// Outcome is how it ended, which is not the same axis as the state it produced.
	Outcome history.Outcome `json:"outcome"`
	// AppliedAt is when it started.
	AppliedAt time.Time `json:"applied_at"`
	// Duration is how long it took.
	Duration elapsed `json:"duration_ms"`
	// LastStatement is the statement a dirty run reached, and zero on every other outcome.
	LastStatement int32 `json:"last_statement,omitempty"`
	// Error is why it did not complete, empty on a run that did.
	Error string `json:"error,omitempty"`
}

// statusRow is one version's reconciled state, and the run that decided it.
type statusRow struct {
	// Version is the migration's UTC timestamp, which a version only the database carries still has.
	Version string `json:"version"`
	// File is the filename it was found in, empty when no file carries the version.
	File string `json:"file,omitempty"`
	// State is what reconciliation resolved it to, and it is exactly one of the five.
	State history.State `json:"state"`
	// Run is the run that left it in that state, absent for a migration nothing has run.
	Run *recordedRun `json:"run,omitempty"`
	// RecordedSource is the source digest that run wrote, present only where the file has since moved.
	RecordedSource string `json:"recorded_source,omitempty"`
	// CurrentSource is what the file hashes to now, present only where it disagrees with the recorded one.
	CurrentSource string `json:"current_source,omitempty"`
	// Checkpoint is how far a dirty migration running in chunks got, absent for every other one.
	Checkpoint *checkpoint `json:"checkpoint,omitempty"`
	// Shadow is where a dirty shadow-table migration stands, absent for every other one.
	Shadow *shadowDoc `json:"shadow,omitempty"`
}

// orderBasis names what decided a report's sequence, since a report resolves only what needs no runtime.
type orderBasis string

// The two bases a report prints under. Only a command that starts a runtime resolves the overlay.
const (
	// orderResolved marks a sequence nothing here can reorder, which is the order a run will use.
	orderResolved orderBasis = "resolved"
	// orderVersion marks version order, which a declared dependency this report cannot read may move.
	orderVersion orderBasis = "version"
)

// statusReport is what one database reconciled to, in the form --json reports it.
type statusReport struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Recorded reports whether this database has a migration history table at all.
	Recorded bool `json:"history_table"`
	// Files is how many migration files are on disk, which is a fact about the directory.
	Files int `json:"files"`
	// Order names which sequence the statuses are in, which is a run's own only when nothing can move it.
	Order orderBasis `json:"order"`
	// Unread is how many migrations may declare an edge only a started runtime could have reported.
	Unread int `json:"unread_declarations,omitempty"`
	// Statuses is one entry per version that resolved to a state, in the order Order names.
	Statuses []statusRow `json:"statuses"`
	// Counts is how many versions resolved to each state, carrying every state so a script may read any.
	Counts map[string]int `json:"counts"`
	// Environment is the one a seed report's database runs under, absent from a migration report.
	Environment string `json:"environment,omitempty"`
	// about is what the rows are, which the zero value reads as migrations.
	about subject
}

// statusOf renders a reconciliation report, which a database with no history table resolves to none of.
func statusOf(database string, found []migration.Migration, report history.Report,
	cps []history.Checkpoint,
) statusReport {
	statuses, present := report.Resolved()

	rows := make([]statusRow, 0, len(statuses))
	for _, status := range statuses {
		rows = append(rows, rowOf(status, cps))
	}

	unread := declarable(found)

	return statusReport{
		Database: database,
		Recorded: present,
		Files:    len(found),
		Order:    basisOf(unread),
		Unread:   unread,
		Statuses: rows,
		Counts:   countStates(statuses),
	}
}

// declarable counts the migrations whose declared dependencies only a started runtime reports.
func declarable(found []migration.Migration) int {
	total := 0

	for _, m := range found {
		if needsRuntime(languageOf(m.Ext)) {
			total++
		}
	}

	return total
}

// basisOf names what a sequence is, which is the run order exactly when no declaration went unread.
func basisOf(unread int) orderBasis {
	if unread == 0 {
		return orderResolved
	}

	return orderVersion
}

// rowOf renders one version's state, the run that decided it, and the digests a drifted one moved between.
func rowOf(status history.Status, cps []history.Checkpoint) statusRow {
	row := statusRow{
		Version: status.Version,
		File:    describeFile(status.Migration),
		State:   status.State,
		Run:     runOf(status.Recorded),
	}

	if status.State == history.StateDirty {
		if cp, found := history.Find(cps, status.Version, status.Recorded.Direction); found {
			row.Checkpoint = checkpointOf(&cp)
		}
	}

	if status.State == history.StateDrifted {
		row.RecordedSource = status.Recorded.Checksum.Source.String()
		row.CurrentSource = status.Source.String()
	}

	return row
}

// runOf renders the run that anchored a state, which a migration nothing has ever run has none of.
func runOf(rec history.Record) *recordedRun {
	if !anchored(rec) {
		return nil
	}

	return &recordedRun{
		Batch:         rec.Batch,
		Ordinal:       rec.Ordinal,
		Direction:     rec.Direction,
		Outcome:       rec.Outcome,
		AppliedAt:     rec.AppliedAt.UTC(),
		Duration:      elapsed(rec.Duration),
		LastStatement: rec.LastStatement,
		Error:         rec.Error,
	}
}

// anchored reports whether a run left this migration where it is, which a pending one has none of.
func anchored(rec history.Record) bool {
	return rec.Batch != 0
}

// countStates counts what resolved to each state, carrying every state so a zero is readable too.
func countStates(statuses []history.Status) map[string]int {
	counts := make(map[string]int, len(history.States()))
	for _, state := range history.States() {
		counts[state.String()] = 0
	}

	for _, status := range statuses {
		counts[status.State.String()]++
	}

	return counts
}

// String renders one state per line, then how many resolved to each.
func (s statusReport) String() string {
	var b strings.Builder

	about := s.about.or()

	fmt.Fprintf(&b, "%sstatus — database %q%s\n", about.command, s.Database,
		describeEnvironment(s.Environment))

	if !s.Recorded {
		fmt.Fprintf(&b, "\nthis database has no %s yet, so nothing has been\n"+
			"reconciled. %s would create it, and %s on disk.\n", about.table, about.creator,
			about.files(s.Files))

		return b.String()
	}

	if len(s.Statuses) == 0 {
		fmt.Fprintf(&b, "\nnothing to report: no %s is in any state, and migr %shistory\n"+
			"shows the runs that left it that way.\n", about.noun, about.command)

		return b.String()
	}

	s.writeRows(&b)

	fmt.Fprintf(&b, "\n%s: %s\n", about.count(len(s.Statuses)), joinCounts(s.Counts))

	s.writeOrderNote(&b)

	return b.String()
}

// describeEnvironment names the environment a seed report is about, and nothing for a migration one.
func describeEnvironment(environment string) string {
	if environment == "" {
		return ""
	}

	return fmt.Sprintf(", environment %q", environment)
}

// writeOrderNote says what this sequence is, for a project whose own order needs a runtime to read.
func (s statusReport) writeOrderNote(b *strings.Builder) {
	if s.Order != orderVersion {
		return
	}

	about := s.about.or()

	fmt.Fprintf(b, "\nthe order above is version order: %s may declare a dependency that only a "+
		"language runtime reports, and a report starts none. migr %s prints the order a run "+
		"will use.\n", about.count(s.Unread), orderShower(about))
}

// orderShower names the command that prints the order a run will use, which for seeds is a dry run.
func orderShower(about subject) string {
	if about.command != "" {
		return about.command + "--dry-run"
	}

	return "plan"
}

// writeRows renders one line per version, aligned on the widest of each column.
func (s statusReport) writeRows(b *strings.Builder) {
	states, names := widest(s.Statuses, stateOfRow), widest(s.Statuses, nameOfRow)
	runs := widest(s.Statuses, describeRun)
	about := s.about.or()

	b.WriteString("\n")

	for _, row := range s.Statuses {
		line := fmt.Sprintf("  %-*s  %-*s  %-*s  %s", states, row.State, names, nameOfRow(row),
			runs, describeRun(row), describeState(row, about))

		fmt.Fprintf(b, "%s\n", strings.TrimRight(line, " "))
	}
}

// stateOfRow names the state one row resolved to.
func stateOfRow(row statusRow) string { return row.State.String() }

// nameOfRow names one row's file, falling back to the version no file carries.
func nameOfRow(row statusRow) string {
	if row.File == "" {
		return row.Version
	}

	return row.File
}

// describeRun names the run that left a migration where it is, which is not always an apply.
func describeRun(row statusRow) string {
	if row.Run == nil {
		return ""
	}

	return fmt.Sprintf("%s in batch %d, %s", row.Run.Direction, row.Run.Batch,
		row.Run.AppliedAt.Format(stamp))
}

// describeState says what a state means for this row, and is total so a sixth breaks the build.
func describeState(row statusRow, about subject) string {
	switch row.State {
	case history.StateApplied, history.StatePending:
		return describeAsserted(row.Run)
	case history.StateMissing:
		return "no " + about.noun + " file carries this version"
	case history.StateDrifted:
		return fmt.Sprintf("edited since it was applied (recorded %s, now %s)",
			shortDigest(row.RecordedSource), shortDigest(row.CurrentSource))
	case history.StateDirty:
		if row.Shadow != nil {
			return describeShadowStopped(row.Shadow)
		}

		return describeDirty(row.Run, row.Checkpoint, about)
	}

	return "this build has no note for that state"
}

// describeAsserted says when a settled state was asserted by an operator rather than run by migr.
func describeAsserted(run *recordedRun) string {
	if run == nil || run.Outcome != history.Resolved {
		return ""
	}

	return "resolved by hand rather than run: " + run.Error
}

// describeDirty says how far a run got before it left the schema between states.
func describeDirty(run *recordedRun, at *checkpoint, about subject) string {
	if run == nil {
		return "left the schema between states"
	}

	if at != nil {
		return fmt.Sprintf("stopped running in chunks: operation %d, %s and %s written, through "+
			"key (%s) -- the next migr %s carries on from there", at.Operation,
			describeChunks(at.Chunks), describeWritten(at.Written), strings.Join(at.Cursor, ", "),
			describeResumer(run.Direction, about))
	}

	return fmt.Sprintf("stopped after statement %d of the %s run", run.LastStatement, run.Direction)
}

// describeResumer names the command that carries a stopped chunked run on, which a rollback's is not.
func describeResumer(dir ir.Direction, about subject) string {
	if dir == ir.Up && about.command == "" {
		return "up"
	}

	if dir == ir.Up {
		return "seed"
	}

	return about.command + "resolve --resume"
}

// shortDigest keeps enough of a rendered digest to compare two by eye.
func shortDigest(rendered string) string {
	if len(rendered) <= digestPrefix {
		return rendered
	}

	return rendered[:digestPrefix]
}

// joinCounts names how many resolved to each state, in the order the states are declared.
func joinCounts(counts map[string]int) string {
	named := make([]string, 0, len(counts))

	for _, state := range history.States() {
		if n := counts[state.String()]; n > 0 {
			named = append(named, fmt.Sprintf("%d %s", n, state))
		}
	}

	return strings.Join(named, ", ")
}
