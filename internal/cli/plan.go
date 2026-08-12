package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// statementIndent is how far a statement sits under the operation it was compiled from.
const statementIndent = "          "

// newPlanCmd builds the `migr plan` subcommand.
func newPlanCmd(opts *options) *cobra.Command {
	narrow := &narrowing{}

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Print the statements a run would execute, without executing any of them",
		Long: "Compiles every migration a run would perform and prints the statements it would\n" +
			"send, grouped by migration and by operation. It takes the migration lock and reads\n" +
			"the history, but writes nothing at all — not even the history table.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPlan(cmd, opts, *narrow)
		},
	}

	narrow.selectors(cmd, "plan only the first N migrations the direction would act on")
	cmd.Flags().BoolVar(&narrow.down, "down", false, "plan the rollback rather than the pending run")

	return cmd
}

// runPlan compiles what a direction would run against this database and prints every statement.
func runPlan(cmd *cobra.Command, opts *options, narrow narrowing) (err error) {
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

	return report(cmd, opts, s, dir)
}

// report compiles the direction and prints what it got, which a refusal partway leaves worth printing.
func report(cmd *cobra.Command, opts *options, s *session, dir ir.Direction) error {
	previewed, planErr := engine.Preview(cmd.Context(), s.conn, s.options, dir)

	if planErr != nil && len(previewed.Steps) == 0 {
		return planErr
	}

	written := writeResult(cmd.OutOrStdout(), opts, compiledOf(s, previewed))

	return errors.Join(planErr, written)
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
	// Statements are what it compiled to, in the order they would run.
	Statements []string `json:"statements"`
}

// compiledMigration is one migration a plan would perform, compiled for this engine and never run.
type compiledMigration struct {
	// Ordinal is the position within the batch it would be recorded under.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Operations are what it emitted, each beside the statements it became.
	Operations []operation `json:"operations"`
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
	}

	caps := s.conn.Capabilities()

	for _, step := range previewed.Steps {
		rendered.Migrations = append(rendered.Migrations, migrationOf(caps, step))
		rendered.Statements += int(step.Plan.Len())
	}

	return rendered
}

// migrationOf renders one migration's operations, each with the verdict this engine gives it.
func migrationOf(caps capability.Set, planned engine.Planned) compiledMigration {
	ops := make([]operation, 0, len(planned.Plan.Steps))
	for _, step := range planned.Plan.Steps {
		ops = append(ops, operationOf(caps, step))
	}

	return compiledMigration{
		Ordinal:    planned.Ordinal,
		Version:    planned.Plan.Migration.Version,
		File:       planned.Plan.Migration.Filename(),
		Operations: ops,
	}
}

// operationOf renders one operation, its verdict, and the statements the driver compiled it to.
func operationOf(caps capability.Set, step plan.Step) operation {
	verdict := caps.Resolve(step.Op)

	stmts := make([]string, 0, len(step.Statements))
	for _, stmt := range step.Statements {
		stmts = append(stmts, stmt.SQL)
	}

	return operation{
		Index:      step.Index,
		Kind:       step.Op.Kind(),
		Support:    verdict.Support(),
		Strategy:   verdict.Strategy(),
		Statements: stmts,
	}
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

		return b.String()
	}

	for _, m := range p.Migrations {
		m.write(&b)
	}

	fmt.Fprintf(&b, "\n%s, %s, %s%s. nothing was executed and nothing was recorded.\n",
		describeMigrations(len(p.Migrations)), describeOperations(p.countOperations()),
		describeStatements(p.Statements), describeSkipped(p.Skipped))

	return b.String()
}

// countOperations returns how many operations the whole plan emitted, across every migration.
func (p compiledPlan) countOperations() int {
	total := 0
	for _, m := range p.Migrations {
		total += len(m.Operations)
	}

	return total
}

// write renders one migration's heading and every operation under it.
func (m compiledMigration) write(b *strings.Builder) {
	fmt.Fprintf(b, "\n  %d  %s\n", m.Ordinal, m.File)

	for _, op := range m.Operations {
		op.write(b)
	}
}

// write renders one operation's heading and the statements it compiled to, indented beneath it.
func (o operation) write(b *strings.Builder) {
	fmt.Fprintf(b, "       %d  %s  %s\n", o.Index, o.Kind, describeVerdict(o))

	for _, stmt := range o.Statements {
		fmt.Fprintf(b, "%s\n", indented(stmt))
	}
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
