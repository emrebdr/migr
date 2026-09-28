package plan

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// errRefused is what a test compiler returns when it is asked to refuse an operation.
var errRefused = errors.New("this driver does not do that")

// compiler is a Compile implementation a test controls, standing in for a driver.
type compiler struct {
	compile func(op ir.Operation) ([]driver.Statement, error)
}

// Compile turns an operation into whatever the test said it should.
func (c compiler) Compile(op ir.Operation) ([]driver.Statement, error) {
	return c.compile(op)
}

// byKind compiles each operation into one statement per letter of the spec its kind maps to.
func byKind(spec map[ir.Kind]string) compiler {
	return compiler{compile: func(op ir.Operation) ([]driver.Statement, error) {
		text, ok := spec[op.Kind()]
		if !ok {
			return nil, errRefused
		}

		stmts := make([]driver.Statement, 0, len(text))
		for _, r := range text {
			stmts = append(stmts, driver.Statement{SQL: string(r) + " " + op.Kind().String()})
		}

		return stmts, nil
	}}
}

// added is the migration every test in this package compiles.
func added(t *testing.T) migration.Migration {
	t.Helper()

	m, err := migration.Parse("20260727T153000_add_users.sql")
	if err != nil {
		t.Fatalf("parsing the fixture filename: %v", err)
	}

	return m
}

// three is an operation list whose kinds are distinct, so a step can be told from its neighbors.
func three() ir.Operations {
	return ir.Operations{
		ir.DropTable{Table: "users"},
		ir.RenameTable{From: "orders", To: "purchases"},
		ir.DropView{View: "active_users"},
	}
}

func TestEachOperationKeepsItsOwnStatementsInItsOwnStep(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{
		ir.KindDropTable:   "a",
		ir.KindRenameTable: "bc",
		ir.KindDropView:    "d",
	})}

	compiled, err := p.Compile(t.Context(), added(t), ir.Up, three())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if len(compiled.Steps) != 3 {
		t.Fatalf("compiled to %d steps, want one per operation (3)", len(compiled.Steps))
	}

	for i, want := range []int{1, 2, 1} {
		if got := len(compiled.Steps[i].Statements); got != want {
			t.Fatalf("step %d holds %d statements, want %d", i+1, got, want)
		}
	}

	t.Log("A flat statement list would answer how many statements a migration has and nothing")
	t.Log("else. Invariant #9 wants every error to name the offending operation, and M13-T5 has")
	t.Log("to show that one declared operation became three statements.")
}

func TestAStepIsNumberedFromOneAndCarriesTheOperationItWasGiven(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{
		ir.KindDropTable:   "a",
		ir.KindRenameTable: "b",
		ir.KindDropView:    "c",
	})}

	ops := three()

	compiled, err := p.Compile(t.Context(), added(t), ir.Up, ops)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var want int32

	for i, step := range compiled.Steps {
		want++

		if step.Index != want {
			t.Fatalf("step %d is numbered %d, want %d", i, step.Index, want)
		}

		if step.Op.Kind() != ops[i].Kind() {
			t.Fatalf("step %d carries %s, want the operation it was given (%s)",
				step.Index, step.Op.Kind(), ops[i].Kind())
		}
	}

	t.Log("Numbering from one matches the golden-file harness and history.Record.LastStatement,")
	t.Log("so zero is unambiguously not a position anywhere in this codebase.")
}

func TestStatementsFlattenInTheOrderTheyMustRun(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{
		ir.KindDropTable:   "ab",
		ir.KindRenameTable: "c",
		ir.KindDropView:    "de",
	})}

	compiled, err := p.Compile(t.Context(), added(t), ir.Up, three())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	stmts := compiled.Statements()
	if got := len(stmts); got != 5 {
		t.Fatalf("flattened to %d statements, want 5", got)
	}

	var letters strings.Builder
	for _, stmt := range stmts {
		letters.WriteString(stmt.SQL[:1])
	}

	if got := letters.String(); got != "abcde" {
		t.Fatalf("flattened to %q, want the compiled order abcde", got)
	}

	if int(compiled.Len()) != len(stmts) {
		t.Fatalf("Len reports %d and Statements returns %d; they describe one plan",
			compiled.Len(), len(stmts))
	}

	t.Log("The executor runs the flattened list, so an order that differs from the compiled one")
	t.Log("would apply a migration's operations in an order the migration did not ask for.")
}

func TestAStatementPositionResolvesToTheOperationItCameFrom(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{
		ir.KindDropTable:   "ab",
		ir.KindRenameTable: "c",
		ir.KindDropView:    "de",
	})}

	compiled, err := p.Compile(t.Context(), added(t), ir.Up, three())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for position, want := range map[int32]ir.Kind{
		1: ir.KindDropTable,
		2: ir.KindDropTable,
		3: ir.KindRenameTable,
		4: ir.KindDropView,
		5: ir.KindDropView,
	} {
		op, ok := compiled.At(position)
		if !ok {
			t.Fatalf("statement %d resolved to no operation, want %s", position, want)
		}

		if op.Kind() != want {
			t.Fatalf("statement %d resolved to %s, want %s", position, op.Kind(), want)
		}
	}

	t.Log("engine.StatementError reports a position. This is what turns it back into the")
	t.Log("operation invariant #9 requires the message to name.")
}

func TestAPositionOutsideThePlanResolvesToNothing(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{ir.KindDropTable: "a"})}

	compiled, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{ir.DropTable{Table: "users"}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for _, position := range []int32{-1, 0, 2} {
		if op, ok := compiled.At(position); ok {
			t.Fatalf("position %d resolved to %s, want nothing", position, op.Kind())
		}
	}

	t.Log("A statement position arrives from a driver's error, so it is not this package's to")
	t.Log("trust. Answering with a plausible operation would misname the one that failed.")
}

func TestAnOperationThatCompilesToNoStatementsIsRefused(t *testing.T) {
	p := Planner{Compiler: compiler{compile: func(ir.Operation) ([]driver.Statement, error) {
		return nil, nil
	}}}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{ir.DropTable{Table: "users"}})
	if !errors.Is(err, ErrNothingCompiled) {
		t.Fatalf("compiling to no statements returned %v, want ErrNothingCompiled", err)
	}

	t.Log("A driver that returns no statements reports success and changes nothing. M2-T4 fails")
	t.Log("that across a driver's whole operation set; this catches it for the operations one")
	t.Log("migration actually emitted, which is where it would be a schema change that vanished.")
}

func TestAnOperationThatCompilesToBlankSQLIsRefused(t *testing.T) {
	p := Planner{Compiler: compiler{compile: func(ir.Operation) ([]driver.Statement, error) {
		return []driver.Statement{{SQL: "DROP TABLE users"}, {SQL: "   \n\t "}}, nil
	}}}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{ir.DropTable{Table: "users"}})
	if !errors.Is(err, ErrNothingCompiled) {
		t.Fatalf("compiling to a blank statement returned %v, want ErrNothingCompiled", err)
	}

	t.Log("A blank statement is the same failure hiding behind a non-empty slice, and the")
	t.Log("executor would count it as a statement that ran.")
}

func TestARefusedOperationIsNamedWithItsMigrationAndItsPosition(t *testing.T) {
	p := Planner{Compiler: compiler{compile: func(op ir.Operation) ([]driver.Statement, error) {
		if op.Kind() == ir.KindRenameTable {
			return nil, errRefused
		}

		return []driver.Statement{{SQL: "SELECT 1"}}, nil
	}}}

	_, err := p.Compile(t.Context(), added(t), ir.Up, three())

	var failure *CompileError
	if !errors.As(err, &failure) {
		t.Fatalf("a refused operation returned %v, want a *CompileError", err)
	}

	if failure.Index != 2 {
		t.Fatalf("the refusal names operation %d, want 2", failure.Index)
	}

	if failure.Kind != ir.KindRenameTable {
		t.Fatalf("the refusal names %s, want %s", failure.Kind, ir.KindRenameTable)
	}

	if !strings.Contains(failure.Error(), "20260727T153000_add_users.sql") {
		t.Fatalf("the refusal reads %q and does not name the migration", failure.Error())
	}

	if !errors.Is(err, errRefused) {
		t.Fatalf("the refusal does not unwrap to what the driver reported")
	}

	t.Log("Invariant #9: every user-facing error names the migration ID and the offending")
	t.Log("operation. A driver's refusal names the operation but has never heard of the file.")
}

func TestAMalformedOperationIsRefusedBeforeTheDriverSeesIt(t *testing.T) {
	var asked bool

	p := Planner{Compiler: compiler{compile: func(ir.Operation) ([]driver.Statement, error) {
		asked = true

		return []driver.Statement{{SQL: "SELECT 1"}}, nil
	}}}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{ir.DropTable{Table: ""}})
	if err == nil {
		t.Fatalf("an operation naming no table compiled cleanly")
	}

	if asked {
		t.Fatalf("the driver was asked to compile an operation that is not valid")
	}

	t.Log("Validating first is what lets migr plan find a malformed migration with no database,")
	t.Log("rather than turning it into SQL a server rejects at apply time.")
}

func TestAPlannerWithNoCompilerIsRefusedRatherThanPanicking(t *testing.T) {
	_, err := Planner{}.Compile(t.Context(), added(t), ir.Up, ir.Operations{ir.DropTable{Table: "users"}})
	if !errors.Is(err, ErrNoCompiler) {
		t.Fatalf("a planner with no compiler returned %v, want ErrNoCompiler", err)
	}

	t.Log("Panic is banned in library code (invariant #9), and a Planner is a struct literal a")
	t.Log("caller can leave half filled in.")
}

func TestAHoleInTheOperationListIsRefusedRatherThanPanicking(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{ir.KindDropTable: "a"})}

	_, err := p.Compile(t.Context(), added(t), ir.Up, ir.Operations{ir.DropTable{Table: "users"}, nil})
	if !errors.Is(err, ErrNoOperation) {
		t.Fatalf("a nil operation returned %v, want ErrNoOperation", err)
	}

	t.Log("ir.Operations is a slice of an interface, so a hole is representable. Reaching for")
	t.Log("Kind on it would take down a migration run rather than report a caller's bug.")
}

func TestCompilingTheSameMigrationTwiceProducesTheSamePlan(t *testing.T) {
	p := Planner{Compiler: byKind(map[ir.Kind]string{
		ir.KindDropTable:   "ab",
		ir.KindRenameTable: "c",
		ir.KindDropView:    "de",
	})}

	first, err := p.Compile(t.Context(), added(t), ir.Up, three())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	second, err := p.Compile(t.Context(), added(t), ir.Up, three())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !equal(first.Statements(), second.Statements()) {
		t.Fatalf("two compilations of one migration disagree:\n%v\n%v",
			first.Statements(), second.Statements())
	}

	t.Log("Invariant #8: planning paths are deterministic. Golden tests, checksums and the")
	t.Log("agreement between migr plan and migr up all rest on this.")
}

// equal reports whether two compiled statement lists are the same statements in the same order.
func equal(a, b []driver.Statement) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
