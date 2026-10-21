package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/schema"
)

// fakeName is the driver name a staged project's migr.yaml points at.
const fakeName = "fake"

// errRefused is what a fake database reports for the statement a test told it to refuse.
var errRefused = errors.New("the server refused this statement")

// errNoMigrationSQL marks a callback no command reaches: a .sql migration is ir.Raw, and compiled.
var errNoMigrationSQL = errors.New("this fake serves no migration's own SQL")

// fake is a database with no server, keeping what a run asked of it for a test to read afterwards.
type fake struct {
	// statements is every statement executed, in the order they were executed.
	statements []string
	// calls is every session-level call, so a test can assert what was never asked for.
	calls []string
	// runs is the history rows written, which a real store would have persisted.
	runs []history.Record
	// table reports whether the history table exists, as ErrNoTable does on a real database.
	table bool
	// refuse is the statement this database rejects, empty when it rejects none.
	refuse string
	// contended is the statement that gives up waiting for a lock, empty when none does.
	contended string
	// contentions is how many more times that statement gives up before it succeeds.
	contentions int
	// slow is the statement that runs until whatever it runs under is stopped.
	slow string
	// autoCommits makes this database declare no transactional DDL, as MySQL's driver does.
	autoCommits bool
	// unsupported is the SQL whose operation this driver will not compile at all.
	unsupported string
	// serverRefuses is the SQL this server declares it will not perform, having compiled it.
	serverRefuses string
	// openErr is what connecting reports, nil for a database that answers.
	openErr error
	// emulates names the strategy this driver achieves every operation by, empty when it is native.
	emulates string
	// expands makes the compiler ask for a session, the way a driver that reads a schema does.
	expands bool
	// outsideTx is the SQL that compiles to a statement which cannot share a transaction.
	outsideTx string
	// invalid are the INVALID indexes this database reports.
	invalid []driver.InvalidIndex
	// repaired is what every repair answers, and nil makes this session repair nothing.
	repaired *driver.Repaired
	// rewrites is the SQL this session performs as a safer two-statement sequence, empty for none.
	rewrites string
	// costly is the SQL this server declares rewrites its table unless the table is small, empty for none.
	costly string
	// assesses reports a session that reads the table and finds it small, lowering what costly costs.
	assesses bool
	// assessErr is what reading the table reports, nil where the read succeeds.
	assessErr error
	// assessed counts the operations this session was asked to read a table for.
	assessed int
	// releases is the release table beside the migration history.
	releases []history.Release
	// lags is each replica-lag read's answer in turn, the last repeated, and none where there are no replicas.
	lags [][]driver.Lag
	// lagReads counts how often the replica lag was read.
	lagReads int
	// seedReleases is the release table beside the seed history.
	seedReleases []history.Release
	// lockErr is what acquiring the migration lock reports, nil when it succeeds.
	lockErr error
	// inTx reports whether a transaction is open, which is what a history write must be inside.
	inTx bool
	// live is the snapshot this database answers an introspection with, empty for a fresh database.
	live *schema.Snapshot
	// seedRuns is the seed history rows written, kept apart from runs as a second table is.
	seedRuns []history.Record
	// seedTable reports whether the seed history table exists.
	seedTable bool
	// checkpoints is how far a chunked migration got, as a migration store's checkpoint table holds it.
	checkpoints []history.Checkpoint
	// seedCheckpoints is the same for seeds, which keep a checkpoint table of their own.
	seedCheckpoints []history.Checkpoint
	// chunks is every slice a chunked run asked this session for, in order.
	chunks []driver.Slice
	// sliced is what each chunk reports in turn, a done chunk of one row once it runs out.
	sliced []driver.Sliced
	// chunkFails is the chunk, counting from one, this session refuses; zero refuses none.
	chunkFails int
	// shadowing is what a shadow-table migration reaches.
	shadowing

	// mu guards the two fields below, which are the only ones a second goroutine could reach.
	mu sync.Mutex
	// serving counts the calls in flight on this one pinned session, which must never exceed one.
	serving int
	// overlapped reports two calls in flight at once, which several live runtimes would produce.
	overlapped bool
}

// enter marks one call in flight on this session, recording an overlap rather than serializing it.
func (f *fake) enter() func() {
	f.mu.Lock()
	f.serving++
	f.overlapped = f.overlapped || f.serving > 1
	f.mu.Unlock()

	return func() {
		f.mu.Lock()
		f.serving--
		f.mu.Unlock()
	}
}

// overlapping reports whether two calls were ever in flight on this session at the same time.
func (f *fake) overlapping() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.overlapped
}

// registry returns a registry holding this fake under the name a staged migr.yaml names.
func (f *fake) registry(t *testing.T) *driver.Registry {
	t.Helper()

	registry := driver.NewRegistry()

	err := registry.Register(fakeName, func(driver.Options) (driver.Driver, error) {
		return &fakeDriver{fake: f, caps: f.capabilities(t)}, nil
	})
	if err != nil {
		t.Fatalf("registering the fake driver: %v", err)
	}

	return registry
}

// fakeAdapter stands in for a language adapter, so cli tests need not name a concrete runtime.
type fakeAdapter struct {
	// name is the language migr.yaml would spell, and the key the registry holds it under.
	name string
}

// Name reports the language this adapter would run.
func (a *fakeAdapter) Name() string {
	return a.name
}

// Discover reports that this machine could run the language, since nothing here starts one.
func (a *fakeAdapter) Discover(context.Context) error {
	return nil
}

// Command refuses, which is the adapter for the tests about a runtime that will not start.
func (a *fakeAdapter) Command(context.Context, runtime.Project) (runtime.Command, error) {
	return runtime.Command{}, errors.New("this fake adapter starts nothing")
}

// runtimeRegistry returns a registry carrying one adapter per language a real build registers.
func runtimeRegistry(t *testing.T, languages ...config.Language) *runtime.Registry {
	t.Helper()

	registry := runtime.NewRegistry()

	for _, lang := range languages {
		name := string(lang)

		err := registry.Register(name, func() (runtime.Runtime, error) {
			return &fakeAdapter{name: name}, nil
		})
		if err != nil {
			t.Fatalf("registering the %s adapter: %v", name, err)
		}
	}

	return registry
}

// note records one thing a run asked of this database, in the order it asked.
func (f *fake) note(call string) {
	f.calls = append(f.calls, call)
}

// record stages the row one run would have written, holding it to what a real store would have.
func (f *fake) record(t *testing.T, rec history.Record) {
	t.Helper()

	rec.Driver = fakeName
	rec.Runtime = string(config.LanguageSQL)

	if rec.AppliedAt.IsZero() {
		rec.AppliedAt = noon.UTC()
	}

	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		t.Fatalf("staging a history row: %v", err)
	}

	f.table = true
	f.runs = append(f.runs, rec)
}

// recordSeed stages one seed history row the way record stages a migration one.
func (f *fake) recordSeed(t *testing.T, rec history.Record) {
	t.Helper()

	rec.Driver = fakeName
	rec.Runtime = string(config.LanguageSQL)

	if rec.AppliedAt.IsZero() {
		rec.AppliedAt = noon.UTC()
	}

	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		t.Fatalf("staging a seed history row: %v", err)
	}

	f.seedTable = true
	f.seedRuns = append(f.seedRuns, rec)
}

// asked reports whether a run made the named call.
func (f *fake) asked(call string) bool {
	for _, made := range f.calls {
		if made == call {
			return true
		}
	}

	return false
}

// count reports how many times one thing was asked of this fake, where once is not the question.
func (f *fake) count(call string) int {
	total := 0

	for _, made := range f.calls {
		if made == call {
			total++
		}
	}

	return total
}

// capabilities declares every operation lock-free, and the support this test asked the driver for.
func (f *fake) capabilities(t *testing.T) capability.Set {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = f.verdict
	}

	set, err := capability.NewSet(capability.Facts{
		Driver:           fakeName,
		Server:           "0.0-fake",
		TransactionalDDL: !f.autoCommits,
		Capture:          f.captureFact(),
	}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return set
}

// verdict answers both axes, refusing what the compiler refuses and what only this server would.
func (f *fake) verdict(op ir.Operation) capability.Verdict {
	if f.refuses(op) {
		return capability.Unsupported(refusalReason)
	}

	if f.declines(op) {
		return capability.Unsupported(serverRefusalReason)
	}

	if costly, ok := f.costlyVerdict(op); ok {
		return costly
	}

	if f.emulates != "" {
		return capability.Emulated(f.emulates, capability.LockFree)
	}

	return capability.Native(f.lockOf(op))
}

// smallTable is the condition under which the costly statement costs this fake server only a moment.
const smallTable = "the table is small enough to change in place"

// lockOf is what an operation costs this fake server: costly rewrites its table, a rewritable one scans under a lock.
func (f *fake) lockOf(op ir.Operation) capability.LockImpact {
	raw, ok := op.(ir.Raw)

	switch {
	case !ok:
		return capability.LockFree
	case f.rewrites != "" && raw.SQL == f.rewrites:
		return capability.LongExclusive
	default:
		return capability.LockFree
	}
}

// costlyVerdict declares the costly statement a rewrite unless a session finds the table small.
func (f *fake) costlyVerdict(op ir.Operation) (capability.Verdict, bool) {
	raw, ok := op.(ir.Raw)
	if !ok || f.costly == "" || raw.SQL != f.costly {
		return capability.Verdict{}, false
	}

	return capability.Native(capability.TableRewrite).Unless(capability.BriefExclusive, smallTable), true
}

// refuses reports whether this driver will not compile an operation at all.
func (f *fake) refuses(op ir.Operation) bool {
	raw, ok := op.(ir.Raw)

	return ok && f.unsupported != "" && raw.SQL == f.unsupported
}

// declines reports whether this server refuses an operation its compiler produced statements for.
func (f *fake) declines(op ir.Operation) bool {
	raw, ok := op.(ir.Raw)

	return ok && f.serverRefuses != "" && raw.SQL == f.serverRefuses
}

// refusalReason is why the fake driver refuses the operation a test told it to refuse.
const refusalReason = "this fake engine has no such thing"

// serverRefusalReason is why the fake server refuses one it compiled, which only a version knows.
const serverRefusalReason = "this release accepts the statement and discards it, so refusing is " +
	"the only answer that is not a lie"

// fakeDriver compiles raw SQL to itself and opens the one session a test reads afterwards.
type fakeDriver struct {
	fake *fake
	caps capability.Set
}

// Name reports the name this driver is registered under.
func (d *fakeDriver) Name() string { return fakeName }

// Compile passes raw SQL through untouched, and renders a typed operation as a comment naming it.
func (d *fakeDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	raw, ok := op.(ir.Raw)
	if !ok {
		return []driver.Statement{{SQL: "-- " + op.Kind().String()}}, nil
	}

	if raw.Driver != fakeName {
		return nil, fmt.Errorf("raw SQL addressed to %q reached %q", raw.Driver, fakeName)
	}

	if d.fake.refuses(op) {
		return nil, driver.Unsupported(fakeName, op.Kind(), refusalReason)
	}

	if d.fake.expands {
		return nil, driver.NeedsSession(fakeName, op.Kind(), "it reads the schema first")
	}

	first := driver.Statement{SQL: raw.SQL, NoTransaction: raw.NoTransaction || raw.SQL == d.fake.outsideTx}
	if d.fake.emulates == "" {
		return []driver.Statement{first}, nil
	}

	return []driver.Statement{first, {SQL: emulationStep(d.fake.emulates)}}, nil
}

// emulationStep is the extra statement an emulated operation costs, so one op becomes several.
func emulationStep(strategy string) string {
	return "-- achieved by " + strategy
}

// Open hands back the session, or whatever a test told this database to refuse connecting with.
func (d *fakeDriver) Open(context.Context, string) (driver.Conn, error) {
	d.fake.note("open")

	if d.fake.openErr != nil {
		return nil, d.fake.openErr
	}

	conn := &fakeConn{fake: d.fake, caps: d.caps}
	if d.fake.shadows {
		return &shadowConn{fakeConn: conn}, nil
	}

	return conn, nil
}

// fakeConn is one pinned session over a fake database.
type fakeConn struct {
	fake *fake
	caps capability.Set
}

// InvalidIndexes reports the INVALID indexes the test staged, asking the session nothing it records.
func (c *fakeConn) InvalidIndexes(context.Context) ([]driver.InvalidIndex, error) {
	return c.fake.invalid, nil
}

// Repair answers as the test staged, recording that it was asked.
func (c *fakeConn) Repair(_ context.Context, _ ir.Operation, stmt driver.Statement, when driver.RepairTime,
	_ error,
) (driver.Repaired, error) {
	if c.fake.repaired == nil {
		return driver.Repaired{}, nil
	}

	c.fake.calls = append(c.fake.calls, "repair "+string(when)+" "+stmt.SQL)

	return *c.fake.repaired, nil
}

// Assess reads the table a costly statement changes, and finds it small where the test said it would.
func (c *fakeConn) Assess(_ context.Context, _ ir.Operation, v capability.Verdict) (capability.Verdict, error) {
	c.fake.assessed++

	if c.fake.assessErr != nil {
		return v, c.fake.assessErr
	}

	if !c.fake.assesses {
		return v, nil
	}

	return v.Confirmed(), nil
}

// Rewrite performs the one raw statement a test named as itself NOT VALID and then a bare check.
func (c *fakeConn) Rewrite(op ir.Operation) (driver.Rewrite, bool) {
	raw, ok := op.(ir.Raw)
	if !ok || c.fake.rewrites == "" || raw.SQL != c.fake.rewrites {
		return driver.Rewrite{}, false
	}

	return driver.Rewrite{
		Name: driver.RewriteNotValid,
		Statements: []driver.Statement{
			{SQL: raw.SQL + " -- not valid"},
			{SQL: "-- validated", NoTransaction: true},
		},
		Reason: "the rows are checked without blocking writes",
		Lock:   capability.BriefExclusive,
	}, true
}

// Exec records the statement and refuses the one a test asked it to refuse.
func (c *fakeConn) Exec(ctx context.Context, stmt driver.Statement) error {
	defer c.fake.enter()()

	c.fake.statements = append(c.fake.statements, stmt.SQL)

	if stmt.SQL == c.fake.slow {
		<-ctx.Done()

		return fmt.Errorf("the fake server stopped the statement: %w", ctx.Err())
	}

	if stmt.SQL == c.fake.refuse {
		return errRefused
	}

	if stmt.SQL == c.fake.contended && c.fake.contentions > 0 {
		c.fake.contentions--

		return fmt.Errorf("%w: another session holds the table", driver.ErrStatementLockTimeout)
	}

	return nil
}

// Expand produces what only a session could, which is what makes this fake a driver.Expander.
func (c *fakeConn) Expand(_ context.Context, op ir.Operation) ([]driver.Statement, error) {
	c.fake.note("expand")

	raw, ok := op.(ir.Raw)
	if !ok {
		return nil, fmt.Errorf("the fake session was given %s", op.Kind())
	}

	return []driver.Statement{
		{SQL: "-- read the schema"},
		{SQL: raw.SQL},
	}, nil
}

// Nothing, rather than an empty database: engine.Unrestorable judges nothing against no snapshot,
// and a scripted migration's operations name tables this fake never creates. A test about that
// gate stages a schema; every other test here is about something else and must not be refused by
// a fake's fiction. A .sql project reaches the gate as ir.Raw either way, which it never judges.

// Introspect answers with the snapshot this fake was staged with, and nothing by default.
func (c *fakeConn) Introspect(context.Context) (*schema.Snapshot, error) {
	c.fake.note("introspect")

	return c.fake.live, nil
}

// Query refuses: no command drives a migration's own SQL, since every .sql migration is ir.Raw.
func (c *fakeConn) Query(context.Context, driver.Query) (driver.Rows, error) {
	defer c.fake.enter()()

	return driver.Rows{}, errNoMigrationSQL
}

// Perform refuses, for the reason Query does.
func (c *fakeConn) Perform(context.Context, driver.Query) (int64, error) {
	defer c.fake.enter()()

	return 0, errNoMigrationSQL
}

// Lock records the acquire and hands back a release that records itself.
func (c *fakeConn) Lock(context.Context) (driver.Unlock, error) {
	if c.fake.lockErr != nil {
		return nil, c.fake.lockErr
	}

	c.fake.note("lock")

	return func(context.Context) error {
		c.fake.note("unlock")

		return nil
	}, nil
}

// Transact brackets the work, committing only when it returns nil.
func (c *fakeConn) Transact(ctx context.Context, work driver.Work) error {
	c.fake.note("begin")
	c.fake.inTx = true

	err := work(ctx)
	c.fake.inTx = false

	if err != nil {
		c.fake.note("rollback")

		return err
	}

	c.fake.note("commit")

	return nil
}

// Capabilities reports the declaration this session was built with.
func (c *fakeConn) Capabilities() capability.Set {
	defer c.fake.enter()()

	return c.caps
}

// History reports this session's store.
func (c *fakeConn) History() history.Store {
	return &fakeStore{fake: c.fake, table: &c.fake.table, runs: &c.fake.runs, cps: &c.fake.checkpoints,
		releases: &c.fake.releases}
}

// Seeds returns the seed history, which notes every call it is asked under a seed- prefix.
func (c *fakeConn) Seeds() history.Store {
	return &fakeStore{fake: c.fake, table: &c.fake.seedTable, runs: &c.fake.seedRuns,
		cps: &c.fake.seedCheckpoints, releases: &c.fake.seedReleases, prefix: "seed-"}
}

// ServerVersion reports the version the declaration was resolved against.
func (c *fakeConn) ServerVersion() string { return c.caps.Server() }

// Close records the release, since a command that leaks a session is a bug a test should see.
func (c *fakeConn) Close(context.Context) error {
	c.fake.note("close")

	return nil
}

// Chunk records the slice and reports what the test scripted, a done chunk of one row by default.
func (c *fakeConn) Chunk(_ context.Context, s driver.Slice) (driver.Sliced, error) {
	c.fake.note(describeWrite("chunk", c.fake.inTx))
	c.fake.chunks = append(c.fake.chunks, s)

	if len(c.fake.chunks) == c.fake.chunkFails {
		return driver.Sliced{}, errRefused
	}

	if at := len(c.fake.chunks) - 1; at < len(c.fake.sliced) {
		return c.fake.sliced[at], nil
	}

	return driver.Sliced{Written: 1, Last: []string{"1"}, Done: true}, nil
}

// ReplicaLag answers with the next scripted reading, which is no replicas where none was scripted.
func (c *fakeConn) ReplicaLag(context.Context) ([]driver.Lag, error) {
	c.fake.lagReads++

	if len(c.fake.lags) == 0 {
		return nil, nil
	}

	return c.fake.lags[min(c.fake.lagReads, len(c.fake.lags))-1], nil
}

// CompileChunk renders a chunk as two statements naming what it reads and writes, for a plan to print.
func (d *fakeDriver) CompileChunk(op ir.Operation, key []string) ([]driver.Statement, error) {
	return []driver.Statement{
		{SQL: "-- the bound of a chunk keyed by (" + strings.Join(key, ", ") + ")"},
		{SQL: "-- one chunk of " + string(op.Kind())},
	}, nil
}

// compile-time proof that the fake satisfies the contract a real session does.
var (
	_ driver.Conn    = (*fakeConn)(nil)
	_ driver.Chunker = (*fakeConn)(nil)
)

// fakeStore is a migration history with no database, reporting an absent table as a real one does.
type fakeStore struct {
	// fake is the database this store is a table of, which every call is noted against.
	fake *fake
	// table reports whether this store's table exists.
	table *bool
	// runs is the rows this store's table holds.
	runs *[]history.Record
	// prefix tells a seed store's calls from a migration store's in what the fake noted.
	prefix string
	// cps is the checkpoints this store's checkpoint table holds.
	cps *[]history.Checkpoint
	// releases is the releases this store's release table holds.
	releases *[]history.Release
}

// RecordRelease records one batch's release, noting whether a transaction was open.
func (s *fakeStore) RecordRelease(_ context.Context, r history.Release) error {
	s.fake.note(s.prefix + describeWrite("record-release", s.fake.inTx))

	if err := r.Validate(); err != nil {
		return err
	}

	*s.releases = append(*s.releases, r)

	return nil
}

// Releases returns every release this store holds.
func (s *fakeStore) Releases(context.Context) ([]history.Release, error) {
	return slices.Clone(*s.releases), nil
}

// SaveCheckpoint replaces one migration's checkpoint, noting whether a transaction was open.
func (s *fakeStore) SaveCheckpoint(ctx context.Context, cp history.Checkpoint) error {
	s.fake.note(s.prefix + describeWrite("save-checkpoint", s.fake.inTx))

	if err := cp.Validate(); err != nil {
		return err
	}

	*s.cps = append(withoutCheckpoint(*s.cps, cp.Version, cp.Direction), cp)

	return nil
}

// Checkpoints returns every checkpoint this store holds.
func (s *fakeStore) Checkpoints(context.Context) ([]history.Checkpoint, error) {
	return slices.Clone(*s.cps), nil
}

// ClearCheckpoint removes one migration's checkpoint, noting whether a transaction was open.
func (s *fakeStore) ClearCheckpoint(_ context.Context, version string, dir ir.Direction) error {
	s.fake.note(s.prefix + describeWrite("clear-checkpoint", s.fake.inTx))
	*s.cps = withoutCheckpoint(*s.cps, version, dir)

	return nil
}

// withoutCheckpoint returns the checkpoints less the one of a migration and direction.
func withoutCheckpoint(cps []history.Checkpoint, version string, dir ir.Direction) []history.Checkpoint {
	return slices.DeleteFunc(slices.Clone(cps), func(cp history.Checkpoint) bool {
		return cp.Version == version && cp.Direction == dir
	})
}

// Ensure creates the history table, which is the write a dry run must never make.
func (s *fakeStore) Ensure(context.Context) error {
	s.fake.note(s.prefix + "ensure")
	*s.table = true

	return nil
}

// List returns every recorded run, or the sentinel a database with no history table owes.
func (s *fakeStore) List(context.Context) ([]history.Record, error) {
	s.fake.note(s.prefix + "list")

	if !*s.table {
		return nil, history.ErrNoTable
	}

	return *s.runs, nil
}

// Latest returns the most recent run, or the sentinel a database with no history table owes.
func (s *fakeStore) Latest(context.Context) (history.Record, bool, error) {
	s.fake.note(s.prefix + "latest")

	if !*s.table {
		return history.Record{}, false, history.ErrNoTable
	}

	if len(*s.runs) == 0 {
		return history.Record{}, false, nil
	}

	return (*s.runs)[len(*s.runs)-1], true, nil
}

// Record appends a run that completed, inside the transaction that ran it.
func (s *fakeStore) Record(_ context.Context, rec history.Record) error {
	s.fake.note(s.prefix + describeWrite("record", s.fake.inTx))

	return s.write(rec, history.Applied, "", 0)
}

// MarkFailed appends a run that failed without changing the schema.
func (s *fakeStore) MarkFailed(_ context.Context, rec history.Record, cause error) error {
	s.fake.note(s.prefix + describeWrite("mark-failed", s.fake.inTx))

	return s.write(rec, history.Failed, cause.Error(), 0)
}

// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
func (s *fakeStore) MarkDirty(_ context.Context, rec history.Record, last int32, cause error) error {
	s.fake.note(s.prefix + describeWrite("mark-dirty", s.fake.inTx))

	return s.write(rec, history.Dirty, cause.Error(), last)
}

// MarkResolved appends an operator's assertion that a migration left between states is settled.
func (s *fakeStore) MarkResolved(_ context.Context, rec history.Record, reason string) error {
	s.fake.note(s.prefix + describeWrite("mark-resolved", s.fake.inTx))

	return s.write(rec, history.Resolved, reason, 0)
}

// write holds a row to what a run must carry, as a real store does, and keeps it.
func (s *fakeStore) write(rec history.Record, outcome history.Outcome, failure string, last int32) error {
	rec.Outcome = outcome
	rec.Error = failure
	rec.LastStatement = last
	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		return err
	}

	*s.runs = append(*s.runs, rec)

	return nil
}

// describeWrite records whether a transaction was open when the row was written.
func describeWrite(call string, inTx bool) string {
	if inTx {
		return call
	}

	return call + "-outside-any-transaction"
}

// compile-time proof that the fake store satisfies the contract a real one does.
var (
	_ history.Store        = (*fakeStore)(nil)
	_ history.Checkpointer = (*fakeStore)(nil)
)
