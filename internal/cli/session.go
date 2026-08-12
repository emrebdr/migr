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

// session is one open database and everything an apply or a rollback needs to run against it.
type session struct {
	// conn is the pinned session every statement and every history write goes through.
	conn driver.Conn
	// database is the name migr.yaml holds this target under.
	database string
	// options is what the engine was given beyond that connection.
	options engine.Options
}

// open resolves the config, discovers the migrations, and connects to the database a command targets.
func open(ctx context.Context, opts *options, scope engine.Scope) (*session, error) {
	cfg, err := config.Load(config.Resolve(opts.configPath))
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	name, db, err := cfg.Target(opts.database)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	fsys := os.DirFS(cfg.Migrations.Dir)

	files, err := discover(fsys)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	source, err := sourceFor(cfg.Migrations.Language, fsys, db.Driver)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	d, conn, err := connect(ctx, opts, cfg, name, db)
	if err != nil {
		return nil, err
	}

	opts.log.Info("connected", "database", name, "driver", db.Driver,
		"server", conn.ServerVersion(), "migrations", len(files))

	return &session{conn: conn, database: name, options: engine.Options{
		Planner:    plan.Planner{Compiler: d},
		Source:     source,
		Migrations: files,
		Clock:      opts.clock,
		Runtime:    string(cfg.Migrations.Language),
		Scope:      scope,
	}}, nil
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

// discover reads the migrations directory, orders it, and hashes what each file currently holds.
func discover(fsys fs.FS) ([]history.File, error) {
	found, err := migration.Discover(fsys)
	if err != nil {
		return nil, err
	}

	ordered, err := migration.Order(found, nil)
	if err != nil {
		return nil, err
	}

	files := make([]history.File, 0, len(ordered))

	for _, m := range ordered {
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

// connect builds the driver migr.yaml names and opens the one pinned session everything runs through.
func connect(ctx context.Context, opts *options, cfg *config.Config, name string,
	db config.Database) (driver.Driver, driver.Conn, error) {
	if opts.drivers == nil {
		return nil, nil, errNoDrivers
	}

	d, err := opts.drivers.New(db.Driver, cfg.DriverOptions(db))
	if err != nil {
		return nil, nil, NewExitError(ExitValidation, fmt.Errorf("databases.%s: %w", name, err))
	}

	conn, err := d.Open(ctx, db.DSN.Reveal())
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to database %q: %w", name, err)
	}

	return d, conn, nil
}
