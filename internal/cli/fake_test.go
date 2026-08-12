package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// fakeName is the driver name a staged project's migr.yaml points at.
const fakeName = "fake"

// errRefused is what a fake database reports for the statement a test told it to refuse.
var errRefused = errors.New("the server refused this statement")

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
	// lockErr is what acquiring the migration lock reports, nil when it succeeds.
	lockErr error
	// inTx reports whether a transaction is open, which is what a history write must be inside.
	inTx bool
}

// registry returns a registry holding this fake under the name a staged migr.yaml names.
func (f *fake) registry(t *testing.T) *driver.Registry {
	t.Helper()

	registry := driver.NewRegistry()

	err := registry.Register(fakeName, func(driver.Options) (driver.Driver, error) {
		return &fakeDriver{fake: f, caps: fakeCapabilities(t)}, nil
	})
	if err != nil {
		t.Fatalf("registering the fake driver: %v", err)
	}

	return registry
}

// note records one thing a run asked of this database, in the order it asked.
func (f *fake) note(call string) {
	f.calls = append(f.calls, call)
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

// fakeCapabilities declares every operation native and lock-free, which no real driver may do.
func fakeCapabilities(t *testing.T) capability.Set {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockFree))
	}

	set, err := capability.NewSet(capability.Facts{
		Driver:           fakeName,
		Server:           "0.0-fake",
		TransactionalDDL: true,
	}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return set
}

// fakeDriver compiles raw SQL to itself and opens the one session a test reads afterwards.
type fakeDriver struct {
	fake *fake
	caps capability.Set
}

// Name reports the name this driver is registered under.
func (d *fakeDriver) Name() string { return fakeName }

// Compile passes raw SQL through untouched, and refuses everything a .sql migration cannot emit.
func (d *fakeDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	raw, ok := op.(ir.Raw)
	if !ok {
		return nil, fmt.Errorf("the fake driver was given %s", op.Kind())
	}

	if raw.Driver != fakeName {
		return nil, fmt.Errorf("raw SQL addressed to %q reached %q", raw.Driver, fakeName)
	}

	return []driver.Statement{{SQL: raw.SQL}}, nil
}

// Open hands back the session, which holds no connection to give away.
func (d *fakeDriver) Open(context.Context, string) (driver.Conn, error) {
	d.fake.note("open")

	return &fakeConn{fake: d.fake, caps: d.caps}, nil
}

// fakeConn is one pinned session over a fake database.
type fakeConn struct {
	fake *fake
	caps capability.Set
}

// Exec records the statement and refuses the one a test asked it to refuse.
func (c *fakeConn) Exec(_ context.Context, stmt driver.Statement) error {
	c.fake.statements = append(c.fake.statements, stmt.SQL)

	if stmt.SQL == c.fake.refuse {
		return errRefused
	}

	return nil
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
func (c *fakeConn) Capabilities() capability.Set { return c.caps }

// History reports this session's store.
func (c *fakeConn) History() history.Store { return &fakeStore{fake: c.fake} }

// ServerVersion reports the version the declaration was resolved against.
func (c *fakeConn) ServerVersion() string { return c.caps.Server() }

// Close records the release, since a command that leaks a session is a bug a test should see.
func (c *fakeConn) Close(context.Context) error {
	c.fake.note("close")

	return nil
}

// compile-time proof that the fake satisfies the contract a real session does.
var _ driver.Conn = (*fakeConn)(nil)

// fakeStore is a migration history with no database, reporting an absent table as a real one does.
type fakeStore struct {
	fake *fake
}

// Ensure creates the history table, which is the write a dry run must never make.
func (s *fakeStore) Ensure(context.Context) error {
	s.fake.note("ensure")
	s.fake.table = true

	return nil
}

// List returns every recorded run, or the sentinel a database with no history table owes.
func (s *fakeStore) List(context.Context) ([]history.Record, error) {
	s.fake.note("list")

	if !s.fake.table {
		return nil, history.ErrNoTable
	}

	return s.fake.runs, nil
}

// Latest returns the most recent run, or the sentinel a database with no history table owes.
func (s *fakeStore) Latest(context.Context) (history.Record, bool, error) {
	s.fake.note("latest")

	if !s.fake.table {
		return history.Record{}, false, history.ErrNoTable
	}

	if len(s.fake.runs) == 0 {
		return history.Record{}, false, nil
	}

	return s.fake.runs[len(s.fake.runs)-1], true, nil
}

// Record appends a run that completed, inside the transaction that ran it.
func (s *fakeStore) Record(_ context.Context, rec history.Record) error {
	s.fake.note(describeWrite("record", s.fake.inTx))

	return s.write(rec, history.Applied, "", 0)
}

// MarkFailed appends a run that failed without changing the schema.
func (s *fakeStore) MarkFailed(_ context.Context, rec history.Record, cause error) error {
	s.fake.note(describeWrite("mark-failed", s.fake.inTx))

	return s.write(rec, history.Failed, cause.Error(), 0)
}

// MarkDirty appends a run that failed partway, naming the last statement that succeeded.
func (s *fakeStore) MarkDirty(_ context.Context, rec history.Record, last int32, cause error) error {
	s.fake.note(describeWrite("mark-dirty", s.fake.inTx))

	return s.write(rec, history.Dirty, cause.Error(), last)
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

	s.fake.runs = append(s.fake.runs, rec)

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
var _ history.Store = (*fakeStore)(nil)
