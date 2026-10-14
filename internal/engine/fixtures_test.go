package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/schema"
)

// errRefused is what the recording session reports for the statement a test told it to fail.
var errRefused = errors.New("the server refused this statement")

// start is the instant every staged run begins at, since this package may not read a clock.
var start = time.Date(2026, 7, 27, 15, 30, 0, 0, time.UTC)

// tick is how far a staged clock advances per reading, so a run's duration is never zero.
const tick = 250 * time.Millisecond

// runtimeName is what staged migrations are written in, and what every staged row records.
const runtimeName = "sql"

// clock returns a clock starting at start and advancing one tick per reading.
func clock() Clock {
	at := start

	return func() time.Time {
		now := at
		at = at.Add(tick)

		return now
	}
}

// backwards returns a clock whose second reading precedes its first, which a real one may do.
func backwards() Clock {
	at := start

	return func() time.Time {
		now := at
		at = at.Add(-tick)

		return now
	}
}

// recorder is a session with no database that records what the executor asked it to do.
type recorder struct {
	caps          capability.Set
	calls         []string
	fail          string
	inTx          bool
	store         *journal
	lockErr       error
	live          *schema.Snapshot
	introspectErr error
}

// newRecorder builds a session whose declaration says whether its DDL is transactional.
func newRecorder(t *testing.T, transactional bool) *recorder {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockFree))
	}

	facts := capability.Facts{Driver: "recorder", Server: "1.0", TransactionalDDL: transactional}

	set, err := capability.NewSet(facts, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	conn := &recorder{caps: set}
	conn.store = &journal{session: conn}

	return conn
}

// note records one thing the executor asked of this session, in the order it asked.
func (r *recorder) note(call string) {
	r.calls = append(r.calls, call)
}

// Exec records the statement and where it ran, failing the one a test asked it to fail.
func (r *recorder) Exec(_ context.Context, stmt driver.Statement) error {
	r.note(r.describeExec(stmt))

	if stmt.SQL == r.fail {
		return errRefused
	}

	return nil
}

// Nothing by default, which Unrestorable judges nothing against. These fixtures stage one index
// name per migration against one table that is never created, so no single snapshot could satisfy
// both halves of them -- an apply needs the index absent and the rollback of that same apply needs
// it present. A test about the gate stages a schema; every other test here is about something else
// and must not be refused by a fake's fiction.

// Introspect records the read and answers with the snapshot this recorder was staged with.
func (r *recorder) Introspect(context.Context) (*schema.Snapshot, error) {
	r.note("introspect")

	if r.introspectErr != nil {
		return nil, r.introspectErr
	}

	return r.live, nil
}

// Query records a migration's own read, which nothing in this package drives yet.
func (r *recorder) Query(_ context.Context, q driver.Query) (driver.Rows, error) {
	r.note("query " + q.SQL)

	return driver.Rows{}, nil
}

// Perform records a migration's own statement, which nothing in this package drives yet.
func (r *recorder) Perform(_ context.Context, q driver.Query) (int64, error) {
	r.note("perform " + q.SQL)

	return 0, nil
}

// describeExec records whether the statement ran inside the transaction or outside it.
func (r *recorder) describeExec(stmt driver.Statement) string {
	if r.inTx {
		return "exec " + stmt.SQL
	}

	return "exec-outside-any-transaction " + stmt.SQL
}

// Transact records the boundary and commits only when the work returns nil.
func (r *recorder) Transact(ctx context.Context, work driver.Work) error {
	r.note("begin")
	r.inTx = true

	err := work(ctx)

	r.inTx = false

	if err != nil {
		r.note("rollback")

		return err
	}

	r.note("commit")

	return nil
}

// Lock records the acquire and hands back a release that records itself and its context.
func (r *recorder) Lock(context.Context) (driver.Unlock, error) {
	if r.lockErr != nil {
		return nil, r.lockErr
	}

	r.note("lock")

	return func(ctx context.Context) error {
		r.note(live(ctx, "unlock"))

		return nil
	}, nil
}

// live marks a call whose context was already canceled, which some call paths must never inherit.
func live(ctx context.Context, call string) string {
	if ctx.Err() != nil {
		return call + "-on-a-canceled-context"
	}

	return call
}

// Capabilities reports the declaration this session was built with.
func (r *recorder) Capabilities() capability.Set { return r.caps }

// History reports this session's store, or none at all when a test staged none.
func (r *recorder) History() history.Store {
	if r.store == nil {
		return nil
	}

	return r.store
}

// Seeds returns the seed history, which no engine test reaches.
func (r *recorder) Seeds() history.Store {
	return nil
}

// ServerVersion reports the version the declaration was resolved against.
func (r *recorder) ServerVersion() string { return r.caps.Server() }

// Close releases nothing, since this session holds nothing.
func (r *recorder) Close(context.Context) error { return nil }

// compile-time proof that the recorder satisfies the contract a real session does.
var _ driver.Conn = (*recorder)(nil)

// journal is a history store with no database, recording what the executor asked of it.
type journal struct {
	session  *recorder
	runs     []history.Record
	table    bool
	ensure   error
	list     error
	latest   error
	record   error
	failed   error
	dirty    error
	resolved error
	cps      []history.Checkpoint
}

// Ensure records the call and creates the table, unless a test staged a failure.
func (j *journal) Ensure(context.Context) error {
	j.session.note("ensure")

	if j.ensure != nil {
		return j.ensure
	}

	j.table = true

	return nil
}

// List returns every recorded run, reporting a table nothing created as the contract requires.
func (j *journal) List(context.Context) ([]history.Record, error) {
	j.session.note("list")

	if j.list != nil {
		return nil, j.list
	}

	if !j.table {
		return nil, history.ErrNoTable
	}

	return j.runs, nil
}

// Latest returns the most recent run, and whether there has been one at all.
func (j *journal) Latest(context.Context) (history.Record, bool, error) {
	j.session.note("latest")

	if j.latest != nil {
		return history.Record{}, false, j.latest
	}

	if !j.table {
		return history.Record{}, false, history.ErrNoTable
	}

	if len(j.runs) == 0 {
		return history.Record{}, false, nil
	}

	return j.runs[len(j.runs)-1], true, nil
}

// Record appends a run that completed, holding the caller to what a row must carry.
func (j *journal) Record(_ context.Context, rec history.Record) error {
	j.session.note(j.describeWrite("record", rec))

	if j.record != nil {
		return j.record
	}

	return j.write(rec, history.Applied, "", 0)
}

// MarkFailed appends a run that failed without changing the schema.
func (j *journal) MarkFailed(ctx context.Context, rec history.Record, cause error) error {
	j.session.note(live(ctx, j.describeWrite("mark-failed", rec)))

	if j.failed != nil {
		return j.failed
	}

	return j.write(rec, history.Failed, cause.Error(), 0)
}

// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
func (j *journal) MarkDirty(ctx context.Context, rec history.Record, last int32, cause error) error {
	j.session.note(live(ctx, j.describeWrite("mark-dirty", rec)))

	if j.dirty != nil {
		return j.dirty
	}

	return j.write(rec, history.Dirty, cause.Error(), last)
}

// MarkResolved appends an operator's assertion that a migration left between states is settled.
func (j *journal) MarkResolved(ctx context.Context, rec history.Record, reason string) error {
	j.session.note(live(ctx, j.describeWrite("mark-resolved", rec)))

	if j.resolved != nil {
		return j.resolved
	}

	return j.write(rec, history.Resolved, reason, 0)
}

// describeWrite records which row was written and whether a transaction was open at the time.
func (j *journal) describeWrite(call string, rec history.Record) string {
	if j.session.inTx {
		return call + " " + rec.Version
	}

	return call + "-outside-any-transaction " + rec.Version
}

// write stamps the build and holds the record to what a run must carry, as a real store does.
func (j *journal) write(rec history.Record, outcome history.Outcome, failure string, last int32) error {
	rec.Outcome = outcome
	rec.Error = failure
	rec.LastStatement = last
	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		return err
	}

	j.runs = append(j.runs, rec)

	return nil
}

// compile-time proof that the journal satisfies the store contract a real one does.
var _ history.Store = (*journal)(nil)

// source hands back the operations a test staged per migration, and remembers what it was asked.
type source struct {
	session    *recorder
	ops        map[string]ir.Operations
	down       map[string]ir.Operations
	direct     map[string][]driver.Query
	fail       map[string]error
	failUp     map[string]error
	asked      []string
	directions []ir.Direction
	modes      []Mode
}

// Emit returns what a test staged for this migration, or the failure it staged instead.
func (s *source) Emit(_ context.Context, m migration.Migration, dir ir.Direction, mode Mode,
) (Emitted, error) {
	s.asked = append(s.asked, m.Version)
	s.directions = append(s.directions, dir)
	s.modes = append(s.modes, mode)

	if s.session != nil {
		s.session.note("emit " + m.Version + " " + string(dir))
	}

	if err := s.fail[m.Version]; err != nil {
		return Emitted{}, err
	}

	if dir == ir.Down {
		return s.emitDown(m)
	}

	if err := s.failUp[m.Version]; err != nil {
		return Emitted{}, err
	}

	return Emitted{Operations: s.ops[m.Version], Direct: s.heldFor(m, mode)}, nil
}

// heldFor returns the staged escape-hatch statements, which only a question ever holds (ADR 0036).
func (s *source) heldFor(m migration.Migration, mode Mode) []driver.Query {
	if mode != ModePlan {
		return nil
	}

	return s.direct[m.Version]
}

// emitDown returns the down a test authored, or the sentinel that asks the engine to derive one.
func (s *source) emitDown(m migration.Migration) (Emitted, error) {
	authored, ok := s.down[m.Version]
	if !ok {
		return Emitted{}, ErrNoDownAuthored
	}

	return Emitted{Operations: authored}, nil
}

// stagedTable is the table every staged operation names, since only the index names carry meaning.
const stagedTable = "staged"

// compiler turns a staged operation into the one statement the object it names spells.
type compiler struct{}

// Compile renders the statement a staged operation stands for, refusing anything else.
func (compiler) Compile(op ir.Operation) ([]driver.Statement, error) {
	switch typed := op.(type) {
	case ir.AddIndex:
		return statement(typed.Name), nil
	case ir.DropIndex:
		return statement(typed.Name), nil
	case ir.CreateTable:
		return statement(typed.Table), nil
	case ir.DropTable:
		return statement(typed.Table), nil
	case ir.DropView:
		return statement(typed.View), nil
	case ir.DataBackfill:
		return statement("fill " + typed.Table), nil
	case ir.DataTransform:
		return statement("rewrite " + typed.Table), nil
	case ir.DataCopy:
		return statement("copy " + typed.From), nil
	case ir.RenameTable:
		return nil, nil
	}

	return nil, fmt.Errorf("the staged compiler was given %s", op.Kind())
}

// statement renders one staged operation, uppercase meaning it cannot run inside a transaction.
func statement(name string) []driver.Statement {
	return []driver.Statement{{SQL: name, NoTransaction: standalone(name)}}
}

// standalone reports whether a staged statement declared it cannot run inside a transaction.
func standalone(sql string) bool {
	return sql != "" && sql[0] >= 'A' && sql[0] <= 'Z'
}

// operations turns a spec into one operation per statement, mirroring statements at the IR level.
func operations(spec string) ir.Operations {
	ops := make(ir.Operations, 0, len(spec))

	for i, letter := range spec {
		ops = append(ops, addIndex(fmt.Sprintf("%c%d", letter, i)))
	}

	return ops
}

// addIndex is a staged operation that reverses cleanly, which is what a rollback fixture needs.
func addIndex(name string) ir.AddIndex {
	return ir.AddIndex{Table: stagedTable, Name: name, Columns: []string{"id"}}
}

// unrecorded is a staged operation with no inverse, because it records nothing to restore.
func unrecorded(name string) ir.DropIndex {
	return ir.DropIndex{Table: stagedTable, Name: name}
}

// versionAt renders the nth staged migration's version, ordered as the sequence it was given in.
func versionAt(n int) string {
	return fmt.Sprintf("20260727T1530%02d", n)
}

// nameAt renders the nth staged migration's name.
func nameAt(n int) string {
	return fmt.Sprintf("migration_%d", n)
}

// bodyAt renders what the nth staged migration's file holds, which its source digest covers.
func bodyAt(n int) string {
	return "-- migration " + versionAt(n) + "\n"
}

// migrationAt returns the nth staged migration, parsed through the grammar a real one comes from.
func migrationAt(t *testing.T, n int) migration.Migration {
	t.Helper()

	m, err := migration.Parse(versionAt(n) + "_" + nameAt(n) + ".sql")
	if err != nil {
		t.Fatalf("staging migration %d: %v", n, err)
	}

	return m
}

// staged builds the options an apply run is given, one spec of statement letters per migration.
func staged(t *testing.T, specs ...string) (Options, *source) {
	t.Helper()

	files := make([]history.File, 0, len(specs))
	src := &source{
		ops:    map[string]ir.Operations{},
		down:   map[string]ir.Operations{},
		direct: map[string][]driver.Query{},
		fail:   map[string]error{},
		failUp: map[string]error{},
	}

	for i, spec := range specs {
		m := migrationAt(t, i)
		files = append(files, history.NewFile(m, []byte(bodyAt(i))))
		src.ops[m.Version] = operations(spec)
	}

	return Options{
		Planner:    plan.Planner{Compiler: compiler{}},
		Source:     src,
		Migrations: files,
		Clock:      clock(),
		Runtime:    written,
	}, src
}

// written names the language every staged migration is written in, which is one for these fixtures.
func written(migration.Migration) string {
	return runtimeName
}

// ran returns the row a previous invocation left for the nth staged migration, as it applied it.
func ran(t *testing.T, n int, batch int64, ordinal int32) history.Record {
	t.Helper()

	m := migrationAt(t, n)

	return history.Record{
		Batch:         batch,
		Ordinal:       ordinal,
		Version:       m.Version,
		Name:          m.Name,
		Direction:     ir.Up,
		Outcome:       history.Applied,
		Checksum:      sumOf(n),
		AppliedAt:     start.Add(-time.Hour),
		Duration:      time.Second,
		Driver:        "recorder",
		Runtime:       runtimeName,
		EngineVersion: "test",
		IRVersion:     ir.Version,
	}
}

// undone returns the row a previous rollback left for the nth staged migration, as it undid it.
func undone(t *testing.T, n int, batch int64, ordinal int32) history.Record {
	t.Helper()

	rec := ran(t, n, batch, ordinal)
	rec.Direction = ir.Down

	return rec
}

// sumOf is the checksum a previous run of the nth staged migration recorded, source half first.
func sumOf(n int) checksum.Checksum {
	return checksum.Checksum{
		Source: checksum.OfSource([]byte(bodyAt(n))),
		IR:     checksum.OfSource([]byte("whatever that run emitted")),
	}
}
