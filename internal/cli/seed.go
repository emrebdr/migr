package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/remedy"
)

// errSchemaInSeed marks a seed that emitted a schema change, which belongs in a migration instead.
var errSchemaInSeed = errors.New("a seed changes data, and this one changes the schema")

// errNoSeedsDir marks a seeds directory that is not there, which leaves nothing to seed.
var errNoSeedsDir = errors.New("the seeds directory does not exist")

// seedKinds are the operations a seed may emit: raw, and the data operations irVersion 2 added.
var seedKinds = append([]ir.Kind{ir.KindRaw}, ir.DataKinds()...)

// newSeedCmd builds the `migr seed` subcommand.
func newSeedCmd(opts *options) *cobra.Command {
	narrow := &narrowing{}

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Apply pending seeds for this database's environment",
		Long: "Applies every pending seed in seeds.dir, and in the subdirectory named for this\n" +
			"database's environment, under the migration lock, recording each one in a history of\n" +
			"its own. Every migration must already be applied: a seed runs against the schema the\n" +
			"migrations leave, never between two of them.",
		Args: argsOf(cobra.NoArgs),
	}

	f := cmd.Flags()
	f.Int32Var(&narrow.step, "step", 0, "apply the first N pending seeds")
	f.StringVar(&narrow.to, "to", "", "leave the seeds at this version")
	f.BoolVar(&narrow.dry, "dry-run", false, "report what would run, and execute none of it")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runSeed(cmd, opts, *narrow)
	}

	cmd.AddCommand(newSeedStatusCmd(opts), newSeedHistoryCmd(opts), newSeedDownCmd(opts),
		newSeedResolveCmd(opts))

	return cmd
}

// runSeed checks the schema is settled and then applies or previews the pending seeds against it.
func runSeed(cmd *cobra.Command, opts *options, narrow narrowing) error {
	return runSeedDirection(cmd, opts, narrow, ir.Up)
}

// runSeedDirection applies or rolls back seeds, or previews either, once every migration has run.
func runSeedDirection(cmd *cobra.Command, opts *options, narrow narrowing,
	dir ir.Direction) (err error) {
	scope, err := narrow.scope(dir)
	if err != nil {
		return NewExitError(ExitValidation, err)
	}

	s, set, err := openSeeds(cmd.Context(), opts, scope)
	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, s.close(context.WithoutCancel(cmd.Context())))
	}()

	if narrow.dry {
		return previewSeeds(cmd, opts, s, set, dir)
	}

	return performSeeds(cmd, opts, s, set, dir)
}

// openSeeds connects, refuses a database any migration has not run against, and attaches the seeds.
func openSeeds(ctx context.Context, opts *options, scope engine.Scope) (*session, seedSet, error) {
	t, err := resolve(opts)
	if err != nil {
		return nil, seedSet{}, err
	}

	set, err := t.seeds(opts)
	if err != nil {
		return nil, seedSet{}, err
	}

	_, files, err := t.files(opts)
	if err != nil {
		return nil, seedSet{}, err
	}

	s, err := t.connect(ctx, opts)
	if err != nil {
		return nil, seedSet{}, err
	}

	if err := s.checkMigrated(ctx, files); err != nil {
		return nil, seedSet{}, errors.Join(err, s.close(context.WithoutCancel(ctx)))
	}

	if err := s.attachSeeds(ctx, opts, set, scope); err != nil {
		return nil, seedSet{}, errors.Join(err, s.close(context.WithoutCancel(ctx)))
	}

	s.conn = seeding{Conn: s.conn}

	return s, set, nil
}

// shelf is one directory seeds are read from, and the seeds discovery found in it.
type shelf struct {
	// at is the target reading that directory, so its runtimes are started there.
	at *target
	// found is the seeds it holds, in version order.
	found []migration.Migration
}

// seedSet is every seed this database's environment runs, by the directory each one came from.
type seedSet struct {
	// environment is the one this database runs under, which names the second directory.
	environment string
	// common is the directory every environment runs.
	common string
	// own is the directory only this environment runs, which may not exist.
	own string
	// shelves are the directories that held seeds, the common one first.
	shelves []shelf
	// all is every seed across them, in version order and each version once.
	all []migration.Migration
}

// seeds discovers the common seeds and this environment's own, refusing a directory no environment names.
func (t *target) seeds(opts *options) (seedSet, error) {
	env := t.db.Environment
	set := seedSet{environment: env, common: t.cfg.Seeds.Dir,
		own: filepath.Join(t.cfg.Seeds.Dir, env)}

	common, err := discoverCommon(set.common, t.cfg.Environments())
	if err != nil {
		return seedSet{}, err
	}

	set.shelves = []shelf{{at: t.at(set.common), found: common}}

	own, err := discoverOwn(set.own)
	if err != nil {
		return seedSet{}, err
	}

	if own != nil {
		set.shelves = append(set.shelves, shelf{at: t.at(set.own), found: own})
	}

	if set.all, err = migration.Merge(common, own); err != nil {
		return seedSet{}, NewExitError(ExitValidation, fmt.Errorf("%s and %s: %w", set.common,
			set.own, err))
	}

	if err := checkRoutable(set.all); err != nil {
		return seedSet{}, err
	}

	opts.log.Debug("discovered seeds", "dir", set.common, "environment", env,
		"common", len(common), "own", len(own))

	return set, nil
}

// discoverCommon reads the seeds every environment runs, passing over each environment's directory.
func discoverCommon(dir string, environments []string) ([]migration.Migration, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, remedy.With(fmt.Errorf("%w: seeds.dir is %s, so there is nothing to seed",
			errNoSeedsDir, dir), "create the directory, or point seeds.dir in migr.yaml at the one that "+
			"holds the seeds")
	}

	if err != nil {
		return nil, remedy.With(fmt.Errorf("reading the seeds directory: %w", err),
			"make the seeds directory readable by the user migr runs as")
	}

	if err := checkEnvironments(dir, entries, environments); err != nil {
		return nil, err
	}

	found, err := migration.DiscoverBeside(os.DirFS(dir), environments)
	if err != nil {
		return nil, unreadable(err)
	}

	return found, nil
}

// checkEnvironments refuses a subdirectory no environment runs, which would hold seeds nothing ever runs.
func checkEnvironments(dir string, entries []fs.DirEntry, environments []string) error {
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") ||
			slices.Contains(environments, entry.Name()) {
			continue
		}

		return NewExitError(ExitValidation, remedy.With(fmt.Errorf("%s: a subdirectory of the seeds "+
			"directory holds the seeds of the environment it is named for, and no database in "+
			"migr.yaml runs under %q (environments: %s)", filepath.Join(dir, entry.Name()), entry.Name(),
			strings.Join(environments, ", ")), "rename the directory to one of those environments, or "+
			"set databases.<name>.environment in migr.yaml to its name"))
	}

	return nil
}

// discoverOwn reads the seeds only this environment runs, and nothing where it has no directory.
func discoverOwn(dir string) ([]migration.Migration, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, remedy.With(fmt.Errorf("reading %s: %w", dir, err),
			"make the directory readable by the user migr runs as")
	}

	if !info.IsDir() {
		return nil, nil
	}

	found, err := migration.Discover(os.DirFS(dir))
	if err != nil {
		return nil, unreadable(err)
	}

	return found, nil
}

// checkMigrated refuses a database every migration has not run against, since a seed runs after them all.
func (s *session) checkMigrated(ctx context.Context, files []history.File) error {
	report, err := s.reconcile(ctx, files)
	if err != nil {
		return err
	}

	resolved, present := report.Resolved()
	if !present {
		return absentHistory(len(files))
	}

	unsettled := map[history.State][]string{}

	for _, one := range resolved {
		if one.State != history.StateApplied {
			unsettled[one.State] = append(unsettled[one.State], describeUnsettled(one))
		}
	}

	for _, state := range history.States() {
		if named := unsettled[state]; len(named) > 0 {
			return remedy.With(fmt.Errorf("this database is not ready to seed: %d %s (%s), and a seed "+
				"runs against the schema every migration leaves", len(named), state,
				strings.Join(named, ", ")), nextFor(state))
		}
	}

	return nil
}

// absentHistory refuses a database no migration has run against, unless the project has none to run.
func absentHistory(migrations int) error {
	if migrations == 0 {
		return nil
	}

	return remedy.With(fmt.Errorf("this database is not ready to seed -- none of the %d migrations has "+
		"run, and a seed runs against the schema every migration leaves, never between two of them: %w",
		migrations, history.ErrNoTable), "run migr up first")
}

// attachSeeds gives this session what reads the seeds, in their own order and against their own table.
func (s *session) attachSeeds(ctx context.Context, opts *options, set seedSet,
	scope engine.Scope) error {
	routes := make(map[string]engine.Source, len(set.all))
	digests := make(map[string]history.File, len(set.all))
	declared := make(migration.Declared, len(set.all))

	var stops []closer

	for _, one := range set.shelves {
		if len(one.found) == 0 {
			continue
		}

		read, err := one.at.sources(ctx, opts, s.conn, one.found)
		if err != nil {
			return errors.Join(err, closeAll(ctx, stops))
		}

		if read.stop != nil {
			stops = append(stops, read.stop)
		}

		if err := shelve(one, read, routes, digests); err != nil {
			return errors.Join(err, closeAll(ctx, stops))
		}

		merge(declared, read.declared)
	}

	s.stop = allEnding(stops)

	ordered, err := overlaid(opts, set.all, declared)
	if err != nil {
		return err
	}

	files := make([]history.File, 0, len(ordered))
	for _, m := range ordered {
		files = append(files, digests[m.Version])
	}

	s.options.Source = dataOnly{source: shelved{byVersion: routes}}
	s.options.Migrations = files
	s.options.Clock = opts.clock
	s.options.Runtime = runtimeOf
	s.options.Scope = scope
	s.options.Allow = engine.Allowance{Kinds: seedKinds}

	return nil
}

// shelve records where each seed of one directory is read from and what its file hashes to.
func shelve(one shelf, read reading, routes map[string]engine.Source,
	digests map[string]history.File) error {
	files, err := hashed(one.at.fsys, one.found)
	if err != nil {
		return err
	}

	for i, m := range one.found {
		routes[m.Version] = read.source
		digests[m.Version] = files[i]
	}

	return nil
}

// shelved routes each seed to the source reading the directory it was found in.
type shelved struct {
	// byVersion holds that source per seed, since discovery proved each version unique across both.
	byVersion map[string]engine.Source
}

// Emit asks whichever source reads the directory this seed was found in.
func (s shelved) Emit(ctx context.Context, m migration.Migration, dir ir.Direction,
	mode engine.Mode) (engine.Emitted, error) {
	source, found := s.byVersion[m.Version]
	if !found {
		return engine.Emitted{}, fmt.Errorf("%s: no seeds directory holds it", m.Filename())
	}

	return source.Emit(ctx, m, dir, mode)
}

// dataOnly is a source that refuses a seed emitting anything but data, which no migration ever reads.
type dataOnly struct {
	// source is what actually reads the seeds.
	source engine.Source
}

// Emit asks one seed what it does, refusing it where any of that is a schema change.
func (d dataOnly) Emit(ctx context.Context, m migration.Migration, dir ir.Direction,
	mode engine.Mode) (engine.Emitted, error) {
	emitted, err := d.source.Emit(ctx, m, dir, mode)
	if err != nil {
		return emitted, err
	}

	for i, op := range emitted.Operations {
		if slices.Contains(seedKinds, op.Kind()) {
			continue
		}

		return engine.Emitted{}, fmt.Errorf("%s: operation %d is %s: %w -- a schema change "+
			"belongs in a migration, since seeds never mix with them (a seed may emit: %s)",
			m.Filename(), i+1, op.Kind(), errSchemaInSeed, joinKinds(seedKinds))
	}

	return emitted, nil
}

// seeding is a session whose history is the seed table, under the lock and transactions it already has.
type seeding struct {
	driver.Conn
}

// History returns the seed history, which is the whole of what makes a seed run not a migration run.
func (s seeding) History() history.Store {
	return s.Seeds()
}

// performSeeds runs seeds one way and reports what it did, even when also reporting a failure.
func performSeeds(cmd *cobra.Command, opts *options, s *session, set seedSet,
	dir ir.Direction) error {
	result, runErr := s.perform(cmd.Context(), dir)
	runErr = seedFailure(runErr)

	if runErr != nil && len(result.Runs) == 0 {
		return runErr
	}

	summary := seedSummaryOf(summaryOf(s.database, dir, result), set.environment)

	return errors.Join(runErr, writeResult(cmd.OutOrStdout(), opts, summary))
}

// previewSeeds reports what running seeds one way would do, having taken the lock and executed nothing.
func previewSeeds(cmd *cobra.Command, opts *options, s *session, set seedSet,
	dir ir.Direction) error {
	previewed, err := engine.Preview(cmd.Context(), s.conn, s.options, dir)
	if err != nil {
		return seedFailure(err)
	}

	return writeResult(cmd.OutOrStdout(), opts, seedPlanOf(planOf(s, previewed), set.environment))
}

// seedFailure tags a seed that changes the schema, and a flag naming no seed, as the argument being wrong.
func seedFailure(err error) error {
	if errors.Is(err, errSchemaInSeed) {
		return NewExitError(ExitValidation, err)
	}

	return selected(wordedForSeeds(err))
}
