package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// leading is the indent every line of a table starts with.
const leading = "  "

// gap is what separates two columns of a table.
const gap = "  "

// headMigration is the column a run's failure is written out under, since a server error is a sentence.
const headMigration = "migration"

// newHistoryCmd builds the `migr history` subcommand.
func newHistoryCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "Print the execution log, one line per run",
		Long: "Prints every run this database has recorded, rollbacks and failures included, in\n" +
			"the order the runs happened. It reads no migration files: the log is the database's,\n" +
			"and a run may outlive the file it ran from.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHistory(cmd, opts)
		},
	}
}

// runHistory prints this database's execution log, discovering nothing and writing nothing.
func runHistory(cmd *cobra.Command, opts *options) (err error) {
	t, err := resolve(opts)
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

	log, err := s.log(cmd.Context())
	if err != nil {
		return err
	}

	return writeResult(cmd.OutOrStdout(), opts, logOf(s.database, log))
}

// loggedRun is one row of the migration history, in the form --json reports it.
type loggedRun struct {
	// Batch is the migr invocation this run belonged to.
	Batch int64 `json:"batch"`
	// Ordinal is the run's position within that batch.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp, the version its filename carried.
	Version string `json:"version"`
	// Name is the migration's snake_case name, the rest of that filename.
	Name string `json:"name"`
	// Direction is whether the run applied the migration or rolled it back.
	Direction ir.Direction `json:"direction"`
	// Outcome is how the run ended.
	Outcome history.Outcome `json:"outcome"`
	// AppliedAt is when the run started.
	AppliedAt time.Time `json:"applied_at"`
	// Duration is how long it took.
	Duration elapsed `json:"duration_ms"`
	// Checksum is what the migration hashed to when the run recorded it, both halves.
	Checksum string `json:"checksum"`
	// LastStatement is the statement a dirty run reached, and zero on every other outcome.
	LastStatement int32 `json:"last_statement,omitempty"`
	// Error is why the run did not complete, empty on one that did.
	Error string `json:"error,omitempty"`
	// Driver is the engine the run targeted.
	Driver string `json:"driver"`
	// Runtime is the language the migration was written in.
	Runtime string `json:"runtime"`
	// EngineVersion is the migr build that performed the run.
	EngineVersion string `json:"engine_version"`
	// IRVersion is the operation IR version that build emitted.
	IRVersion int32 `json:"ir_version"`
}

// migration names the run's migration the only way a row can, since no row records an extension.
func (r loggedRun) migration() string {
	return r.Version + "_" + r.Name
}

// executionLog is every run one database has recorded, in the form --json reports it.
type executionLog struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Recorded reports whether this database has a migration history table at all.
	Recorded bool `json:"history_table"`
	// Runs is every recorded run, in the order the runs happened.
	Runs []loggedRun `json:"runs"`
}

// logOf renders a history log, which a database with no history table has none of.
func logOf(database string, log history.Log) executionLog {
	recorded := log.Runs()

	runs := make([]loggedRun, 0, len(recorded))
	for _, rec := range recorded {
		runs = append(runs, loggedRunOf(rec))
	}

	return executionLog{Database: database, Recorded: log.Present(), Runs: runs}
}

// loggedRunOf renders one recorded run, carrying every column so --json can inspect the table itself.
func loggedRunOf(rec history.Record) loggedRun {
	return loggedRun{
		Batch:         rec.Batch,
		Ordinal:       rec.Ordinal,
		Version:       rec.Version,
		Name:          rec.Name,
		Direction:     rec.Direction,
		Outcome:       rec.Outcome,
		AppliedAt:     rec.AppliedAt.UTC(),
		Duration:      elapsed(rec.Duration),
		Checksum:      rec.Checksum.String(),
		LastStatement: rec.LastStatement,
		Error:         rec.Error,
		Driver:        rec.Driver,
		Runtime:       rec.Runtime,
		EngineVersion: rec.EngineVersion,
		IRVersion:     rec.IRVersion,
	}
}

// logColumn is one column of the log table: its heading, its alignment, and what it reads from a run.
type logColumn struct {
	// head is what the column is called, which also sets its minimum width.
	head string
	// right reports whether the column holds a number, which reads right-aligned.
	right bool
	// of renders one run's value for this column.
	of func(loggedRun) string
}

// logColumns is the table migr history prints, in the order it prints them.
var logColumns = []logColumn{
	{head: "batch", right: true, of: func(r loggedRun) string { return strconv.FormatInt(r.Batch, 10) }},
	{head: "#", right: true, of: func(r loggedRun) string { return strconv.Itoa(int(r.Ordinal)) }},
	{head: headMigration, of: loggedRun.migration},
	{head: "dir", of: func(r loggedRun) string { return string(r.Direction) }},
	{head: "outcome", of: func(r loggedRun) string { return r.Outcome.String() }},
	{head: "started", of: func(r loggedRun) string { return r.AppliedAt.Format(stamp) }},
	{head: "took", right: true, of: func(r loggedRun) string { return r.Duration.String() }},
}

// migrationColumn is where the table puts the migration, found by name so reordering cannot break it.
var migrationColumn = slices.IndexFunc(logColumns, func(c logColumn) bool {
	return c.head == headMigration
})

// String renders the log as the table it is, with each failure written out under the run it ended.
func (l executionLog) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "history — database %q\n", l.Database)

	if !l.Recorded {
		b.WriteString("\nthis database has no migration history table yet; nothing has run against it.\n")

		return b.String()
	}

	if len(l.Runs) == 0 {
		b.WriteString("\nthis database's migration history table holds no runs yet.\n")

		return b.String()
	}

	l.writeTable(&b)

	fmt.Fprintf(&b, "\n%s\n", describeRuns(len(l.Runs)))

	return b.String()
}

// writeTable renders the heading and one line per run, each column as wide as its widest value.
func (l executionLog) writeTable(b *strings.Builder) {
	widths := widthsOf(l.Runs)
	indent := strings.Repeat(" ", detailIndent(widths))

	b.WriteString("\n")
	writeCells(b, headings(), widths)

	for _, run := range l.Runs {
		writeCells(b, cellsOf(run), widths)

		if detail := describeOutcome(run); detail != "" {
			fmt.Fprintf(b, "%s%s\n", indent, detail)
		}
	}
}

// detailIndent is where the migration column starts, so a failure reads as part of the run above it.
func detailIndent(widths []int) int {
	indent := len(leading)
	for _, width := range widths[:migrationColumn] {
		indent += width + len(gap)
	}

	return indent
}

// headings renders the column names as a row, so the heading is laid out by the same code as the rest.
func headings() []string {
	cells := make([]string, len(logColumns))
	for i, column := range logColumns {
		cells[i] = column.head
	}

	return cells
}

// cellsOf renders one run as the cells of its line, in column order.
func cellsOf(run loggedRun) []string {
	cells := make([]string, len(logColumns))
	for i, column := range logColumns {
		cells[i] = column.of(run)
	}

	return cells
}

// widthsOf returns the width each column needs to hold its heading and every run's value.
func widthsOf(runs []loggedRun) []int {
	widths := make([]int, len(logColumns))

	for i, column := range logColumns {
		widths[i] = max(len(column.head), widest(runs, column.of))
	}

	return widths
}

// writeCells lays one row out across the given widths, numbers to the right and words to the left.
func writeCells(b *strings.Builder, cells []string, widths []int) {
	var line strings.Builder

	line.WriteString(leading)

	for i, cell := range cells {
		fmt.Fprintf(&line, "%s%s", pad(cell, widths[i], logColumns[i].right), gap)
	}

	fmt.Fprintf(b, "%s\n", strings.TrimRight(line.String(), " "))
}

// pad lays one cell out to a column's width, on the side its alignment asks for.
func pad(cell string, width int, right bool) string {
	if right {
		return fmt.Sprintf("%*s", width, cell)
	}

	return fmt.Sprintf("%-*s", width, cell)
}

// describeOutcome writes out what ended a run, and is total so a fourth outcome breaks the build.
func describeOutcome(run loggedRun) string {
	switch run.Outcome {
	case history.Applied:
		return ""
	case history.Failed:
		return "failed: " + run.Error
	case history.Dirty:
		return fmt.Sprintf("stopped after statement %d: %s", run.LastStatement, run.Error)
	}

	return run.Error
}

// describeRuns counts recorded runs with the right noun.
func describeRuns(n int) string {
	if n == 1 {
		return "1 run"
	}

	return fmt.Sprintf("%d runs", n)
}
