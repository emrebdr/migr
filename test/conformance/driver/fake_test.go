package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// native is the ordinary declaration: the engine performs the operation directly.
var native = capability.Native(capability.LockUnknown)

// fakeDriver compiles every operation to a marker comment, so no test here authors engine SQL.
type fakeDriver struct {
	name              string
	refusals          map[ir.Kind]error
	silent            map[ir.Kind][]driver.Statement
	acceptsForeignRaw bool
	asked             []ir.Kind
}

// newFake returns a driver that compiles everything and refuses nothing.
func newFake(name string) *fakeDriver {
	return &fakeDriver{
		name:     name,
		refusals: make(map[ir.Kind]error),
		silent:   make(map[ir.Kind][]driver.Statement),
	}
}

// Name returns the driver's registered name.
func (d *fakeDriver) Name() string {
	return d.name
}

// Compile returns one marker statement, or whatever this fake was told to return instead.
func (d *fakeDriver) Compile(op ir.Operation) ([]driver.Statement, error) {
	d.asked = append(d.asked, op.Kind())

	if err := d.refuseForeignRaw(op); err != nil {
		return nil, err
	}

	if err, refused := d.refusals[op.Kind()]; refused {
		return nil, err
	}

	if stmts, quiet := d.silent[op.Kind()]; quiet {
		return stmts, nil
	}

	return []driver.Statement{{SQL: "-- " + op.Kind().String()}}, nil
}

// refuseForeignRaw refuses a raw operation addressed elsewhere, which is what a driver must do.
func (d *fakeDriver) refuseForeignRaw(op ir.Operation) error {
	raw, isRaw := op.(ir.Raw)
	if !isRaw || raw.Driver == d.name || d.acceptsForeignRaw {
		return nil
	}

	return driver.Unsupported(d.name, ir.KindRaw, "this raw operation is addressed to "+raw.Driver)
}

// Open reports that this fake has no database behind it.
func (d *fakeDriver) Open(context.Context, string) (driver.Conn, error) {
	return nil, errors.New("the fake driver has no database")
}

// fakeConn is a session with no database, carrying the capability set a driver builds at Open.
type fakeConn struct {
	caps capability.Set
}

// Exec reports that this fake has no database to run a statement against.
func (c *fakeConn) Exec(context.Context, driver.Statement) error {
	return errors.New("the fake connection has no database")
}

// Query reports that this fake has no database to read from.
func (c *fakeConn) Query(context.Context, driver.Query) (driver.Rows, error) {
	return driver.Rows{}, errors.New("the fake connection has no database")
}

// Introspect reports that this fake has no catalog to read a snapshot out of.
func (c *fakeConn) Introspect(context.Context) (*schema.Snapshot, error) {
	return nil, errors.New("the fake connection has no database")
}

// Perform reports that this fake has no database to run a migration's own statement against.
func (c *fakeConn) Perform(context.Context, driver.Query) (int64, error) {
	return 0, errors.New("the fake connection has no database")
}

// Lock hands back a release that succeeds, so a caller's deferred unlock has something to call.
func (c *fakeConn) Lock(context.Context) (driver.Unlock, error) {
	return func(context.Context) error { return nil }, nil
}

// Transact reports that this fake has no database to open a transaction on.
func (c *fakeConn) Transact(context.Context, driver.Work) error {
	return errors.New("the fake connection has no database")
}

// Capabilities returns the set this session was opened with.
func (c *fakeConn) Capabilities() capability.Set {
	return c.caps
}

// History reports no store, since this fake has no database to keep one in.
func (c *fakeConn) History() history.Store {
	return nil
}

// ServerVersion reports the version this fake session was opened against.
func (c *fakeConn) ServerVersion() string {
	return c.caps.Server()
}

// Close releases nothing, since this fake holds nothing.
func (c *fakeConn) Close(context.Context) error {
	return nil
}

// compile-time proof that the fakes satisfy the contracts the suite holds a driver to.
var (
	_ driver.Driver = (*fakeDriver)(nil)
	_ driver.Conn   = (*fakeConn)(nil)
)

// declaring returns a rule for every operation, answering v except for a foreign raw.
func declaring(name string, v capability.Verdict) map[ir.Kind]capability.Rule {
	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(v)
	}

	rules[ir.KindRaw] = func(op ir.Operation) capability.Verdict {
		if raw, isRaw := op.(ir.Raw); isRaw && raw.Driver != name {
			return capability.Unsupported("a raw operation addressed to " + raw.Driver)
		}

		return v
	}

	return rules
}

// mustSet builds a capability set, failing the test if the rule table will not build.
func mustSet(t *testing.T, name string, rules map[ir.Kind]capability.Rule) capability.Set {
	t.Helper()

	set, err := capability.NewSet(capability.Facts{Driver: name, Server: "1.0"}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return set
}

// suiteOf pairs a fake driver with a session declaring one verdict for every operation.
func suiteOf(t *testing.T, d *fakeDriver, v capability.Verdict) Suite {
	t.Helper()

	return suiteDeclaring(t, d, declaring(d.name, v))
}

// suiteDeclaring pairs a fake driver with a session declaring exactly the given rules.
func suiteDeclaring(t *testing.T, d *fakeDriver, rules map[ir.Kind]capability.Rule) Suite {
	t.Helper()

	return Suite{Driver: d, Conn: &fakeConn{caps: mustSet(t, d.name, rules)}}
}

// joined renders a suite's problems as one string, so a test can look for what it expects.
func joined(problems []string) string {
	return strings.Join(problems, "\n")
}
