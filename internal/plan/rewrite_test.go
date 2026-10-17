package plan

import (
	"reflect"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// safer rewrites every add_index into a two-statement sequence, standing in for a session.
type safer struct{}

// Rewrite answers for add_index alone, the way a driver answers for what it can make safer.
func (safer) Rewrite(op ir.Operation) (driver.Rewrite, bool) {
	index, ok := op.(ir.AddIndex)
	if !ok {
		return driver.Rewrite{}, false
	}

	return driver.Rewrite{
		Name: driver.RewriteConcurrentIndex,
		Statements: []driver.Statement{
			{SQL: "first " + index.Name},
			{SQL: "second " + index.Name, NoTransaction: true},
		},
		Reason: "it blocks nothing",
		Lock:   capability.LockFree,
	}, true
}

// rewriting is a planner whose session rewrites indexes, with the rewrites it is told to leave on.
func rewriting(on ...driver.RewriteName) Planner {
	return Planner{
		Compiler: byKind(map[ir.Kind]string{ir.KindAddIndex: "a", ir.KindCreateTable: "c", ir.KindRenameTable: "r"}),
		Rewriter: safer{},
		Rewrites: on,
	}
}

// index is the operation every case here may see rewritten.
func index(table string) ir.AddIndex {
	return ir.AddIndex{Table: table, Name: "idx_" + table, Columns: []string{"id"}}
}

func TestARewriteReplacesTheStatementsAndSaysSoOnTheStep(t *testing.T) {
	compiled, err := rewriting(driver.RewriteConcurrentIndex).Compile(added(t), ir.Up,
		ir.Operations{index("users")})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	step := compiled.Steps[0]

	if len(step.Statements) != 2 || step.Statements[1].SQL != "second idx_users" {
		t.Fatalf("the step holds %+v, want the rewritten sequence", step.Statements)
	}

	want := &Rewritten{Name: driver.RewriteConcurrentIndex, Reason: "it blocks nothing",
		Lock: capability.LockFree, Plain: 1}
	if !reflect.DeepEqual(step.Rewrite, want) {
		t.Errorf("the step records %+v, want %+v", step.Rewrite, want)
	}

	if !reflect.DeepEqual(step.Op, ir.Operation(index("users"))) {
		t.Errorf("the step's operation became %v, and a rewrite changes statements, never the operation", step.Op)
	}

	if compiled.Rewritten() != 1 {
		t.Errorf("the plan counts %d rewritten operations, want 1", compiled.Rewritten())
	}

	t.Log("The step keeps the operation the migration emitted beside the statements it now runs as,")
	t.Log("so one declared operation becoming several statements is something a plan can print.")
}

func TestARewriteSwitchedOffChangesNothing(t *testing.T) {
	for name, p := range map[string]Planner{
		"switched off": rewriting(driver.RewriteNotValid),
		"all off":      rewriting(),
		"no rewriter":  {Compiler: rewriting().Compiler, Rewrites: driver.RewriteNames()},
	} {
		compiled, err := p.Compile(added(t), ir.Up, ir.Operations{index("users")})
		if err != nil {
			t.Fatalf("%s: Compile: %v", name, err)
		}

		if step := compiled.Steps[0]; step.Rewrite != nil || len(step.Statements) != 1 {
			t.Errorf("%s: the step was rewritten to %+v", name, step.Statements)
		}
	}
}

func TestAnOperationOnATableThisMigrationCreatesIsNotRewritten(t *testing.T) {
	compiled, err := rewriting(driver.RewriteConcurrentIndex).Compile(added(t), ir.Up, ir.Operations{
		ir.CreateTable{Table: "fresh", Columns: []ir.Column{{Name: "id", Type: fill(t).Type}}},
		index("fresh"),
		ir.RenameTable{From: "fresh", To: "renamed"},
		index("renamed"),
		index("users"),
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for i, want := range []bool{false, false, false, false, true} {
		if got := compiled.Steps[i].Rewrite != nil; got != want {
			t.Errorf("operation %d rewritten %v, want %v", i+1, got, want)
		}
	}

	t.Log("Nothing can be waiting on a table that did not exist when the migration began, and a")
	t.Log("rewrite's bare statements would split the migration for nothing but lost atomicity.")
}

func TestARewriteIsTheSameForEitherDirectionAndEveryCompile(t *testing.T) {
	p := rewriting(driver.RewriteConcurrentIndex)

	first, _ := p.Compile(added(t), ir.Down, ir.Operations{index("users")})

	for range 10 {
		again, _ := p.Compile(added(t), ir.Down, ir.Operations{index("users")})
		if !reflect.DeepEqual(again, first) {
			t.Fatal("two compilations of one migration differ, which invariant #8 forbids")
		}
	}

	if first.Steps[0].Rewrite == nil {
		t.Error("a down's add_index was not rewritten, so undoing a drop would block writes")
	}
}
