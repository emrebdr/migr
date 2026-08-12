package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/source/sql"
)

// errNoDrivers marks a build that reached a database command with no drivers registered at all.
var errNoDrivers = errors.New("this build registered no database drivers")

// executable names the task that first runs each language end to end, for the ones nothing runs yet.
var executable = map[config.Language]string{
	config.LanguageGo:         "M6-T10",
	config.LanguageTypeScript: "M7-T8",
}

// target is the one database a command reaches, resolved from the config and reaching nothing yet.
type target struct {
	// cfg is the whole resolved configuration, defaults applied and secrets expanded.
	cfg *config.Config
	// name is the key migr.yaml holds this database under.
	name string
	// db is what that key resolved to, DSN included.
	db config.Database
	// fsys is the migrations directory, which a command reads only if it needs the files.
	fsys fs.FS
}

// session is one open database, and the name a report or a run prints it under.
type session struct {
	// conn is the pinned session every statement and every history read goes through.
	conn driver.Conn
	// database is the name migr.yaml holds this target under.
	database string
	// options is what the engine was given beyond that connection, bare for a command that runs nothing.
	options engine.Options
}

// resolve loads the config and names the one database this invocation targets, connecting to nothing.
func resolve(opts *options) (*target, error) {
	cfg, err := config.Load(config.Resolve(opts.configPath))
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	name, db, err := cfg.Target(opts.database)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	return &target{cfg: cfg, name: name, db: db, fsys: os.DirFS(cfg.Migrations.Dir)}, nil
}

// discovered reads the migrations directory and orders it, without reading a file's contents.
func (t *target) discovered(opts *options) ([]migration.Migration, error) {
	found, err := discover(t.fsys)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	opts.log.Debug("discovered migrations", "dir", t.cfg.Migrations.Dir, "migrations", len(found))

	return found, nil
}

// files discovers the migrations directory, orders it, and hashes what each file currently holds.
func (t *target) files(opts *options) ([]history.File, error) {
	found, err := t.discovered(opts)
	if err != nil {
		return nil, err
	}

	files, err := hashed(t.fsys, found)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	return files, nil
}

// source returns what reads this project's migrations, or why nothing can run them yet.
func (t *target) source() (engine.Source, error) {
	source, err := sourceFor(t.cfg.Migrations.Language, t.fsys, t.db.Driver)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	return source, nil
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

	source, err := t.source()
	if err != nil {
		return nil, err
	}

	s, err := t.connect(ctx, opts)
	if err != nil {
		return nil, err
	}

	s.options.Source = source
	s.options.Migrations = files
	s.options.Clock = opts.clock
	s.options.Runtime = string(t.cfg.Migrations.Language)
	s.options.Scope = scope

	return s, nil
}

// close gives the connection back, on a context that outlives whatever canceled the run.
func (s *session) close(ctx context.Context) error {
	return s.conn.Close(ctx)
}

// run performs one direction against this database, returning what it did alongside what stopped it.
func (s *session) run(ctx context.Context, dir ir.Direction) (engine.Result, error) {
	if dir == ir.Down {
		return engine.Rollback(ctx, s.conn, s.options)
	}

	return engine.Apply(ctx, s.conn, s.options)
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

// sourceFor returns what reads the migrations of one language, or why nothing can run them yet.
func sourceFor(lang config.Language, fsys fs.FS, driverName string) (engine.Source, error) {
	if lang == config.LanguageSQL {
		return sql.New(fsys, driverName), nil
	}

	if task, ok := executable[lang]; ok {
		return nil, fmt.Errorf("migr cannot run %s migrations yet: that SDK and its runtime "+
			"arrive in %s. migrations.language: %s is the format M5 runs",
			lang, task, config.LanguageSQL)
	}

	return nil, fmt.Errorf("migrations.language: %q is not a language migr knows (expected: %s)",
		lang, joinLanguages(config.Languages()))
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
