package host

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/runtime/protocol"
	"github.com/emrebdr/migr/internal/schema"
)

// errNoServer marks a fake asked for something only a real database could answer.
var errNoServer = errors.New("this fake has no database")

// session is a driver.Conn that records what a callback asked it and answers with what a test set.
type session struct {
	caps capability.Set
	// reads are the statements host.db.query sent, in order.
	reads []driver.Query
	// writes are the statements host.db.exec sent, in order.
	writes []driver.Query
	// rows is what a read returns.
	rows driver.Rows
	// affected is what a write reports it changed.
	affected int64
	// readErr is what a read fails with, and nothing means it succeeds.
	readErr error
	// writeErr is what a write fails with, and nothing means it succeeds.
	writeErr error
	// version is what ServerVersion reports.
	version string
}

// newSession builds a fake session that resolves every operation the way a test asked.
func newSession(t *testing.T, verdict capability.Verdict) *session {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(verdict)
	}

	caps, err := capability.NewSet(
		capability.Facts{Driver: "postgres", Server: "17.2", TransactionalDDL: true}, rules)
	if err != nil {
		t.Fatalf("building the capability set: %v", err)
	}

	return &session{caps: caps, version: "17.2", rows: driver.Rows{}}
}

// Exec is never reached: the host serves a migration's own SQL, and compiled statements are the engine's.
func (s *session) Exec(context.Context, driver.Statement) error { return errNoServer }

// Introspect is never reached: no host callback reads a snapshot.
func (s *session) Introspect(context.Context) (*schema.Snapshot, error) { return nil, errNoServer }

// Query records a migration's read and answers with what the test set.
func (s *session) Query(_ context.Context, q driver.Query) (driver.Rows, error) {
	s.reads = append(s.reads, q)

	if s.readErr != nil {
		return driver.Rows{}, s.readErr
	}

	return s.rows, nil
}

// Perform records a migration's own statement and reports what the test set.
func (s *session) Perform(_ context.Context, q driver.Query) (int64, error) {
	s.writes = append(s.writes, q)

	if s.writeErr != nil {
		return 0, s.writeErr
	}

	return s.affected, nil
}

// Lock is never reached: the migration lock is taken before any callback fires.
func (s *session) Lock(context.Context) (driver.Unlock, error) { return nil, errNoServer }

// Transact is never reached: the transaction opens after the migration has finished emitting.
func (s *session) Transact(context.Context, driver.Work) error { return errNoServer }

// Capabilities reports the declaration host.capability.get and host.info read.
func (s *session) Capabilities() capability.Set { return s.caps }

// History is never reached from a callback.
func (s *session) History() history.Store { return nil }

// ServerVersion reports what host.info renders.
func (s *session) ServerVersion() string { return s.version }

// Close is never reached: the session outlives every call.
func (s *session) Close(context.Context) error { return nil }

// compile-time proof that the fake is held to the same contract a driver is.
var _ driver.Conn = (*session)(nil)

// newServer builds a host server over a fake session, in whatever mode each call states.
func newServer(t *testing.T, conn driver.Conn) *Server {
	t.Helper()

	server, err := New(Options{
		Conn:     conn,
		Database: "default",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	return server
}

// execute opens an up call for the fixture migration, in the mode a test asked for.
func execute(t *testing.T, server *Server, mode protocol.Mode) *Call {
	t.Helper()

	call, err := server.Execute("20260727T153000", ir.Up, mode)
	if err != nil {
		t.Fatalf("opening a %s call: %v", mode, err)
	}

	return call
}

// logged is a handler that keeps every record, so a test can read what host.log filed.
type logged struct {
	records []slog.Record
}

// Enabled keeps every level, since the engine's own threshold is not what is under test.
func (h *logged) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the record.
func (h *logged) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)

	return nil
}

// WithAttrs returns the same handler, since nothing under test adds attributes.
func (h *logged) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup returns the same handler, since nothing under test opens a group.
func (h *logged) WithGroup(string) slog.Handler { return h }

// attrs collects one record's attributes in the order they were logged.
func attrs(r slog.Record) map[string]string {
	found := make(map[string]string, r.NumAttrs())

	r.Attrs(func(a slog.Attr) bool {
		found[a.Key] = a.Value.String()

		return true
	})

	return found
}
