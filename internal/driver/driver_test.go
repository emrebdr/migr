package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

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

// compile-time proof that the fake satisfies the contract it is standing in for.
var _ Driver = (*fakeDriver)(nil)

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
