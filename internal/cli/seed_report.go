package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/remedy"
)

// newSeedStatusCmd builds `migr seed status`, the seed history's own reconciliation.
func newSeedStatusCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what each seed reconciled to",
		Long: "Folds this database's seed history against the seeds this environment runs and\n" +
			"prints one state per seed. Like migr status it takes no lock and writes nothing, and\n" +
			"it lists no migration -- migr status lists those and no seed.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSeedStatus(cmd, opts)
		},
	}
}

// newSeedHistoryCmd builds `migr seed history`, the seed history's own execution log.
func newSeedHistoryCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "Show every seed run recorded against this database",
		Long: "Prints every row of this database's seed history, oldest first. Like migr history\n" +
			"it takes no lock and writes nothing.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSeedHistory(cmd, opts)
		},
	}
}

// newSeedDownCmd builds `migr seed down`, which rolls seeds back without touching a migration.
func newSeedDownCmd(opts *options) *cobra.Command {
	narrow := &narrowing{}

	cmd := &cobra.Command{
		Use:   "down",
		Short: "Roll back applied seeds, newest first",
		Long: "Rolls back the last seed batch, or what --step and --to name, newest first, under\n" +
			"the migration lock. Every migration must be applied, since a seed's down is written\n" +
			"against the schema they leave. No migration is rolled back.",
		Args: argsOf(cobra.NoArgs),
	}

	f := cmd.Flags()
	f.Int32Var(&narrow.step, "step", 0, "roll back the last N applied seeds")
	f.StringVar(&narrow.to, "to", "", "leave the seeds at this version")
	f.BoolVar(&narrow.dry, "dry-run", false, "report what would run, and execute none of it")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runSeedDirection(cmd, opts, *narrow, ir.Down)
	}

	return cmd
}

// newSeedResolveCmd builds `migr seed resolve`, which settles a seed a stopped run left between states.
func newSeedResolveCmd(opts *options) *cobra.Command {
	ask := &asking{}

	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Settle a seed left between states",
		Long: "Resolves the one seed a stopped run left between states, as migr resolve does for a\n" +
			"migration: --resume runs on from where it stopped, and --applied or --rolled-back\n" +
			"record what you did yourself. It prints what it is about to change before changing\n" +
			"it, and --dry-run prints only.",
		Args: argsOf(cobra.NoArgs),
	}

	ask.register(cmd)

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runSeedResolve(cmd, opts, *ask)
	}

	return cmd
}

// runSeedStatus reconciles this environment's seeds against the seed history and prints the result.
func runSeedStatus(cmd *cobra.Command, opts *options) (err error) {
	s, set, files, err := reportingSeeds(cmd.Context(), opts)
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

	status := statusOf(s.database, set.all, report, cps)
	status.Environment, status.about = set.environment, seedReports

	return writeResult(cmd.OutOrStdout(), opts, status)
}

// runSeedHistory prints the seed history's rows, which reach no file and start no runtime.
func runSeedHistory(cmd *cobra.Command, opts *options) (err error) {
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

	s.conn = seeding{Conn: s.conn}

	log, err := s.log(cmd.Context())
	if err != nil {
		return err
	}

	written := logOf(s.database, log)
	written.Environment, written.about = t.db.Environment, seedReports

	return writeResult(cmd.OutOrStdout(), opts, written)
}

// reportingSeeds connects a report to the seed history and hashes the seeds, starting no runtime.
func reportingSeeds(ctx context.Context, opts *options) (*session, seedSet, []history.File, error) {
	t, err := resolve(opts)
	if err != nil {
		return nil, seedSet{}, nil, err
	}

	set, err := t.seeds(opts)
	if err != nil {
		return nil, seedSet{}, nil, err
	}

	files, err := set.files()
	if err != nil {
		return nil, seedSet{}, nil, err
	}

	s, err := t.connect(ctx, opts)
	if err != nil {
		return nil, seedSet{}, nil, err
	}

	s.conn = seeding{Conn: s.conn}

	return s, set, files, nil
}

// files hashes every seed in version order, each read from the directory it was found in.
func (set seedSet) files() ([]history.File, error) {
	byVersion := make(map[string]history.File, len(set.all))

	for _, one := range set.shelves {
		hashes, err := hashed(one.at.fsys, one.found)
		if err != nil {
			return nil, err
		}

		for i, m := range one.found {
			byVersion[m.Version] = hashes[i]
		}
	}

	files := make([]history.File, 0, len(set.all))
	for _, m := range set.all {
		files = append(files, byVersion[m.Version])
	}

	return files, nil
}

// runSeedResolve settles the seed this database was left between states on, or reports that none is.
func runSeedResolve(cmd *cobra.Command, opts *options, ask asking) (err error) {
	repair, err := ask.repair()
	if err != nil {
		return NewExitError(ExitValidation, wordedForSeeds(err))
	}

	if err := repair.Check(); err != nil {
		return NewExitError(ExitValidation, err)
	}

	s, set, err := settlingSeeds(cmd.Context(), opts, repair)
	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, s.close(context.WithoutCancel(cmd.Context())))
	}()

	settled, err := engine.Resolve(cmd.Context(), s.conn, s.options, repair)
	if err != nil {
		return seedFailure(err)
	}

	resolved := resolutionOf(s.database, repair, settled)
	resolved.Environment, resolved.about = set.environment, seedReports

	return writeResult(cmd.OutOrStdout(), opts, resolved)
}

// settlingSeeds opens what a seed repair needs: a resume runs seeds, an asserted repair only records.
func settlingSeeds(ctx context.Context, opts *options, repair engine.Repair,
) (*session, seedSet, error) {
	if !repair.Mode.Asserts() {
		return openSeeds(ctx, opts, engine.Scope{})
	}

	s, set, files, err := reportingSeeds(ctx, opts)
	if err != nil {
		return nil, seedSet{}, err
	}

	s.options.Migrations = files
	s.options.Clock = opts.clock

	return s, set, nil
}

// seedWords are the engine's own sentences naming a migration command, and the seed command each means.
var seedWords = strings.NewReplacer(
	"migr resolve", "migr seed resolve",
	"migr status and migr history", "migr seed status and migr seed history",
	"The next migr up", "The next migr seed",
	"migr down refuses", "migr seed down refuses",
	"Neither up nor down runs", "Neither migr seed nor migr seed down runs",
	"Both up and down refuse", "Both migr seed and migr seed down refuse",
	"an applied migration has been edited since it was applied", "an applied seed has been edited since it ran",
	"this migration", "this seed",
	"about the migration", "about the seed",
	"a migration may branch", "a seed may branch",
)

// seedNexts are the engine's own remedies naming a migration command, and the seed command each means.
var seedNexts = strings.NewReplacer(
	"migr resolve", "migr seed resolve",
	"migr down --to", "migr seed down --to",
	"migr status", "migr seed status",
	"run migr up again to carry on", "run migr seed again to carry on",
	"make the change in a new migration with migr create", "make the change in a new seed",
	"author a down for this migration", "author a down for this seed",
	"the migration between states", "the seed between states",
)

// seedWorded is a seed run's failure, its prose naming the seed commands where the engine names its own.
type seedWorded struct {
	// err is the failure, which errors.Is and errors.As still reach.
	err error
}

// wordedForSeeds wraps a failure so its prose names the seed commands, leaving nil as nil.
func wordedForSeeds(err error) error {
	if err == nil {
		return nil
	}

	return seedWorded{err: err}
}

// Error renders the failure with every migration command it names spelled as its seed one.
func (w seedWorded) Error() string {
	return seedWords.Replace(w.err.Error())
}

// Next renders the failure's remedy with every migration command it names spelled as its seed one.
func (w seedWorded) Next() string {
	return seedNexts.Replace(remedy.Of(w.err))
}

// Unwrap reports the failure itself, so a failure document still finds the fields it is built from.
func (w seedWorded) Unwrap() error {
	return w.err
}
