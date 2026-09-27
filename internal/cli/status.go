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

	_, files, err := t.files(opts)
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

	return writeResult(cmd.OutOrStdout(), opts, statusOf(s.database, len(files), report))
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
}

// statusReport is what one database reconciled to, in the form --json reports it.
type statusReport struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Recorded reports whether this database has a migration history table at all.
	Recorded bool `json:"history_table"`
	// Files is how many migration files are on disk, which is a fact about the directory.
	Files int `json:"files"`
	// Statuses is one entry per version that resolved to a state, in the order migr up would run them.
	Statuses []statusRow `json:"statuses"`
	// Counts is how many versions resolved to each state, carrying every state so a script may read any.
	Counts map[string]int `json:"counts"`
}

// statusOf renders a reconciliation report, which a database with no history table resolves to none of.
func statusOf(database string, files int, report history.Report) statusReport {
	statuses, present := report.Resolved()

	rows := make([]statusRow, 0, len(statuses))
	for _, status := range statuses {
		rows = append(rows, rowOf(status))
	}

	return statusReport{
		Database: database,
		Recorded: present,
		Files:    files,
		Statuses: rows,
		Counts:   countStates(statuses),
	}
}

// rowOf renders one version's state, the run that decided it, and the digests a drifted one moved between.
func rowOf(status history.Status) statusRow {
	row := statusRow{
		Version: status.Version,
		File:    describeFile(status.Migration),
		State:   status.State,
		Run:     runOf(status.Recorded),
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

	fmt.Fprintf(&b, "status — database %q\n", s.Database)

	if !s.Recorded {
		fmt.Fprintf(&b, "\nthis database has no migration history table yet, so nothing has been\n"+
			"reconciled. migr up would create it, and %s on disk.\n", describeFiles(s.Files))

		return b.String()
	}

	if len(s.Statuses) == 0 {
		b.WriteString("\nnothing to report: no migration is in any state, and migr history\n" +
			"shows the runs that left it that way.\n")

		return b.String()
	}

	s.writeRows(&b)

	fmt.Fprintf(&b, "\n%s: %s\n", describeMigrations(len(s.Statuses)), joinCounts(s.Counts))

	return b.String()
}

// writeRows renders one line per version, aligned on the widest of each column.
func (s statusReport) writeRows(b *strings.Builder) {
	states, names := widest(s.Statuses, stateOfRow), widest(s.Statuses, nameOfRow)
	runs := widest(s.Statuses, describeRun)

	b.WriteString("\n")

	for _, row := range s.Statuses {
		line := fmt.Sprintf("  %-*s  %-*s  %-*s  %s", states, row.State, names, nameOfRow(row),
			runs, describeRun(row), describeState(row))

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

// describeState says what a state means for this migration, and is total so a sixth breaks the build.
func describeState(row statusRow) string {
	switch row.State {
	case history.StateApplied, history.StatePending:
		return ""
	case history.StateMissing:
		return "no migration file carries this version"
	case history.StateDrifted:
		return fmt.Sprintf("edited since it was applied (recorded %s, now %s)",
			shortDigest(row.RecordedSource), shortDigest(row.CurrentSource))
	case history.StateDirty:
		return describeDirty(row.Run)
	}

	return "this build has no note for that state"
}

// describeDirty says how far a run got before it left the schema between states.
func describeDirty(run *recordedRun) string {
	if run == nil {
		return "left the schema between states"
	}

	return fmt.Sprintf("stopped after statement %d of the %s run", run.LastStatement, run.Direction)
}

// shortDigest keeps enough of a rendered digest to compare two by eye.
func shortDigest(rendered string) string {
	if len(rendered) <= digestPrefix {
		return rendered
	}

	return rendered[:digestPrefix]
}

// describeFiles counts the migration files on disk with the verb and noun that agree with the count.
func describeFiles(n int) string {
	if n == 1 {
		return "there is 1 migration file"
	}

	return fmt.Sprintf("there are %d migration files", n)
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
