package plan

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// sessionCompiler asks for a session for one kind, and compiles everything else the usual way.
type sessionCompiler struct {
	needs ir.Kind
}

// Compile reports that one operation is compiled against a database, and compiles the rest.
func (c sessionCompiler) Compile(op ir.Operation) ([]driver.Statement, error) {
	if op.Kind() == c.needs {
		return nil, driver.NeedsSession("fake", op.Kind(), "it is emulated")
	}

	return []driver.Statement{{SQL: "-- " + op.Kind().String()}}, nil
}

// fakeExpander is a session that answers for the operation the compiler would not.
type fakeExpander struct {
	statements []driver.Statement
	err        error
	asked      []ir.Kind
	sawContext bool
}

// Expand records what it was asked and returns whatever this fake was told to.
func (e *fakeExpander) Expand(ctx context.Context, op ir.Operation) ([]driver.Statement, error) {
	e.asked = append(e.asked, op.Kind())
	e.sawContext = ctx != nil

	return e.statements, e.err
}

func TestAnOperationTheCompilerCannotProduceIsAskedOfTheSession(t *testing.T) {
	expander := &fakeExpander{statements: []driver.Statement{{SQL: "-- rebuilt"}}}

	p := Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}, Expander: expander}

	compiled, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{
		ir.DropTable{Table: "users"},
		ir.AlterColumn{Table: "users", Column: "email", Nullable: pointerTo(true)},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if len(expander.asked) != 1 || expander.asked[0] != ir.KindAlterColumn {
		t.Errorf("the session was asked about %v, want only alter_column", expander.asked)
	}

	if !expander.sawContext {
		t.Error("the session was given no context, so nothing it reads can be canceled")
	}

	if got := compiled.Steps[1].Statements[0].SQL; got != "-- rebuilt" {
		t.Errorf("the plan carries %q rather than what the session produced", got)
	}

	t.Log("An expansion produces statements rather than operations, because the shadow table it")
	t.Log("creates carries CHECK and FOREIGN KEY clauses the IR has no way to express -- so a")
	t.Log("rebuild routed back through create_table would lose exactly what it exists to keep.")
}

func TestWithoutASessionTheOperationIsReportedRatherThanCompiled(t *testing.T) {
	p := Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{
		ir.AlterColumn{Table: "users", Column: "email", Nullable: pointerTo(true)},
	})

	if !errors.Is(err, driver.ErrNeedsSession) {
		t.Fatalf("Compile returned %v, want %v", err, driver.ErrNeedsSession)
	}

	var failed *CompileError
	if !errors.As(err, &failed) || failed.Kind != ir.KindAlterColumn {
		t.Errorf("the failure does not name the operation: %v", err)
	}

	t.Log("This is what migr validate sees, since it opens no database: the operation is not")
	t.Log("refused and it is not judged either, and the sentinel is what lets validate tell those")
	t.Log("apart and report it unchecked rather than as a problem (ADR 0040's shape).")
}

func TestAFailedExpansionIsTheMigrationsFailure(t *testing.T) {
	expander := &fakeExpander{err: errors.New("no such table")}

	p := Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}, Expander: expander}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{
		ir.AlterColumn{Table: "users", Column: "email", Nullable: pointerTo(true)},
	})

	var failed *CompileError
	if !errors.As(err, &failed) {
		t.Fatalf("Compile returned %v, which does not name the migration", err)
	}
}

func TestAnExpansionThatProducesNothingIsRefused(t *testing.T) {
	expander := &fakeExpander{statements: []driver.Statement{}}

	p := Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}, Expander: expander}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{
		ir.AlterColumn{Table: "users", Column: "email", Nullable: pointerTo(true)},
	})

	if !errors.Is(err, ErrNothingCompiled) {
		t.Errorf("Compile returned %v, want %v", err, ErrNothingCompiled)
	}

	t.Log("An expansion goes through the same check a compilation does: reporting success while")
	t.Log("producing nothing that runs is the failure this package exists to catch, and a session")
	t.Log("producing it is no different from a compiler producing it.")
}

// pointerTo returns a pointer to a value, for the operation fields that are tri-state.
func pointerTo[T any](v T) *T {
	return &v
}
