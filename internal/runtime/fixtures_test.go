package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// errNoServer marks a fake asked for something only a real database could answer.
var errNoServer = errors.New("this fake has no database")

// conn is a driver.Conn that answers a migration's callbacks out of what a test staged.
type conn struct {
	caps capability.Set
	// reads are the statements host.db.query sent, in order.
	reads []driver.Query
	// performed are the statements host.db.exec sent and this session ran, which plan mode holds.
	performed []driver.Query
}

// newConn builds a fake session that resolves every operation natively and reads one row.
func newConn(t *testing.T) *conn {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockFree))
	}

	caps, err := capability.NewSet(
		capability.Facts{Driver: "postgres", Server: "17.2", TransactionalDDL: true}, rules)
	if err != nil {
		t.Fatalf("building the capability set: %v", err)
	}

	return &conn{caps: caps}
}

// Exec is never reached: the host serves a migration's own SQL, and compiled statements are the engine's.
func (c *conn) Exec(context.Context, driver.Statement) error { return errNoServer }

// Query records a migration's read and answers with one row, which is what the fake asks for.
func (c *conn) Query(_ context.Context, q driver.Query) (driver.Rows, error) {
	c.reads = append(c.reads, q)
	count := "0"

	return driver.Rows{Columns: []string{"n"}, Values: [][]*string{{&count}}}, nil
}

// Perform records the escape hatch's statement, which apply mode runs and plan mode holds.
func (c *conn) Perform(_ context.Context, q driver.Query) (int64, error) {
	c.performed = append(c.performed, q)

	return 1, nil
}

// Lock is never reached: the migration lock is taken before any runtime starts.
func (c *conn) Lock(context.Context) (driver.Unlock, error) { return nil, errNoServer }

// Transact is never reached: the transaction opens after the migration has finished emitting.
func (c *conn) Transact(context.Context, driver.Work) error { return errNoServer }

// Capabilities reports the declaration host.capability.get and host.info read.
func (c *conn) Capabilities() capability.Set { return c.caps }

// History is never reached from a callback.
func (c *conn) History() history.Store { return nil }

// ServerVersion reports what host.info renders.
func (c *conn) ServerVersion() string { return "17.2" }

// Close is never reached: the session outlives every runtime.
func (c *conn) Close(context.Context) error { return nil }

// compile-time proof that the fake is held to the same contract a driver is.
var _ driver.Conn = (*conn)(nil)

// kept is a slog handler that keeps every record, so a test can read what was filed.
type kept struct {
	mu      sync.Mutex
	records []slog.Record
}

// Enabled keeps every level, since the engine's own threshold is not what is under test.
func (h *kept) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the record.
func (h *kept) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, r)

	return nil
}

// WithAttrs returns the same handler, since nothing under test adds attributes.
func (h *kept) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup returns the same handler, since nothing under test opens a group.
func (h *kept) WithGroup(string) slog.Handler { return h }

// messages returns every line filed so far, as a copy.
func (h *kept) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	lines := make([]string, 0, len(h.records))
	for _, r := range h.records {
		lines = append(lines, r.Message)
	}

	return lines
}

// fixture is the migration every test here asks a runtime about.
var fixture = migration.Migration{Version: "20260727T153000", Name: "add_users", Ext: ".go"}

// dependent is the version of the second migration a fake reports, which declares the first.
const dependent = "20260727T153100"

// project is what a fake runtime is told about the run, which is a real path and nothing secret.
func project(t *testing.T) Project {
	t.Helper()

	root := t.TempDir()

	return Project{Root: root, Dir: root + "/migrations", Language: "go",
		Database: "default", Driver: "postgres"}
}

// start spawns a fake runtime playing one script, and shuts it down when the test ends.
func start(t *testing.T, act string) (*Session, *conn) {
	t.Helper()

	s, c, err := open2(t, act, &kept{})
	if err != nil {
		t.Fatalf("Start(%s): %v", act, err)
	}

	t.Cleanup(func() { _ = s.Close(context.Background()) })

	return s, c
}

// open2 spawns a fake runtime and hands back whatever Start reported, for a test that expects a refusal.
func open2(t *testing.T, act string, log *kept) (*Session, *conn, error) {
	t.Helper()

	c := newConn(t)

	s, err := Start(t.Context(), Options{
		Runtime:  &fakeRuntime{act: act},
		Project:  project(t),
		Conn:     c,
		Log:      slog.New(log),
		Timeouts: Timeouts{ShutdownGrace: brief},
	})

	return s, c, err
}

// brief is the grace these tests give a runtime, since none of them is testing patience.
const brief = 2 * time.Second

// quiet is a logger that keeps nothing, for the tests that do not read what was filed.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
