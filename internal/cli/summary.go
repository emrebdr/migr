package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
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
	// Duration is how long the run took.
	Duration elapsed `json:"duration_ms"`
}

// planned is one migration an invocation would have performed, compiled but never executed.
type planned struct {
	// Ordinal is the position within the batch it would have been recorded under.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Statements is how many statements it would execute.
	Statements int `json:"statements"`
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
		summary.Runs = append(summary.Runs, ran{
			Ordinal:    run.Ordinal,
			Version:    run.Migration.Version,
			File:       run.Migration.Filename(),
			Statements: run.Statements,
			Duration:   elapsed(run.Duration),
		})
	}

	summary.Duration = elapsed(total)

	return summary
}

// planOf renders what an invocation would have done, and what it found standing in the way.
func planOf(database string, previewed engine.Previewed) planSummary {
	summary := planSummary{
		Database:  database,
		Direction: previewed.Direction,
		DryRun:    true,
		Recorded:  previewed.Recorded,
		Batch:     previewed.Batch,
		Steps:     make([]planned, 0, len(previewed.Steps)),
		Skipped:   leftBehind(previewed.Skipped),
	}

	for _, step := range previewed.Steps {
		summary.Steps = append(summary.Steps, planned{
			Ordinal:    step.Ordinal,
			Version:    step.Plan.Migration.Version,
			File:       step.Plan.Migration.Filename(),
			Statements: len(step.Plan.Statements()),
		})
	}

	return summary
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
			describeStatements(int(run.Statements)), run.Duration)
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
		fmt.Fprintf(&b, "  %d  %-*s  %s\n", step.Ordinal, widest(s.Steps, fileOfStep), step.File,
			describeStatements(step.Statements))
	}

	fmt.Fprintf(&b, "\nwould %s %s%s. nothing was executed and nothing was recorded.\n%s\n",
		presentTense(s.Direction), describeMigrations(len(s.Steps)), describeSkipped(s.Skipped),
		describePlanCommand(s.Direction))

	return b.String()
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
