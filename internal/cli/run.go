package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// errBothSelectors marks a run given two ways of narrowing itself, which name different sets.
var errBothSelectors = errors.New("--step and --to select differently, so a run takes one of them")

// mistyped names the refusals that are about this run's own flags rather than about the database.
//
// Each is decided before the run acts on anything and each is fixed by retyping a flag, which is
// what CLAUDE.md §5 gives exit 2 to. engine.ErrNotApplied is deliberately absent: the same command
// line succeeds against a database that does carry the version, so the argument is not wrong --
// the database is not where the operator thought it was, which is exit 1 (ADR 0029, D).
var mistyped = []error{engine.ErrNoSteps, engine.ErrUnknownVersion, engine.ErrNoBatch}

// selected tags a refusal about the flags this run was given, leaving every other failure alone.
func selected(err error) error {
	for _, sentinel := range mistyped {
		if errors.Is(err, sentinel) {
			return NewExitError(ExitValidation, err)
		}
	}

	return err
}

// newUpCmd builds the `migr up` subcommand.
func newUpCmd(opts *options) *cobra.Command {
	cmd := directionCmd(opts, ir.Up, &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		Long: "Applies every pending migration in order, under the migration lock, recording\n" +
			"each one before the transaction that ran it commits.",
		Args: argsOf(cobra.NoArgs),
	})

	registerAllowDestructive(cmd, opts)
	registerRelease(cmd, opts)
	registerUnattended(cmd, opts)

	return cmd
}

// unattendedVariable is the environment variable a CI job may opt out of confirming a cutover in instead of the flag.
const unattendedVariable = "MIGR_UNATTENDED"

// registerUnattended binds the flag that cuts a shadow-table migration over without waiting for a confirmation.
func registerUnattended(cmd *cobra.Command, opts *options) {
	cmd.Flags().BoolVar(&opts.unattended, "unattended", false,
		"cut a shadow-table migration over as soon as its copy is caught up, confirming nothing; "+
			unattendedVariable+" when unset")
}

// unattendedOf reads whether this run waits for no cutover confirmation, by flag or else by environment.
func unattendedOf(opts *options) (bool, error) {
	if opts.unattended {
		return true, nil
	}

	set := os.Getenv(unattendedVariable)
	if set == "" {
		return false, nil
	}

	unattended, err := strconv.ParseBool(set)
	if err != nil {
		return false, NewExitError(ExitValidation, fmt.Errorf("%s is %q, and it is read as true or "+
			"false: %w", unattendedVariable, set, err))
	}

	return unattended, nil
}

// releaseVariable is the environment variable a deploy may name its release in instead of the flag.
const releaseVariable = "MIGR_RELEASE"

// registerRelease binds the flag naming the deploy a run belongs to, on the commands that judge a step by it.
func registerRelease(cmd *cobra.Command, opts *options) {
	cmd.Flags().StringVar(&opts.release, "release", "",
		"the deploy this run belongs to, such as a build id; "+releaseVariable+" when unset")
}

// releaseOf is the release this invocation names, by flag or else by environment.
func releaseOf(opts *options) string {
	if opts.release != "" {
		return opts.release
	}

	return os.Getenv(releaseVariable)
}

// newDownCmd builds the `migr down` subcommand.
func newDownCmd(opts *options) *cobra.Command {
	return directionCmd(opts, ir.Down, &cobra.Command{
		Use:   "down",
		Short: "Roll back applied migrations, newest first",
		Long: "Rolls back the last batch, or what --step and --to name, newest first. A\n" +
			"migration that cannot be reversed stops the whole run before its first statement.",
		Args: argsOf(cobra.NoArgs),
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

	if dir == ir.Up {
		if opts.unattended, err = unattendedOf(opts); err != nil {
			return err
		}
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

	logHeld(opts, result.Held)

	written := writeResult(cmd.OutOrStdout(), opts, summaryOf(s.database, dir, result))

	return errors.Join(runErr, written)
}

// logHeld says, at -v, that a run stopped at a step waiting for a later release.
func logHeld(opts *options, held *engine.Held) {
	if held == nil {
		return
	}

	opts.log.Info("a step waits for a later release, so the run stops before it",
		"migration", held.Migration.Filename(), "change", held.Phase.Change, "phase", held.Phase.Name,
		"after", held.Prior.Filename())
}

// previewDirection reports what the direction would run, having taken the lock and executed nothing.
func previewDirection(cmd *cobra.Command, opts *options, s *session, dir ir.Direction) error {
	previewed, err := s.preview(cmd.Context(), dir)
	if err != nil {
		return err
	}

	return writeResult(cmd.OutOrStdout(), opts, planOf(s, previewed))
}
