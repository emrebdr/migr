package cli

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// lackedCapture is what the fake server lacks for log-based capture, where a test says it lacks it.
const lackedCapture = "wal_level = logical (it is replica, and changing it restarts the server)"

// shadowing is the part of the fake a shadow-table migration reaches.
type shadowing struct {
	// captures reports a server that permits log-based change capture.
	captures bool
	// shadows reports a session that performs shadow-table migration.
	shadows bool
	// states is the shadow-table state beside the migration history.
	states map[string]history.ShadowState
	// slots names every capture resource the server holds.
	slots map[string]bool
	// confirmAfter is how many reads of the held log pass before the fake confirms, zero for never.
	confirmAfter int
	// polls counts the reads of the log while a copy is held in sync.
	polls int
	// killAt is the read of the held log at which the fake stops the run, zero for never.
	killAt int
}

// errStopped is the fake stopping a held run the way Ctrl-C does.
var errStopped = context.Canceled

// usersSnapshot is a database holding users, keyed by id, which a shadow-table migration can copy.
func usersSnapshot() *schema.Snapshot {
	typ, _ := schema.Parse("int64")
	id, _ := schema.ExactType(typ, "bigint")
	text, _ := schema.Parse("string(16)")
	plan, _ := schema.ExactType(text, "varchar(16)")

	return &schema.Snapshot{Tables: []schema.Table{{Name: "users",
		Columns:    []schema.Column{{Name: "id", Type: id}, {Name: "plan", Type: plan, Nullable: true}},
		PrimaryKey: &schema.PrimaryKey{Columns: []string{"id"}}}}}
}

// shadowConn is a session that also performs shadow-table migration.
type shadowConn struct {
	*fakeConn
}

// Shadows refuses nothing.
func (c *shadowConn) Shadows(ir.Operation) error { return nil }

// Admits refuses no table.
func (c *shadowConn) Admits(context.Context, driver.Shadow) error { return nil }

// CreateShadow compiles the empty copy.
func (c *shadowConn) CreateShadow(_ context.Context, s driver.Shadow) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "-- create " + s.Shadow}}, nil
}

// CopyChunk copies the whole table in one chunk.
func (c *shadowConn) CopyChunk(context.Context, driver.Shadow, []string, int) (driver.Sliced, error) {
	c.fake.note(describeWrite("copy-chunk", c.fake.inTx))

	return driver.Sliced{Written: 3, Last: []string{"3"}, Done: true}, nil
}

// ApplyChanges writes nothing, the fake app writing nothing.
func (c *shadowConn) ApplyChanges(context.Context, driver.Shadow, []capture.Change) error {
	return nil
}

// Cutover catches up and notes the swap.
func (c *shadowConn) Cutover(ctx context.Context, _ driver.Shadow, catchUp driver.CatchUp) error {
	if err := catchUp(ctx); err != nil {
		return err
	}

	c.fake.note(describeWrite("cutover", c.fake.inTx))

	return nil
}

// DropShadow compiles dropping the copy.
func (c *shadowConn) DropShadow(s driver.Shadow) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "-- drop " + s.Shadow}}, nil
}

// DropRetired compiles dropping the retired original.
func (c *shadowConn) DropRetired(s driver.Shadow) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "-- drop " + s.Retired}}, nil
}

// Capture opens a stream on a log nothing writes to, or refuses where the server permits no capture.
func (d *fakeDriver) Capture(_ context.Context, _ string, req capture.Request) (capture.Stream, error) {
	d.fake.note("capture " + req.Name)

	if !d.fake.captures {
		return nil, capture.ErrUnsupported
	}

	if d.fake.slots == nil {
		d.fake.slots = map[string]bool{}
	}

	d.fake.slots[req.Name] = true

	return &fakeStream{fake: d.fake, name: req.Name, at: req.From}, nil
}

// fakeStream reads a log nothing writes to, confirming or stopping the hold where a test said.
type fakeStream struct {
	// fake is the database.
	fake *fake
	// name is the resource it reads through.
	name string
	// at is where it began.
	at capture.Position
}

// Start returns where the stream began, the log's end for a fresh one.
func (s *fakeStream) Start() capture.Position {
	if s.at == "" {
		return "0"
	}

	return s.at
}

// Next returns nothing new, counting the reads made while a copy is held.
func (s *fakeStream) Next(context.Context) (capture.Batch, error) {
	if !s.held() {
		return capture.Batch{Through: s.Start()}, nil
	}

	s.fake.polls++

	if s.fake.killAt > 0 && s.fake.polls == s.fake.killAt {
		return capture.Batch{}, errStopped
	}

	if s.fake.confirmAfter > 0 && s.fake.polls >= s.fake.confirmAfter {
		for version, state := range s.fake.states {
			state.Confirmed = true
			s.fake.states[version] = state
		}
	}

	return capture.Batch{Through: capture.Position(strconv.Itoa(s.fake.polls))}, nil
}

// held reports a copy being held in sync, which is when a read counts.
func (s *fakeStream) held() bool {
	for _, state := range s.fake.states {
		if state.Stage == history.StageSyncing {
			return true
		}
	}

	return false
}

// Close keeps the resource.
func (s *fakeStream) Close(context.Context) error {
	s.fake.note("detach " + s.name)

	return nil
}

// Drop releases the resource.
func (s *fakeStream) Drop(context.Context) error {
	s.fake.note("release " + s.name)
	delete(s.fake.slots, s.name)

	return nil
}

// BeginShadow records a new state, unconfirmed.
func (s *fakeStore) BeginShadow(_ context.Context, state history.ShadowState) error {
	s.fake.note(describeWrite(s.prefix+"begin-shadow", s.fake.inTx))

	if s.fake.states == nil {
		s.fake.states = map[string]history.ShadowState{}
	}

	state.Confirmed = false
	s.fake.states[state.Version] = state

	return state.Validate()
}

// SaveShadow records how far one got, leaving its confirmation as it is.
func (s *fakeStore) SaveShadow(_ context.Context, state history.ShadowState) error {
	state.Confirmed = s.fake.states[state.Version].Confirmed
	s.fake.states[state.Version] = state

	return state.Validate()
}

// ConfirmShadow records a confirmation.
func (s *fakeStore) ConfirmShadow(_ context.Context, version string) error {
	if state, found := s.fake.states[version]; found {
		state.Confirmed = true
		s.fake.states[version] = state
	}

	return nil
}

// ShadowStates returns every state, sorted by version.
func (s *fakeStore) ShadowStates(context.Context) ([]history.ShadowState, error) {
	versions := slices.Sorted(maps.Keys(s.fake.states))

	states := make([]history.ShadowState, 0, len(versions))
	for _, version := range versions {
		states = append(states, s.fake.states[version])
	}

	return states, nil
}

// ClearShadow removes one state.
func (s *fakeStore) ClearShadow(_ context.Context, version string) error {
	delete(s.fake.states, version)

	return nil
}

// compile-time proof that the fake can carry a shadow-table migration.
var (
	_ driver.Shadower = (*shadowConn)(nil)
	_ history.Shadows = (*fakeStore)(nil)
)

// fakeRetention is how long the fake server says it keeps its log.
const fakeRetention = "the fake keeps its log for a week"

// captureFact is whether the fake server permits log-based capture, as its declaration states it.
func (f *fake) captureFact() capability.Capture {
	if f.captures {
		return capability.Permits().Retains(fakeRetention)
	}

	return capability.Lacks(lackedCapture)
}
