package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// errBothSelectors marks a run given two ways of narrowing itself, which name different sets.
var errBothSelectors = errors.New("--step and --to select differently, so a run takes one of them")

// newUpCmd builds the `migr up` subcommand.
func newUpCmd(opts *options) *cobra.Command {
	return directionCmd(opts, ir.Up, &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		Long: "Applies every pending migration in order, under the migration lock, recording\n" +
			"each one before the transaction that ran it commits.",
		Args: cobra.NoArgs,
	})
}

// newDownCmd builds the `migr down` subcommand.
func newDownCmd(opts *options) *cobra.Command {
	return directionCmd(opts, ir.Down, &cobra.Command{
		Use:   "down",
		Short: "Roll back applied migrations, newest first",
		Long: "Rolls back the last batch, or what --step and --to name, newest first. A\n" +
			"migration that cannot be reversed stops the whole run before its first statement.",
		Args: cobra.NoArgs,
	})
}

// directionCmd finishes either command, since the two differ only in the direction they run.
func directionCmd(opts *options, dir ir.Direction, cmd *cobra.Command) *cobra.Command {
	narrow := &narrowing{}
	narrow.register(cmd, dir)

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runDirection(cmd, opts, *narrow, dir)
	}

	return cmd
}

// narrowing is what --step, --to, --dry-run, and --down were given, before any becomes a scope.
type narrowing struct {
	// step acts on the first N migrations the direction would act on.
	step int32
	// to names the version the database should be left at.
	to string
	// dry stops the run before its first statement, reporting what it would have done.
	dry bool
	// down selects the rollback, for the one command that is not already a direction.
	down bool
}

// register binds the flags that narrow a run and the one that stops it before its first statement.
func (n *narrowing) register(cmd *cobra.Command, dir ir.Direction) {
	n.selectors(cmd, describeStep(dir))
	cmd.Flags().BoolVar(&n.dry, "dry-run", false, "report what would run, and execute none of it")
}

// selectors binds the flags that narrow which migrations a direction acts on.
func (n *narrowing) selectors(cmd *cobra.Command, step string) {
	f := cmd.Flags()
	f.Int32Var(&n.step, "step", 0, step)
	f.StringVar(&n.to, "to", "", "leave the database at this version")
}

// direction reports which way the flags asked for, for a command that is not itself one.
func (n narrowing) direction() ir.Direction {
	if n.down {
		return ir.Down
	}

	return ir.Up
}

// describeStep says what a step counts, which is not the same set in the two directions.
func describeStep(dir ir.Direction) string {
	if dir == ir.Down {
		return "roll back the last N applied migrations"
	}

	return "apply the first N pending migrations"
}

// scope turns the flags into the selector the engine narrows with, refusing two that disagree.
func (n narrowing) scope(dir ir.Direction) (engine.Scope, error) {
	if n.step != 0 && n.to != "" {
		return engine.Scope{}, errBothSelectors
	}

	if n.step != 0 {
		return engine.Steps(n.step), nil
	}

	if n.to != "" {
		return engine.To(n.to), nil
	}

	if dir == ir.Down {
		return engine.LastBatch(), nil
	}

	return engine.All(), nil
}

// runDirection opens the database a command targets and applies or rolls back against it.
func runDirection(cmd *cobra.Command, opts *options, narrow narrowing, dir ir.Direction) (err error) {
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

	if narrow.dry {
		return previewDirection(cmd, opts, s, dir)
	}

	return performDirection(cmd, opts, s, dir)
}

// performDirection runs the direction and reports what it did, even when it is also reporting a failure.
func performDirection(cmd *cobra.Command, opts *options, s *session, dir ir.Direction) error {
	result, runErr := s.run(cmd.Context(), dir)

	if runErr != nil && len(result.Runs) == 0 {
		return runErr
	}

	written := writeResult(cmd.OutOrStdout(), opts, summaryOf(s.database, dir, result))

	return errors.Join(runErr, written)
}

// previewDirection reports what the direction would run, having taken the lock and executed nothing.
func previewDirection(cmd *cobra.Command, opts *options, s *session, dir ir.Direction) error {
	previewed, err := engine.Preview(cmd.Context(), s.conn, s.options, dir)
	if err != nil {
		return err
	}

	return writeResult(cmd.OutOrStdout(), opts, planOf(s.database, previewed))
}
