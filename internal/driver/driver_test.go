package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// fakeDriver compiles to a comment, so no golden file can be generated from it by accident.
type fakeDriver struct {
	name    string
	refuses map[ir.Kind]string
}

// newFake returns a driver that compiles every operation to a marker comment.
func newFake(name string) *fakeDriver {
	return &fakeDriver{name: name, refuses: make(map[ir.Kind]string)}
}

// Name returns the driver's registered name.
func (d *fakeDriver) Name() string { return d.name }

// Compile returns one marker statement, or the refusal this fake was told to give.
func (d *fakeDriver) Compile(op ir.Operation) ([]Statement, error) {
	if reason, refused := d.refuses[op.Kind()]; refused {
		return nil, Unsupported(d.name, op.Kind(), reason)
	}

	return []Statement{{SQL: "-- " + op.Kind().String()}}, nil
}

// Open reports that this fake has no database behind it.
func (d *fakeDriver) Open(_ context.Context, _ string) (Conn, error) {
	return nil, errors.New("the fake driver has no database")
}

// constructor returns a Constructor that hands back this fake.
func (d *fakeDriver) constructor() Constructor {
	return func(Options) (Driver, error) { return d, nil }
}

// fakeConn is a session with no database, carrying the capability set a driver resolves at Open.
type fakeConn struct {
	caps capability.Set
}

// Exec reports that this fake has no database to run a statement against.
func (c *fakeConn) Exec(context.Context, Statement) error {
	return errors.New("the fake connection has no database")
}

// Lock hands back a release that succeeds, so a caller's deferred unlock has something to call.
func (c *fakeConn) Lock(context.Context) (Unlock, error) {
	return func(context.Context) error { return nil }, nil
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

// compile-time proof that the fakes satisfy the contracts they are standing in for.
var (
	_ Driver = (*fakeDriver)(nil)
	_ Conn   = (*fakeConn)(nil)
)

func TestASessionReportsTheCapabilitiesOfItsServer(t *testing.T) {
	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockUnknown))
	}

	set, err := capability.NewSet(capability.Facts{Driver: "fake", Server: "16.2"}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	var conn Conn = &fakeConn{caps: set}

	if got := conn.Capabilities().Server(); got != "16.2" {
		t.Errorf("Capabilities().Server() = %q, want the version the session was opened against", got)
	}

	if !conn.Capabilities().Resolve(ir.DropTable{Table: "users"}).Allowed() {
		t.Error("the session reported a capability its set does not hold")
	}

	t.Log("Capabilities sits on the session rather than the driver because a lock verdict is")
	t.Log("derived from the connected server's version (invariant #13), which a driver holding")
	t.Log("no connection cannot know. A set is built inside Open, so it is bound to one server.")
}

func TestCompilingNeedsNoConnection(t *testing.T) {
	d := newFake("fake")

	ops := ir.Operations{
		ir.DropTable{Table: "users"},
		ir.RenameTable{From: "users", To: "accounts"},
		ir.AddIndex{Table: "users", Name: "idx", Columns: []string{"email"}},
	}

	for _, op := range ops {
		t.Run(string(op.Kind()), func(t *testing.T) {
			stmts, err := d.Compile(op)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}

			if len(stmts) == 0 {
				t.Error("compiling produced no statements")
			}
		})
	}

	if _, err := d.Open(context.Background(), "ignored"); err == nil {
		t.Error("the fake must have no database, so the two paths cannot be confused")
	}

	t.Log("Compile takes no context and no connection, which is what lets migr plan print")
	t.Log("statements without touching a database and lets golden tests run without one.")
}

func TestAStatementRunsInATransactionUnlessItSaysOtherwise(t *testing.T) {
	var zero Statement

	if zero.NoTransaction {
		t.Error("the zero Statement opts out of transactions")
		t.Log("A driver that never sets the field must get the safe behavior. If the flag")
		t.Log("were inverted, forgetting it would silently run DDL outside a transaction.")
	}

	stmt := Statement{SQL: "CREATE INDEX CONCURRENTLY idx ON users (email)", NoTransaction: true}
	if stmt.String() != stmt.SQL {
		t.Errorf("String() = %q, want the statement text %q", stmt, stmt.SQL)
	}
}

func TestAnUnsupportedOperationNamesTheDriverTheOperationAndTheReason(t *testing.T) {
	err := Unsupported("sqlite", ir.KindDropColumn, "requires a table rebuild before 3.35")

	for _, want := range []string{"sqlite", "drop_column", "requires a table rebuild"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
			t.Log("An operator reading this must learn which engine refused, what it refused,")
			t.Log("and whether the refusal is something they can work around.")
		}
	}
}

func TestUnsupportedIsDistinguishableFromAnyOtherCompileFailure(t *testing.T) {
	d := newFake("sqlite")
	d.refuses[ir.KindDropColumn] = "requires a table rebuild before 3.35"

	_, refusal := d.Compile(ir.DropColumn{Table: "users", Column: "nickname"})
	if !errors.Is(refusal, ErrUnsupported) {
		t.Errorf("a declared refusal reported %v, want ErrUnsupported", refusal)
	}

	var unsupported *UnsupportedError
	if !errors.As(refusal, &unsupported) || unsupported.Kind != ir.KindDropColumn {
		t.Errorf("errors.As did not recover the refused operation from %v", refusal)
	}

	if errors.Is(errors.New("syntax error near CREATE"), ErrUnsupported) {
		t.Error("an ordinary failure reported as an unsupported operation")
		t.Log("The conformance suite asserts every op is either compilable or refused with a")
		t.Log("reason -- no third possibility. That is only checkable if a refusal is")
		t.Log("distinguishable from a compiler bug.")
	}
}
