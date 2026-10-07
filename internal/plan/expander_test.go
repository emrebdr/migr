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

// emulating returns a planner over a compiler that refuses one kind, and a session that answers it.
func emulating(statements []driver.Statement, err error) (Planner, *fakeExpander) {
	expander := &fakeExpander{statements: statements, err: err}

	return Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}, Expander: expander}, expander
}

// twoOperations is a drop the compiler handles and an alter it refuses, in that order.
func twoOperations() ir.Operations {
	return ir.Operations{
		ir.DropTable{Table: "users"},
		ir.AlterColumn{Table: "users", Column: "email", Nullable: pointerTo(true)},
	}
}

func TestAnOperationTheCompilerCannotProduceIsDeferredRatherThanExpanded(t *testing.T) {
	p, expander := emulating([]driver.Statement{{SQL: "-- rebuilt"}}, nil)

	compiled, err := p.Compile(added(t), ir.Up, twoOperations())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if len(expander.asked) != 0 {
		t.Errorf("compiling asked the session about %v, and it must ask about nothing", expander.asked)
	}

	if compiled.Steps[0].Deferred || len(compiled.Steps[0].Statements) != 1 {
		t.Errorf("the operation the compiler handles was deferred: %+v", compiled.Steps[0])
	}

	if !compiled.Steps[1].Deferred || len(compiled.Steps[1].Statements) != 0 {
		t.Errorf("the emulated operation carries statements already: %+v", compiled.Steps[1])
	}

	if compiled.Deferred() != 1 {
		t.Errorf("the plan reports %d operations still to compile, want the one", compiled.Deferred())
	}

	t.Log("An expansion is rendered from the catalog, so compiling one before the operations ahead")
	t.Log("of it have run renders it from a schema that is about to change (ADR 0071). Compile")
	t.Log("therefore needs no database at all any more, which its signature now says.")
}

func TestADeferredStepIsCompiledWhenTheCallerAsksTheSession(t *testing.T) {
	p, expander := emulating([]driver.Statement{{SQL: "-- rebuilt"}}, nil)

	compiled, err := p.Compile(added(t), ir.Up, twoOperations())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	expanded, err := p.Expand(t.Context(), added(t), compiled.Steps[1])
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	if len(expander.asked) != 1 || expander.asked[0] != ir.KindAlterColumn {
		t.Errorf("the session was asked about %v, want only alter_column", expander.asked)
	}

	if !expander.sawContext {
		t.Error("the session was given no context, so nothing it reads can be canceled")
	}

	if expanded.Deferred {
		t.Error("an expanded step still reports itself deferred, so it would be expanded twice")
	}

	if expanded.Index != compiled.Steps[1].Index || expanded.Op.Kind() != ir.KindAlterColumn {
		t.Errorf("the expanded step is %+v, which is not the step it was asked about", expanded)
	}

	if got := expanded.Statements[0].SQL; got != "-- rebuilt" {
		t.Errorf("the step carries %q rather than what the session produced", got)
	}

	t.Log("An expansion produces statements rather than operations, because the shadow table it")
	t.Log("creates carries CHECK and FOREIGN KEY clauses the IR has no way to express -- so a")
	t.Log("rebuild routed back through create_table would lose exactly what it exists to keep.")
}

func TestWithoutASessionTheOperationIsReportedRatherThanCompiled(t *testing.T) {
	p := Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}}

	_, err := p.Compile(added(t), ir.Up, ir.Operations{
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
	t.Log("apart and report it unchecked rather than as a problem (ADR 0040's shape). Deferring it")
	t.Log("instead would hand validate a step nothing could ever fill in.")
}

func TestADeferredStepHandedToAPlannerWithNoSessionIsRefused(t *testing.T) {
	p, _ := emulating([]driver.Statement{{SQL: "-- rebuilt"}}, nil)

	compiled, err := p.Compile(added(t), ir.Up, twoOperations())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	offline := Planner{Compiler: sessionCompiler{needs: ir.KindAlterColumn}}

	_, err = offline.Expand(t.Context(), added(t), compiled.Steps[1])
	if !errors.Is(err, ErrNoExpander) {
		t.Errorf("Expand returned %v, want %v", err, ErrNoExpander)
	}

	var failed *CompileError
	if !errors.As(err, &failed) || failed.Kind != ir.KindAlterColumn {
		t.Errorf("the refusal does not name the operation: %v", err)
	}
}

func TestAFailedExpansionIsTheMigrationsFailure(t *testing.T) {
	p, _ := emulating(nil, errors.New("no such table"))

	compiled, err := p.Compile(added(t), ir.Up, twoOperations())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = p.Expand(t.Context(), added(t), compiled.Steps[1])

	var failed *CompileError
	if !errors.As(err, &failed) {
		t.Fatalf("Expand returned %v, which does not name the migration", err)
	}
}

func TestAnExpansionThatProducesNothingIsRefused(t *testing.T) {
	p, _ := emulating([]driver.Statement{}, nil)

	compiled, err := p.Compile(added(t), ir.Up, twoOperations())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = p.Expand(t.Context(), added(t), compiled.Steps[1])
	if !errors.Is(err, ErrNothingCompiled) {
		t.Errorf("Expand returned %v, want %v", err, ErrNothingCompiled)
	}

	t.Log("An expansion goes through the same check a compilation does: reporting success while")
	t.Log("producing nothing that runs is the failure this package exists to catch, and a session")
	t.Log("producing it is no different from a compiler producing it.")
}

// pointerTo returns a pointer to a value, for the operation fields that are tri-state.
func pointerTo[T any](v T) *T {
	return &v
}
