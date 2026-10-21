package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/rule"
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source/sql"
	"github.com/emrebdr/migr/internal/validate"
)

// errNoDrivers marks a build that reached a database command with no drivers registered at all.
var errNoDrivers = errors.New("this build registered no database drivers")

// errNoRuntimes marks a build that reached a language command with no runtimes registered at all.
var errNoRuntimes = errors.New("this build registered no language runtimes")

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
	// dir is the directory this target reads migrations from, which is migrations.dir unless seeding.
	dir string
	// fsys is that directory, which a command reads only if it needs the files.
	fsys fs.FS
}

// at returns this target reading another directory, which is how seeds reach the same machinery.
func (t *target) at(dir string) *target {
	moved := *t
	moved.dir = dir
	moved.fsys = os.DirFS(dir)

	return &moved
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
	// ceiling is safety.max_lock_impact, the most a preview lets an operation cost a live database.
	ceiling capability.LockImpact
	// logger is where a run warns of what it leaves behind it, such as an INVALID index.
	logger *slog.Logger
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
		dir: cfg.Migrations.Dir, fsys: os.DirFS(cfg.Migrations.Dir)}, nil
}

// discovered reads the migrations directory and orders it, without reading a file's contents.
func (t *target) discovered(opts *options) ([]migration.Migration, error) {
	found, err := discover(t.fsys)
	if err != nil {
		return nil, unreadable(err)
	}

	if err := checkRoutable(found); err != nil {
		return nil, err
	}

	opts.log.Debug("discovered migrations", "dir", t.dir, "migrations", len(found))

	return found, nil
}

// unreadable separates a directory migr went looking for from the files it found and refused.
func unreadable(err error) error {
	if errors.Is(err, migration.ErrNoDirectory) {
		return err
	}

	return NewExitError(ExitValidation, err)
}

// files discovers the migrations directory in version order, hashing what each file currently holds.
func (t *target) files(opts *options) ([]migration.Migration, []history.File, error) {
	found, err := t.discovered(opts)
	if err != nil {
		return nil, nil, err
	}

	hashes, err := hashed(t.fsys, found)
	if err != nil {
		return nil, nil, err
	}

	return found, hashes, nil
}

// checkRoutable reports a migration written in a language nothing here reads.
func checkRoutable(found []migration.Migration) error {
	for _, m := range found {
		if languageOf(m.Ext) != "" {
			continue
		}

		return NewExitError(ExitValidation, fmt.Errorf("%s: nothing here reads %s files, and a "+
			"migrations directory holds migrations one of them runs (%s) -- rename the file or "+
			"move it out of the directory", m.Filename(), m.Ext, routable()))
	}

	return nil
}

// mux routes each migration to the source that reads the language its own extension names.
type mux struct {
	// byExt holds one source per extension, since the file a migration is decides what reads it.
	byExt map[string]engine.Source
}

// Emit asks whichever source reads the language this migration's extension names.
func (m mux) Emit(ctx context.Context, mig migration.Migration, dir ir.Direction,
	mode engine.Mode) (engine.Emitted, error) {
	source, routed := m.byExt[mig.Ext]
	if !routed {
		return engine.Emitted{}, fmt.Errorf("%s: nothing here reads %s files", mig.Filename(), mig.Ext)
	}

	return source.Emit(ctx, mig, dir, mode)
}

// reading is what reads this project's migrations: the source, what ends it, and what it declared.
type reading struct {
	// source is what a run or a question asks each migration through.
	source engine.Source
	// stop ends every runtime process this reading started, and reports nothing for a file source.
	stop closer
	// declared is what each migration says it must follow, which only a started runtime reports.
	declared migration.Declared
	// phases is what each migration says it is a step of, which only a started runtime reports either.
	phases migration.Phases
	// online is how each migration says it runs against a live table, which only a runtime reports too.
	online migration.Online
}

// sources returns what reads every language this directory holds, what ends them, and what they declared.
func (t *target) sources(ctx context.Context, opts *options, conn driver.Conn,
	found []migration.Migration) (reading, error) {
	routed := routedByLanguage(found)
	built := mux{byExt: make(map[string]engine.Source, len(routed))}
	declared := migration.NewDeclarations()

	var stops []closer

	for _, lang := range config.Languages() {
		mine, needed := routed[lang]
		if !needed {
			continue
		}

		read, err := t.sourceFor(ctx, opts, conn, lang, mine)
		if err != nil {
			return reading{}, errors.Join(err, closeAll(ctx, stops))
		}

		if read.stop != nil {
			stops = append(stops, read.stop)
		}

		for _, ext := range extensions[lang] {
			built.byExt[ext] = read.source
		}

		declared.Merge(migration.Declarations{Dependencies: read.declared, Phases: read.phases,
			Online: read.online})
	}

	if err := declared.Online.Check(found); err != nil {
		return reading{}, errors.Join(NewExitError(ExitValidation, err), closeAll(ctx, stops))
	}

	return reading{source: built, stop: allEnding(stops), declared: declared.Dependencies,
		phases: declared.Phases, online: declared.Online}, nil
}

// merge folds one language's declarations into the run's, which the routes keep from colliding.
func merge(into, from migration.Declared) {
	for version, deps := range from {
		into[version] = deps
	}
}

// sourceFor returns what reads one language's migrations, starting a runtime where it needs one.
func (t *target) sourceFor(ctx context.Context, opts *options, conn driver.Conn,
	lang config.Language, mine []migration.Migration) (reading, error) {
	if !needsRuntime(lang) {
		return reading{source: sql.New(t.fsys, t.db.Driver)}, nil
	}

	adapter, err := runnable(lang, opts.runtimes)
	if err != nil {
		return reading{}, NewExitError(ExitValidation, err)
	}

	started, stop, err := t.start(ctx, opts, conn, lang, adapter)
	if err != nil {
		return reading{}, err
	}

	declared, err := checkLoaded(ctx, started, lang, mine)
	if err != nil {
		return reading{}, errors.Join(err, stop(context.WithoutCancel(ctx)))
	}

	return reading{source: started, stop: stop, declared: declared.Dependencies,
		phases: declared.Phases, online: declared.Online}, nil
}

// needsRuntime reports whether a language is read by a process rather than by reading a file here.
func needsRuntime(lang config.Language) bool {
	return lang != config.LanguageSQL
}

// routedByLanguage groups the discovered migrations by the language each one's extension names.
func routedByLanguage(found []migration.Migration) map[config.Language][]migration.Migration {
	routed := make(map[config.Language][]migration.Migration, len(config.Languages()))

	for _, m := range found {
		if lang := languageOf(m.Ext); lang != "" {
			routed[lang] = append(routed[lang], m)
		}
	}

	return routed
}

// checkLoaded holds what a runtime loaded to what was routed to it, and returns what it declared.
func checkLoaded(ctx context.Context, started *runtime.Session, lang config.Language,
	routed []migration.Migration) (migration.Declarations, error) {
	loaded, declared, err := started.Loaded(ctx)
	if err != nil {
		return migration.Declarations{}, err
	}

	if err := errors.Join(
		missingFrom(lang, routed, loaded),
		surplusIn(lang, routed, loaded),
	); err != nil {
		return migration.Declarations{}, err
	}

	return declared, nil
}

// missingFrom reports a migration routed to a runtime that the runtime did not load.
func missingFrom(lang config.Language, routed, loaded []migration.Migration) error {
	held := versions(loaded)

	for _, m := range routed {
		if !held[m.Version] {
			return fmt.Errorf("%s: the %s runtime did not load it, so nothing here can run it",
				m.Filename(), lang)
		}
	}

	return nil
}

// surplusIn reports a migration a runtime loaded that was never routed to it.
func surplusIn(lang config.Language, routed, loaded []migration.Migration) error {
	sent := versions(routed)

	for _, m := range loaded {
		if !sent[m.Version] {
			return fmt.Errorf("%s: the %s runtime loaded it and discovery routed it elsewhere, so "+
				"two runtimes claim one migration", m.Filename(), lang)
		}
	}

	return nil
}

// versions indexes migrations by the version discovery proved unique.
func versions(found []migration.Migration) map[string]bool {
	held := make(map[string]bool, len(found))
	for _, m := range found {
		held[m.Version] = true
	}

	return held
}

// closeAll ends every source that started, on a context whatever failed may already have canceled.
func closeAll(ctx context.Context, stops []closer) error {
	outliving := context.WithoutCancel(ctx)

	var ended error

	for _, stop := range slices.Backward(stops) {
		ended = errors.Join(ended, stop(outliving))
	}

	return ended
}

// allEnding renders every source's close as one a caller may always defer, newest first.
func allEnding(stops []closer) closer {
	return func(ctx context.Context) error {
		return closeAll(ctx, stops)
	}
}

// asked is what one session answered before it was closed, which is nothing for a run not connected.
type asked struct {
	// caps is what the server declares it can do, which only a connected run has (ADR 0064).
	caps *capability.Set
	// live is the schema that database holds now, which the reference check projects from (M10-T3).
	live *schema.Snapshot
	// rewriter is the session's safer sequences, which are pure and so outlive it (ADR 0085, 0086).
	rewriter driver.Rewriter
	// shadows reports a session that performs shadow-table migration, which a report states (ADR 0089).
	shadows bool
	// invalid are the indexes a failed concurrent build or drop left INVALID, which a report lists.
	invalid []invalidIndex
}

// Both reads happen on one session and it closes straight after, which is ADR 0064's rule unchanged:
// a verdict performs no I/O and a snapshot is a value, so neither outlives the connection. It takes
// no migration lock and reads no history (ADR 0027) -- this is a report, and the reference check is
// honest about a half-migrated database without being told how far it got.

// A driver that answers no snapshot and no error is carried as a run with no live schema rather than
// crashed over: the checks reading it already treat a missing one as a run that opened nothing, and
// the report names what it was able to project, so nothing claims a basis it did not have.

// logSchema records what the database answered, and that it answered nothing where it did.
func logSchema(opts *options, t *target, live *schema.Snapshot) {
	if live == nil {
		opts.log.Info("the driver reported no schema at all", "database", t.name,
			"driver", t.db.Driver)

		return
	}

	opts.log.Info("read the schema the database holds", "database", t.name,
		"tables", len(live.Tables), "views", len(live.Views))
}

// declared returns what the target database answered, and nothing for a run not asked to connect.
func (t *target) declared(ctx context.Context, opts *options) (asked, error) {
	if !opts.connect {
		return asked{}, nil
	}

	_, conn, err := dial(ctx, opts, t)
	if err != nil {
		return asked{}, err
	}

	caps := conn.Capabilities()

	opts.log.Info("read the server's capabilities", "database", t.name, "driver", t.db.Driver,
		"server", caps.Server())

	live, err := conn.Introspect(ctx)
	if err != nil {
		return asked{}, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}

	logSchema(opts, t, live)

	invalid, err := invalidIndexesOf(ctx, conn)
	if err != nil {
		return asked{}, errors.Join(err, conn.Close(context.WithoutCancel(ctx)))
	}

	if err := conn.Close(context.WithoutCancel(ctx)); err != nil {
		return asked{}, fmt.Errorf("closing the session the database was read from: %w", err)
	}

	return asked{caps: &caps, live: live, rewriter: rewriterOf(conn), shadows: shadowsOf(conn),
		invalid: invalid}, nil
}

// offlineSource returns what reads this project's migrations over sessions that open no database.
func (t *target) offlineSource(ctx context.Context, opts *options,
	found []migration.Migration) (reading, error) {
	conn, err := driver.Offline(t.db.Driver)
	if err != nil {
		return reading{}, err
	}

	read, err := t.sources(ctx, opts, conn, found)
	if err != nil {
		return reading{}, err
	}

	read.source = checking{source: read.source, conn: conn}

	return read, nil
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

// start spawns the runtime that reads one of this project's languages and shakes hands with it.
func (t *target) start(ctx context.Context, opts *options, conn driver.Conn, lang config.Language,
	adapter runtime.Runtime) (*runtime.Session, closer, error) {
	project, err := t.project(lang)
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

// project describes this run to a runtime, which is told the language it is being started for.
func (t *target) project(lang config.Language) (runtime.Project, error) {
	dir, err := filepath.Abs(t.dir)
	if err != nil {
		return runtime.Project{}, fmt.Errorf("resolving %s: %w", t.dir, err)
	}

	return runtime.Project{
		Root:     t.root,
		Dir:      dir,
		Language: string(lang),
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

	caps := conn.Capabilities()

	options := t.paced(engine.Options{
		Planner: plan.Planner{Compiler: d, Expander: expanderOf(conn), Rewriter: rewriterOf(conn),
			Rewrites: t.cfg.Safety.Enabled(), Capabilities: &caps},
	}, conn, opts.log)
	options = t.shadowing(options, d, conn, opts)
	options.Repairer = repairerOf(conn)

	return &session{conn: conn, database: t.name, options: t.retrying(options, opts.log),
		ceiling: t.cfg.Safety.MaxLockImpact, logger: opts.log}, nil
}

// open resolves the target, discovers the migrations, and opens a session ready to run a direction.
func open(ctx context.Context, opts *options, scope engine.Scope) (*session, error) {
	t, err := resolve(opts)
	if err != nil {
		return nil, err
	}

	found, err := t.discovered(opts)
	if err != nil {
		return nil, err
	}

	s, err := t.connect(ctx, opts)
	if err != nil {
		return nil, err
	}

	if err := s.attach(ctx, t, opts, found, scope); err != nil {
		return nil, errors.Join(err, s.close(context.WithoutCancel(ctx)))
	}

	return s, nil
}

// attach gives this session what reads the migrations, which needs the connection it reaches.
func (s *session) attach(ctx context.Context, t *target, opts *options,
	found []migration.Migration, scope engine.Scope) error {
	read, err := t.sources(ctx, opts, s.conn, found)
	if err != nil {
		return err
	}

	s.stop = read.stop

	ordered, err := overlaid(opts, found, read.declared)
	if err != nil {
		return err
	}

	gates, err := gatesOf(ordered, read.phases)
	if err != nil {
		return err
	}

	files, err := hashed(t.fsys, ordered)
	if err != nil {
		return err
	}

	s.options.Phases, s.options.Gates, s.options.Release = read.phases, gates, releaseOf(opts)
	s.options.Online = read.online

	s.options.Source = read.source
	s.options.Migrations = files
	s.options.Clock = opts.clock
	s.options.Runtime = runtimeOf
	s.options.Scope = scope
	s.options.Allow = allowanceFor(t, opts)

	return nil
}

// allowanceFor reads what this project permits and what this run permits, which are two decisions.
func allowanceFor(t *target, opts *options) engine.Allowance {
	return engine.Allowance{
		Kinds: t.cfg.Validation.AllowDestructive,
		All:   opts.allowDestructive,
	}
}

// rulesFor resolves every lint rule against the severity this project gave it, refusing one it did not.
func rulesFor(t *target) (*rule.Set, error) {
	rules, err := rule.NewSet(rule.All(), t.cfg.Validation.Rules)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	return &rules, nil
}

// gatesOf resolves every step that waits on another, refusing declarations no deploy could carry out.
func gatesOf(ordered []migration.Migration, phases migration.Phases) (map[string]migration.Gate, error) {
	gates, err := migration.Gates(ordered, phases)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	return gates, nil
}

// overlaid puts the migrations in the order their declarations and their timestamps together name.
func overlaid(opts *options, found []migration.Migration,
	declared migration.Declared) ([]migration.Migration, error) {
	ordered, err := migration.Order(found, declared)
	if err != nil {
		return nil, NewExitError(ExitValidation, err)
	}

	opts.log.Debug("resolved the run order", "migrations", len(ordered), "declared", len(declared))

	return ordered, nil
}

// runtimeOf names the language one migration is written in, by the same map the mux routes on.
func runtimeOf(m migration.Migration) string {
	return string(languageOf(m.Ext))
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
	s.warnInvalid(ctx)

	return result, selected(err)
}

// warnInvalid warns of every INVALID index the database holds after a run, on a context Ctrl-C has not ended.
func (s *session) warnInvalid(ctx context.Context) {
	if s.logger != nil {
		warnInvalidIndexes(context.WithoutCancel(ctx), s.logger, s.conn)
	}
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

// orphans reads the replication slots no live shadow-table migration of this project owns, taking no lock.
func (s *session) orphans(ctx context.Context) ([]orphanedSlot, error) {
	report, err := s.reconcile(ctx, s.options.Migrations)
	if err != nil {
		return nil, err
	}

	states, err := history.ReadShadows(ctx, s.conn.History())
	if err != nil {
		return nil, err
	}

	return orphansOf(ctx, s.conn, engine.Carried(report), states)
}

// reader narrows a session's store to the half a report may use, so no report can write or Ensure.
func reader(conn driver.Conn) history.Reader {
	return conn.History()
}

// discover reads the migrations directory and puts it in version order, which no declaration has moved yet.
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

// rewriterOf returns the session as the rewriter a planner makes operations safer through, where it is one.
func rewriterOf(conn driver.Conn) driver.Rewriter {
	rewriter, rewrites := conn.(driver.Rewriter)
	if !rewrites {
		return nil
	}

	return rewriter
}

// expanderOf returns the session as the expander a planner compiles an emulation against, where it is one.
func expanderOf(conn driver.Conn) driver.Expander {
	expander, emulates := conn.(driver.Expander)
	if !emulates {
		return nil
	}

	return expander
}
