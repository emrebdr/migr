package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/schema"
)

// errKilled is what every call answers once a world's process has been killed.
var errKilled = errors.New("the process was killed")

// peopleTable is the table every shadow-table fixture changes.
const peopleTable = "people"

// world is the server a shadow-table fixture runs against, all of which a failed transaction puts back.
type world struct {
	// tables maps each table's name to its rows, each keyed by its id.
	tables map[string]map[string]string
	// log is every write to people the server committed, in order.
	log []capture.Change
	// slots names every capture resource the server holds.
	slots map[string]bool
	// runs is the history.
	runs []history.Record
	// states is the shadow-table state, by version.
	states map[string]history.ShadowState
	// truth is what people should hold, maintained beside the server as the app writes.
	truth map[string]string
	// permits is whether the server permits log-based capture.
	permits bool
	// shadower reports whether the session performs shadow-table migration.
	shadower bool
	// at is called at each named point the fixture reaches, which is where a test writes or kills.
	at func(point string)
	// killed reports a process that has stopped, which no call reaches any more.
	killed bool
	// inTx reports an open transaction.
	inTx bool
	// batch is how many changes one read of the stream returns at most.
	batch int
	// calls records what was asked of the server, for a test about order.
	calls []string
	// read is the log position the last read of a stream reached.
	read int
	// refuses is what the session's Admits answers, nil admitting the table.
	refuses error
	// cause is what a killed process's calls fail with, errKilled where nothing else is named.
	cause error
}

// newWorld returns a server holding people with the given number of rows, permitting capture.
func newWorld(rows int) *world {
	w := &world{tables: map[string]map[string]string{peopleTable: {}}, slots: map[string]bool{},
		states: map[string]history.ShadowState{}, truth: map[string]string{}, permits: true,
		shadower: true, batch: 2, at: func(string) {}}

	for i := 1; i <= rows; i++ {
		w.tables[peopleTable][strconv.Itoa(i)] = "v" + strconv.Itoa(i)
		w.truth[strconv.Itoa(i)] = "v" + strconv.Itoa(i)
	}

	return w
}

// write is the application writing to people while the migration runs, which the log records.
func (w *world) write(kind capture.Kind, id int, value string) {
	key := strconv.Itoa(id)

	switch kind {
	case capture.Delete:
		delete(w.tables[peopleTable], key)
		delete(w.truth, key)
		w.log = append(w.log, capture.Change{Kind: kind, Table: peopleTable, Columns: []string{"id"},
			Row: []*string{&key}})
	default:
		w.tables[peopleTable][key] = value
		w.truth[key] = value
		v := value
		w.log = append(w.log, capture.Change{Kind: kind, Table: peopleTable,
			Columns: []string{"id", "label"}, Row: []*string{&key, &v}})
	}
}

// reach calls the test at a named point, then refuses everything if that killed the process.
func (w *world) reach(point string) error {
	w.calls = append(w.calls, point)

	if w.killed {
		return w.killedBy()
	}

	w.at(point)

	if w.killed {
		return w.killedBy()
	}

	return nil
}

// killedBy is what a call into a stopped process fails with.
func (w *world) killedBy() error {
	if w.cause != nil {
		return w.cause
	}

	return errKilled
}

// restart is the next process: the server keeps everything it committed, and answers again.
func (w *world) restart() {
	w.killed, w.inTx, w.at, w.cause = false, false, func(string) {}, nil
}

// saved is everything a transaction that fails puts back.
type saved struct {
	// tables are the rows.
	tables map[string]map[string]string
	// runs is the history.
	runs []history.Record
	// states is the shadow-table state.
	states map[string]history.ShadowState
}

// save copies what a transaction may change.
func (w *world) save() saved {
	tables := make(map[string]map[string]string, len(w.tables))
	for name, rows := range w.tables {
		tables[name] = maps.Clone(rows)
	}

	return saved{tables: tables, runs: slices.Clone(w.runs), states: maps.Clone(w.states)}
}

// restore puts back what a failed transaction changed.
func (w *world) restore(s saved) {
	w.tables, w.runs, w.states = s.tables, s.runs, s.states
}

// worldConn is the session a shadow-table fixture runs through, its store, and its strategy at once.
type worldConn struct {
	// w is the server.
	w *world
	// caps is what the session declares.
	caps capability.Set
}

// connect opens a session on the world, declaring capture as the world permits it.
func connect(t *testing.T, w *world) *worldConn {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.TableRewrite))
	}

	caught := capability.Lacks("binlog_format = ROW (it is STATEMENT)")
	if w.permits {
		caught = capability.Permits()
	}

	set, err := capability.NewSet(capability.Facts{Driver: "world", Server: "1.0", TransactionalDDL: true,
		Capture: caught}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return &worldConn{w: w, caps: set}
}

// Exec runs one of the three statements this fixture's strategy compiles to.
func (c *worldConn) Exec(_ context.Context, stmt driver.Statement) error {
	if err := c.w.reach("exec " + stmt.SQL); err != nil {
		return err
	}

	verb, name, _ := strings.Cut(stmt.SQL, " ")

	switch verb {
	case "--":
	case "create":
		if _, exists := c.w.tables[name]; exists {
			return fmt.Errorf("the table %s already exists", name)
		}

		c.w.tables[name] = map[string]string{}
	case "drop":
		delete(c.w.tables, name)
	default:
		return fmt.Errorf("the world runs no %q", stmt.SQL)
	}

	return nil
}

// Query answers nothing, since no fixture reads.
func (c *worldConn) Query(context.Context, driver.Query) (driver.Rows, error) {
	return driver.Rows{}, nil
}

// Perform changes nothing, since no fixture writes through it.
func (c *worldConn) Perform(context.Context, driver.Query) (int64, error) { return 0, nil }

// Introspect reports every table the world holds, each keyed by id with a label column.
func (c *worldConn) Introspect(context.Context) (*schema.Snapshot, error) {
	if err := c.w.reach("introspect"); err != nil {
		return nil, err
	}

	label := schema.Column{Name: "label", Type: exactly("string(16)"), Nullable: true}
	id := schema.Column{Name: "id", Type: exactly("int64")}

	var tables []schema.Table
	for _, name := range slices.Sorted(maps.Keys(c.w.tables)) {
		tables = append(tables, schema.Table{Name: name, Columns: []schema.Column{id, label},
			PrimaryKey: &schema.PrimaryKey{Columns: []string{"id"}}})
	}

	return &schema.Snapshot{Tables: tables}, nil
}

// exactly is a column type a fixture names, which is always one the grammar accepts.
func exactly(spelling string) schema.ColumnType {
	typ, _ := schema.Parse(spelling)
	column, _ := schema.ExactType(typ, spelling)

	return column
}

// Lock takes nothing, since one fixture runs alone.
func (c *worldConn) Lock(context.Context) (driver.Unlock, error) {
	if c.w.killed {
		return nil, errKilled
	}

	return func(context.Context) error { return nil }, nil
}

// Transact runs work and puts everything back where it fails, as a server does with a transaction.
func (c *worldConn) Transact(ctx context.Context, work driver.Work) error {
	if c.w.killed {
		return errKilled
	}

	before := c.w.save()
	c.w.inTx = true

	err := work(ctx)

	c.w.inTx = false

	if err == nil && c.w.killed {
		err = errKilled
	}

	if err != nil {
		c.w.restore(before)
	}

	return err
}

// Capabilities reports what the session declares.
func (c *worldConn) Capabilities() capability.Set { return c.caps }

// History returns the world's history, which also remembers shadow-table state.
func (c *worldConn) History() history.Store { return worldStore{c.w} }

// Seeds returns no seed history.
func (c *worldConn) Seeds() history.Store { return nil }

// ServerVersion reports the declared version.
func (c *worldConn) ServerVersion() string { return "1.0" }

// Close releases nothing.
func (c *worldConn) Close(context.Context) error { return nil }

// Shadows refuses nothing, since every fixture's operation is an index the copy can carry.
func (c *worldConn) Shadows(ir.Operation) error { return nil }

// Admits refuses the table where the world was told to, and records that it was asked.
func (c *worldConn) Admits(_ context.Context, s driver.Shadow) error {
	c.w.calls = append(c.w.calls, "admits "+s.Table)

	return c.w.refuses
}

// CreateShadow compiles the empty copy.
func (c *worldConn) CreateShadow(_ context.Context, s driver.Shadow) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "create " + s.Shadow}}, nil
}

// CopyChunk copies the rows after a key into the copy in key order, keeping any already there.
func (c *worldConn) CopyChunk(_ context.Context, s driver.Shadow, after []string, rows int) (driver.Sliced, error) {
	if err := c.w.reach("copy"); err != nil {
		return driver.Sliced{}, err
	}

	from := 0
	if len(after) == 1 {
		from, _ = strconv.Atoi(after[0])
	}

	keys := keysAfter(c.w.tables[s.Table], from)
	taken := keys[:min(rows, len(keys))]

	var sliced driver.Sliced

	for _, k := range taken {
		key := strconv.Itoa(k)
		if _, there := c.w.tables[s.Shadow][key]; !there {
			c.w.tables[s.Shadow][key] = c.w.tables[s.Table][key]
			sliced.Written++
		}

		sliced.Last = []string{key}
	}

	if sliced.Last == nil {
		sliced.Last = after
	}

	sliced.Done = len(taken) == len(keys)

	return sliced, nil
}

// keysAfter lists a table's integer keys above a bound, in order.
func keysAfter(rows map[string]string, from int) []int {
	var keys []int

	for key := range rows {
		if k, _ := strconv.Atoi(key); k > from {
			keys = append(keys, k)
		}
	}

	slices.Sort(keys)

	return keys
}

// ApplyChanges writes captured changes to the copy as upserts and deletes by key.
func (c *worldConn) ApplyChanges(_ context.Context, s driver.Shadow, changes []capture.Change) error {
	if err := c.w.reach("apply"); err != nil {
		return err
	}

	for _, change := range changes {
		key := *change.Row[0]

		if change.Kind == capture.Delete {
			delete(c.w.tables[s.Shadow], key)
			continue
		}

		c.w.tables[s.Shadow][key] = *change.Row[1]
	}

	return nil
}

// Cutover catches up under its lock and then swaps the copy in, keeping the original under its retired name.
func (c *worldConn) Cutover(ctx context.Context, s driver.Shadow, catchUp driver.CatchUp) error {
	if err := c.w.reach("cutover"); err != nil {
		return err
	}

	if err := catchUp(ctx); err != nil {
		return err
	}

	if err := c.w.reach("swap"); err != nil {
		return err
	}

	c.w.tables[s.Retired] = c.w.tables[s.Table]
	c.w.tables[s.Table] = c.w.tables[s.Shadow]
	delete(c.w.tables, s.Shadow)

	return nil
}

// DropShadow compiles dropping the copy.
func (c *worldConn) DropShadow(s driver.Shadow) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "drop " + s.Shadow}}, nil
}

// DropRetired compiles dropping the retired original.
func (c *worldConn) DropRetired(s driver.Shadow) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "drop " + s.Retired}}, nil
}

// capturer opens a stream on the world's log: from its end, recreating the resource, or from a position.
func (c *worldConn) capturer(ctx context.Context, req capture.Request) (capture.Stream, error) {
	if err := c.w.reach("capture"); err != nil {
		return nil, err
	}

	if req.From == "" {
		c.w.slots[req.Name] = true

		return &worldStream{w: c.w, name: req.Name, at: len(c.w.log), start: len(c.w.log)}, nil
	}

	if !c.w.slots[req.Name] {
		return nil, fmt.Errorf("the capture resource %s is gone", req.Name)
	}

	at, err := strconv.Atoi(string(req.From))
	if err != nil {
		return nil, err
	}

	_ = ctx

	return &worldStream{w: c.w, name: req.Name, at: at, start: at}, nil
}

// worldStream reads the world's log from a position.
type worldStream struct {
	// w is the server.
	w *world
	// name is the resource it reads through.
	name string
	// at is the next entry it returns.
	at int
	// start is where it began.
	start int
}

// Start returns where the stream began.
func (s *worldStream) Start() capture.Position { return capture.Position(strconv.Itoa(s.start)) }

// Next returns up to a batch of the entries after the position, and the position after them.
func (s *worldStream) Next(context.Context) (capture.Batch, error) {
	if err := s.w.reach("next"); err != nil {
		return capture.Batch{}, err
	}

	end := min(s.at+s.w.batch, len(s.w.log))
	batch := capture.Batch{Changes: slices.Clone(s.w.log[s.at:end]), Through: capture.Position(strconv.Itoa(end)),
		Behind: time.Duration(len(s.w.log)-end) * time.Millisecond}
	s.at = end
	s.w.read = end

	return batch, nil
}

// Close keeps the resource.
func (s *worldStream) Close(context.Context) error {
	s.w.calls = append(s.w.calls, "close "+s.name)

	return nil
}

// Drop releases the resource.
func (s *worldStream) Drop(context.Context) error {
	s.w.calls = append(s.w.calls, "drop-stream "+s.name)
	delete(s.w.slots, s.name)

	return nil
}

// worldStore is the world's history and its shadow-table state.
type worldStore struct {
	// w is the server.
	w *world
}

// Ensure creates nothing, the history always being there.
func (s worldStore) Ensure(context.Context) error { return s.w.alive() }

// alive refuses every call once the process is killed.
func (w *world) alive() error {
	if w.killed {
		return errKilled
	}

	return nil
}

// List returns every run.
func (s worldStore) List(context.Context) ([]history.Record, error) {
	return slices.Clone(s.w.runs), s.w.alive()
}

// Latest returns the last run.
func (s worldStore) Latest(context.Context) (history.Record, bool, error) {
	if len(s.w.runs) == 0 {
		return history.Record{}, false, s.w.alive()
	}

	return s.w.runs[len(s.w.runs)-1], true, s.w.alive()
}

// Record appends an applied run.
func (s worldStore) Record(_ context.Context, rec history.Record) error {
	return s.append(rec, history.Applied, "", 0)
}

// MarkFailed appends a failed run.
func (s worldStore) MarkFailed(_ context.Context, rec history.Record, cause error) error {
	return s.append(rec, history.Failed, cause.Error(), 0)
}

// MarkDirty appends a dirty run.
func (s worldStore) MarkDirty(_ context.Context, rec history.Record, last int32, cause error) error {
	return s.append(rec, history.Dirty, cause.Error(), last)
}

// MarkResolved appends a resolution.
func (s worldStore) MarkResolved(_ context.Context, rec history.Record, reason string) error {
	return s.append(rec, history.Resolved, reason, 0)
}

// append holds a row to what a real store does, and refuses it once the process is killed.
func (s worldStore) append(rec history.Record, outcome history.Outcome, failure string, last int32) error {
	if err := s.w.alive(); err != nil {
		return err
	}

	rec.Outcome, rec.Error, rec.LastStatement = outcome, failure, last
	rec = rec.Stamp()

	if err := rec.Validate(); err != nil {
		return err
	}

	s.w.runs = append(s.w.runs, rec)

	return nil
}

// BeginShadow records a new state, unconfirmed.
func (s worldStore) BeginShadow(_ context.Context, state history.ShadowState) error {
	if err := s.w.alive(); err != nil {
		return err
	}

	if err := state.Validate(); err != nil {
		return err
	}

	state.Confirmed = false
	s.w.states[state.Version] = state

	return nil
}

// SaveShadow records how far one got, leaving its confirmation as someone else wrote it.
func (s worldStore) SaveShadow(_ context.Context, state history.ShadowState) error {
	if err := s.w.reach("save"); err != nil {
		return err
	}

	if err := state.Validate(); err != nil {
		return err
	}

	state.Confirmed = s.w.states[state.Version].Confirmed
	s.w.states[state.Version] = state

	return nil
}

// ConfirmShadow records a confirmation.
func (s worldStore) ConfirmShadow(_ context.Context, version string) error {
	state, found := s.w.states[version]
	if found {
		state.Confirmed = true
		s.w.states[version] = state
	}

	return s.w.alive()
}

// ShadowStates returns every state.
func (s worldStore) ShadowStates(context.Context) ([]history.ShadowState, error) {
	return slices.Collect(maps.Values(s.w.states)), s.w.alive()
}

// ClearShadow removes one state.
func (s worldStore) ClearShadow(_ context.Context, version string) error {
	delete(s.w.states, version)

	return s.w.alive()
}

// Checkpoints returns none, since no fixture here runs in chunks.
func (s worldStore) Checkpoints(context.Context) ([]history.Checkpoint, error) {
	return nil, s.w.alive()
}

// SaveCheckpoint refuses, since no fixture here runs in chunks.
func (s worldStore) SaveCheckpoint(context.Context, history.Checkpoint) error {
	return errors.New("no fixture here runs in chunks")
}

// ClearCheckpoint clears nothing.
func (s worldStore) ClearCheckpoint(context.Context, string, ir.Direction) error { return s.w.alive() }

// confirm is someone confirming the cutover of the one staged shadow-table migration.
func (w *world) confirm() {
	for version, state := range w.states {
		state.Confirmed = true
		w.states[version] = state
	}
}

// indexPeople is the one operation every shadow-table fixture's migration emits.
func indexPeople() ir.AddIndex {
	return ir.AddIndex{Table: peopleTable, Name: "people_label", Columns: []string{"label"}}
}

// shadowRun builds the options that run one migration through a shadow table against the world.
func shadowRun(t *testing.T, c *worldConn, ops ...ir.Operation) Options {
	t.Helper()

	opts, src := staged(t, "")

	if len(ops) == 0 {
		ops = ir.Operations{indexPeople()}
	}

	src.ops[versionAt(0)] = ops
	opts.Planner = plan.Planner{Compiler: anything{}}
	opts.Online = migration.Online{versionAt(0): migration.ShadowTable}
	opts.Capture = c.capturer
	opts.Pacer = Steady{Rows: 2}
	opts.Sleep = func(context.Context, time.Duration) error { return nil }

	if c.w.shadower {
		opts.Shadower = c
	}

	return opts
}

// anything compiles every operation to one statement naming its kind, which a shadow never runs.
type anything struct{}

// Compile renders one statement naming the operation's kind.
func (anything) Compile(op ir.Operation) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "-- " + op.Kind().String()}}, nil
}

// compile-time proof that the world's session is a session, a strategy, and a shadow-state store.
var (
	_ driver.Conn     = (*worldConn)(nil)
	_ driver.Shadower = (*worldConn)(nil)
	_ history.Shadows = worldStore{}
)
