package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/source/sql"
	"github.com/emrebdr/migr/internal/validate"
)

// errNoDrivers marks a build that reached a database command with no drivers registered at all.
var errNoDrivers = errors.New("this build registered no database drivers")

// errNoRuntimes marks a build that reached a language command with no runtimes registered at all.
var errNoRuntimes = errors.New("this build registered no language runtimes")

// executable names the task that first runs each language end to end, for the ones nothing runs yet.
var executable = map[config.Language]string{
	config.LanguageTypeScript: "M7-T8",
}

// target is the one database a command reaches, resolved from the config and reaching nothing yet.
type target struct {
	// cfg is the whole resolved configuration, defaults applied and secrets expanded.
	cfg *config.Config
	// root is the directory holding migr.yaml, which docs/protocol.md §9 gives a runtime to work in.
	root string
	// name is the key migr.yaml holds this database under.
	name string
	// db is what that key resolved to, DSN included.
	db config.Database
	// fsys is the migrations directory, which a command reads only if it needs the files.
	fsys fs.FS
}

// closer ends whatever a source started, and is nil for a source that started nothing.
type closer func(ctx context.Context) error

// session is one open database, and the name a report or a run prints it under.
type session struct {
	// conn is the pinned session every statement and every history read goes through.
	conn driver.Conn
	// stop ends the runtime process this session's source is talking to, nil when there is none.
	stop closer
	// database is the name migr.yaml holds this target under.
	database string
	// options is what the engine was given beyond that connection, bare for a command that runs nothing.
	options engine.Options
}

// resolve loads the config and names the one database this invocation targets, connecting to nothing.
func resolve(opts *options) (*target, error) {
	path := config.Resolve(opts.configPath)

	cfg, err := config.Load(path)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	name, db, err := cfg.Target(opts.database)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolving the directory holding %s: %w", path, err)
	}

	return &target{cfg: cfg, root: root, name: name, db: db,
		fsys: os.DirFS(cfg.Migrations.Dir)}, nil
}

// discovered reads the migrations directory and orders it, without reading a file's contents.
func (t *target) discovered(opts *options) ([]migration.Migration, error) {
	found, err := discover(t.fsys)
	if err != nil {
		return nil, unreadable(err)
	}

	opts.log.Debug("discovered migrations", "dir", t.cfg.Migrations.Dir, "migrations", len(found))

	return found, nil
}

// unreadable separates a directory migr went looking for from the files it found and refused.
func unreadable(err error) error {
	if errors.Is(err, migration.ErrNoDirectory) {
		return err
	}

	return NewExitError(ExitValidation, err)
}

// files discovers the migrations directory, orders it, and hashes what each file currently holds.
func (t *target) files(opts *options) ([]history.File, error) {
	found, err := t.discovered(opts)
	if err != nil {
		return nil, err
	}

	return hashed(t.fsys, found)
}

// source returns what reads this project's migrations, and what ends it when the command is done.
func (t *target) source(ctx context.Context, opts *options,
	conn driver.Conn) (engine.Source, closer, error) {
	lang := t.cfg.Migrations.Language

	if lang == config.LanguageSQL {
		return sql.New(t.fsys, t.db.Driver), nil, nil
	}

	adapter, err := runnable(lang, opts.runtimes)
	if err != nil {
		return nil, nil, NewExitError(ExitValidation, err)
	}

	return t.start(ctx, opts, conn, adapter)
}

// offlineSource returns what reads this project's migrations over a session that opens no database.
func (t *target) offlineSource(ctx context.Context, opts *options) (engine.Source, closer, error) {
	conn, err := driver.Offline(t.db.Driver)
	if err != nil {
		return nil, nil, err
	}

	source, stop, err := t.source(ctx, opts, conn)
	if err != nil {
		return nil, nil, err
	}

	return checking{source: source, conn: conn}, ending(stop), nil
}

// checking is a source that reports a half whose answer a run with no database cannot trust.
type checking struct {
	// source is what actually reads this project's migrations.
	source engine.Source
	// conn is the session it reaches, which refuses everything and remembers being asked.
	conn *driver.OfflineConn
}

// Emit asks one half, reporting one that needed a database this run deliberately did not open.
func (c checking) Emit(ctx context.Context, m migration.Migration, dir ir.Direction,
	mode engine.Mode) (engine.Emitted, error) {
	c.conn.Forget()

	emitted, err := c.source.Emit(ctx, m, dir, mode)
	if c.conn.Reached() {
		return engine.Emitted{}, fmt.Errorf("%w: it reaches the database, and %w",
			validate.ErrNotCheckable, driver.ErrNoDatabase)
	}

	return emitted, err
}

// ending renders a source's close as one a caller may always defer, since a file starts nothing.
func ending(stop closer) closer {
	if stop == nil {
		return func(context.Context) error { return nil }
	}

	return stop
}

// start spawns the runtime that reads this project's migrations and shakes hands with it.
func (t *target) start(ctx context.Context, opts *options, conn driver.Conn,
	adapter runtime.Runtime) (engine.Source, closer, error) {
	project, err := t.project()
	if err != nil {
		return nil, nil, err
	}

	session, err := runtime.Start(ctx, runtime.Options{
		Runtime: adapter, Project: project, Conn: conn, Log: opts.log,
		Timeouts: t.timeouts(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("starting the %s runtime: %w", adapter.Name(), err)
	}

	return session, session.Close, nil
}

// project describes this run to a runtime, which is told where it is and never what the DSN is.
func (t *target) project() (runtime.Project, error) {
	dir, err := filepath.Abs(t.cfg.Migrations.Dir)
	if err != nil {
		return runtime.Project{}, fmt.Errorf("resolving %s: %w", t.cfg.Migrations.Dir, err)
	}

	return runtime.Project{
		Root:     t.root,
		Dir:      dir,
		Language: string(t.cfg.Migrations.Language),
		Database: t.name,
		Driver:   t.db.Driver,
	}, nil
}

// timeouts renders §8.2's bounds as migr.yaml resolved them, which applyDefaults has already filled.
func (t *target) timeouts() runtime.Timeouts {
	return runtime.Timeouts{
		Handshake:     t.cfg.Runtime.HandshakeTimeout.Duration(),
		Request:       t.cfg.Runtime.RequestTimeout.Duration(),
		CancelGrace:   t.cfg.Runtime.CancelGrace.Duration(),
		ShutdownGrace: t.cfg.Runtime.ShutdownGrace.Duration(),
	}
}

// connect opens the one pinned session everything this command does goes through.
func (t *target) connect(ctx context.Context, opts *options) (*session, error) {
	d, conn, err := dial(ctx, opts, t)
	if err != nil {
		return nil, err
	}

	opts.log.Info("connected", "database", t.name, "driver", t.db.Driver,
		"server", conn.ServerVersion())

	return &session{conn: conn, database: t.name, options: engine.Options{
		Planner: plan.Planner{Compiler: d},
	}}, nil
}

// open resolves the target, discovers the migrations, and opens a session ready to run a direction.
func open(ctx context.Context, opts *options, scope engine.Scope) (*session, error) {
	t, err := resolve(opts)
	if err != nil {
		return nil, err
	}

	files, err := t.files(opts)
	if err != nil {
		return nil, err
	}

	s, err := t.connect(ctx, opts)
	if err != nil {
		return nil, err
	}

	if err := s.attach(ctx, t, opts, files, scope); err != nil {
		return nil, errors.Join(err, s.close(context.WithoutCancel(ctx)))
	}

	return s, nil
}

// attach gives this session what reads the migrations, which needs the connection it reaches.
func (s *session) attach(ctx context.Context, t *target, opts *options,
	files []history.File, scope engine.Scope) error {
	source, stop, err := t.source(ctx, opts, s.conn)
	if err != nil {
		return err
	}

	s.stop = stop
	s.options.Source = source
	s.options.Migrations = files
	s.options.Clock = opts.clock
	s.options.Runtime = string(t.cfg.Migrations.Language)
	s.options.Scope = scope

	return nil
}

// close ends the runtime before giving the connection back, since its callbacks reach that connection.
func (s *session) close(ctx context.Context) error {
	ended := s.end(ctx)

	return errors.Join(ended, s.conn.Close(ctx))
}

// end stops the runtime process this session started, and reports nothing for a source that is a file.
func (s *session) end(ctx context.Context) error {
	if s.stop == nil {
		return nil
	}

	return s.stop(ctx)
}

// run performs one direction against this database, returning what it did alongside what stopped it.
func (s *session) run(ctx context.Context, dir ir.Direction) (engine.Result, error) {
	result, err := s.perform(ctx, dir)

	return result, selected(err)
}

// perform routes a direction to the entry point that runs it.
func (s *session) perform(ctx context.Context, dir ir.Direction) (engine.Result, error) {
	if dir == ir.Down {
		return engine.Rollback(ctx, s.conn, s.options)
	}

	return engine.Apply(ctx, s.conn, s.options)
}

// preview reports what one direction would run against this database, having executed none of it.
func (s *session) preview(ctx context.Context, dir ir.Direction) (engine.Previewed, error) {
	previewed, err := engine.Preview(ctx, s.conn, s.options, dir)

	return previewed, selected(err)
}

// log reads this database's history through its read half alone, which is all a report may be given.
func (s *session) log(ctx context.Context) (history.Log, error) {
	return history.Read(ctx, reader(s.conn))
}

// reconcile folds this database's history against the migrations on disk, taking no lock at all.
func (s *session) reconcile(ctx context.Context, files []history.File) (history.Report, error) {
	log, err := s.log(ctx)
	if err != nil {
		return history.Report{}, err
	}

	return history.Reconcile(files, log), nil
}

// reader narrows a session's store to the half a report may use, so no report can write or Ensure.
func reader(conn driver.Conn) history.Reader {
	return conn.History()
}

// discover reads the migrations directory and puts it in the order a run would perform it.
func discover(fsys fs.FS) ([]migration.Migration, error) {
	found, err := migration.Discover(fsys)
	if err != nil {
		return nil, err
	}

	return migration.Order(found, nil)
}

// hashed pairs each discovered migration with the digest of the bytes its file currently holds.
func hashed(fsys fs.FS, found []migration.Migration) ([]history.File, error) {
	files := make([]history.File, 0, len(found))

	for _, m := range found {
		src, err := fs.ReadFile(fsys, m.Filename())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", m.Filename(), err)
		}

		files = append(files, history.NewFile(m, src))
	}

	return files, nil
}

// runnable returns the adapter that runs one language, or why nothing can run it here.
func runnable(lang config.Language, runtimes *runtime.Registry) (runtime.Runtime, error) {
	if !slices.Contains(config.Languages(), lang) {
		return nil, fmt.Errorf("migrations.language: %q is not a language migr knows (expected: %s)",
			lang, joinLanguages(config.Languages()))
	}

	if runtimes == nil {
		return nil, fmt.Errorf("migrations.language: %s: %w", lang, errNoRuntimes)
	}

	adapter, err := runtimes.New(string(lang))
	if err != nil {
		return nil, fmt.Errorf("migrations.language: %s: %w", lang, err)
	}

	if task, waiting := executable[lang]; waiting {
		return nil, fmt.Errorf("migr has a %s runtime adapter and the SDK it runs, and nothing has "+
			"run one end to end yet: that is %s. migrations.language: %s and %s are the formats "+
			"migr runs today", lang, task, config.LanguageSQL, config.LanguageGo)
	}

	return adapter, nil
}

// compiler builds the driver migr.yaml names, which needs no DSN and reaches no database.
func (t *target) compiler(opts *options) (driver.Driver, error) {
	if opts.drivers == nil {
		return nil, errNoDrivers
	}

	d, err := opts.drivers.New(t.db.Driver, t.cfg.DriverOptions(t.db))
	if err != nil {
		return nil, NewExitError(ExitValidation, fmt.Errorf("databases.%s: %w", t.name, err))
	}

	return d, nil
}

// dial builds the driver migr.yaml names and opens a connection to the database it points at.
func dial(ctx context.Context, opts *options, t *target) (driver.Driver, driver.Conn, error) {
	d, err := t.compiler(opts)
	if err != nil {
		return nil, nil, err
	}

	conn, err := d.Open(ctx, t.db.DSN.Reveal())
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to database %q: %w", t.name, err)
	}

	return d, conn, nil
}
