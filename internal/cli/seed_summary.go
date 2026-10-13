package cli

import (
	"fmt"
	"strings"
)

// seedSummary is what one seed run did against one database, in the form --json reports it.
type seedSummary struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Environment is the one this database runs under, which chose the seeds it ran.
	Environment string `json:"environment"`
	// Batch is what every seed in this run was recorded under, zero when nothing ran.
	Batch int64 `json:"batch,omitempty"`
	// Runs is one entry per seed performed, in the order they were performed.
	Runs []ran `json:"runs"`
	// Skipped is one entry per seed this run did not perform.
	Skipped []left `json:"skipped,omitempty"`
	// Duration is how long every seed in this run took together.
	Duration elapsed `json:"duration_ms"`
}

// seedPlan is what one seed run would have done, having taken the lock and executed nothing.
type seedPlan struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Environment is the one this database runs under.
	Environment string `json:"environment"`
	// DryRun is always true, so a recorded document says for itself that nothing happened.
	DryRun bool `json:"dry_run"`
	// Recorded reports whether this database has a seed history table at all.
	Recorded bool `json:"seed_table"`
	// Batch is what these seeds would have been recorded under.
	Batch int64 `json:"batch,omitempty"`
	// Steps is one entry per seed that would run, in the order they would run.
	Steps []planned `json:"steps"`
	// Skipped is one entry per seed this run would not run.
	Skipped []left `json:"skipped,omitempty"`
}

// seedSummaryOf renders a run's summary as a seed run, naming the environment that chose its seeds.
func seedSummaryOf(run runSummary, environment string) seedSummary {
	return seedSummary{Database: run.Database, Environment: environment, Batch: run.Batch,
		Runs: run.Runs, Skipped: run.Skipped, Duration: run.Duration}
}

// seedPlanOf renders a preview as a seed run's, naming the environment that chose its seeds.
func seedPlanOf(preview planSummary, environment string) seedPlan {
	return seedPlan{Database: preview.Database, Environment: environment, DryRun: true,
		Recorded: preview.Recorded, Batch: preview.Batch, Steps: preview.Steps,
		Skipped: preview.Skipped}
}

// String renders what ran, one seed per line, then what the whole run cost.
func (s seedSummary) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "seed — database %q, environment %q%s\n", s.Database, s.Environment,
		describeBatch(s.Batch))

	if len(s.Runs) == 0 {
		fmt.Fprintf(&b, "\nnothing to seed%s\n", describeSkipped(s.Skipped))

		return b.String()
	}

	b.WriteString("\n")

	for _, run := range s.Runs {
		fmt.Fprintf(&b, "  %d  %-*s  %s  %s\n", run.Ordinal, widest(s.Runs, fileOfRun), run.File,
			describeStatements(int(run.Statements)), run.Duration)
	}

	fmt.Fprintf(&b, "\nseeded %s in %s%s\n", describeSeeds(len(s.Runs)), s.Duration,
		describeSkipped(s.Skipped))

	return b.String()
}

// String renders what would run, one seed per line, and says plainly that none of it did.
func (s seedPlan) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "seed — database %q, environment %q (dry run)%s\n", s.Database, s.Environment,
		describeBatch(s.Batch))

	if !s.Recorded {
		b.WriteString("\nthis database has no seed history table yet; migr seed would create it\n")
	}

	if len(s.Steps) == 0 {
		fmt.Fprintf(&b, "\nnothing to seed%s\n", describeSkipped(s.Skipped))

		return b.String()
	}

	b.WriteString("\n")

	for _, step := range s.Steps {
		fmt.Fprintf(&b, "  %d  %-*s  %s\n", step.Ordinal, widest(s.Steps, fileOfStep), step.File,
			describeStatements(step.Statements))
	}

	fmt.Fprintf(&b, "\nwould seed %s%s. nothing was executed and nothing was recorded.\n",
		describeSeeds(len(s.Steps)), describeSkipped(s.Skipped))

	s.writeUnsupported(&b)

	return b.String()
}

// writeUnsupported says what this server will not perform, which migr seed refuses before sending anything.
func (s seedPlan) writeUnsupported(b *strings.Builder) {
	unsupported := 0
	for _, step := range s.Steps {
		unsupported += step.Unsupported
	}

	if unsupported == 0 {
		return
	}

	fmt.Fprintf(b, "\n%s this server declares it cannot perform, so migr seed refuses the seed "+
		"they are in.\n", describeUnpermitted(unsupported))
}

// describeSeeds counts seeds with the right noun.
func describeSeeds(n int) string {
	if n == 1 {
		return "1 seed"
	}

	return fmt.Sprintf("%d seeds", n)
}
