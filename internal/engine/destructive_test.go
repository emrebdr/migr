package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// destroying is one operation per destructive kind, each naming something a message can print.
func destroying(t *testing.T) ir.Operations {
	t.Helper()

	narrowed, err := schema.Parse("string(64)")
	if err != nil {
		t.Fatalf("parsing a column type: %v", err)
	}

	return ir.Operations{
		ir.DropTable{Table: "legacy"},
		ir.DropColumn{Table: "users", Column: "email"},
		ir.AlterColumn{Table: "users", Column: "note", Type: &narrowed},
		ir.DropView{View: "listing"},
		ir.Raw{Driver: "fake", SQL: "DELETE FROM users"},
	}
}

func TestEveryDestructiveOperationIsFoundAndNamed(t *testing.T) {
	found := Destructive(destroying(t), Allowance{})

	if len(found) != len(ir.DestructiveKinds()) {
		t.Fatalf("%d operations were found, want one per destructive kind: %+v", len(found), found)
	}

	subjects := []string{
		"legacy", "users.email", "users.note", "listing", "DELETE FROM users",
	}

	for i, d := range found {
		if d.Operation != int32(i)+1 {
			t.Errorf("finding %d names operation %d", i+1, d.Operation)
		}

		if d.Subject != subjects[i] {
			t.Errorf("%s names subject %q, want %q", d.Kind, d.Subject, subjects[i])
			t.Log("The subject is what --json carries and what migr plan marks an operation with,")
			t.Log("so a table where a column belongs reads as a different change entirely.")
		}

		if d.Detail == "" || strings.Contains(d.Detail, "nothing here can say what") {
			t.Errorf("%s says nothing specific about what it destroys: %q", d.Kind, d.Detail)
			t.Log("The done-when asks for the affected object, so every kind that can destroy has")
			t.Log("a sentence of its own. The fallback is for an operation nobody classified.")
		}
	}
}

func TestAnAllowanceCoversTheKindsItNamesAndNothingElse(t *testing.T) {
	ops := destroying(t)

	if found := Destructive(ops, Allowance{All: true}); found != nil {
		t.Errorf("--allow-destructive left %d operations refused", len(found))
	}

	found := Destructive(ops, Allowance{Kinds: []ir.Kind{ir.KindDropTable, ir.KindRaw}})

	var left []ir.Kind
	for _, d := range found {
		left = append(left, d.Kind)
	}

	want := []ir.Kind{ir.KindDropColumn, ir.KindAlterColumn, ir.KindDropView}
	if !slices.Equal(left, want) {
		t.Errorf("permitting drop_table and raw left %v, want %v", left, want)
	}
}

func TestAHarmlessOperationNeedsNoAllowance(t *testing.T) {
	harmless := ir.Operations{
		addIndex("idx"),
		ir.AlterColumn{Table: "users", Column: "note", DropDefault: true},
	}

	if found := Destructive(harmless, Allowance{}); found != nil {
		t.Errorf("an allowance that permits nothing refused %+v", found)
		t.Log("An alter_column that drops a default is the payload the IR reports harmless, and")
		t.Log("the kind alone cannot decide one: permission is asked per operation, not per kind.")
	}
}

func TestEveryKindNamesASubjectSoANewOneCannotBeForgotten(t *testing.T) {
	for _, op := range drivertest.Operations("fake") {
		if Subject(op) == "" {
			t.Errorf("%s names no subject", op.Kind())
			t.Log("Subject is a type switch, which the exhaustive linter does not read, so this")
			t.Log("is what carries it: drivertest.Operations holds one operation per kind and is")
			t.Log("itself held to ir.Kinds(), so a twenty-first operation fails here.")
		}
	}
}

func TestADestructiveMigrationIsRefusedBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, src := staged(t, "a")
	src.ops[versionAt(0)] = ir.Operations{ir.DropTable{Table: "legacy"}}

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrDestructive) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrDestructive", err)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Fatalf("the session was asked for %v before the refusal", conn.calls)
	}

	var refusal *DestructiveError
	if !errors.As(err, &refusal) {
		t.Fatalf("Apply returned %v, which is not a *DestructiveError", err)
	}

	for _, want := range []string{
		migrationAt(t, 0).Filename(),
		"operation 1",
		string(ir.KindDropTable),
		`"legacy"`,
		"--allow-destructive",
		"allow_destructive: [drop_table]",
	} {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, refusal.Error())
		}
	}
}

func TestAPermittedMigrationRunsAndTheRefusalSaysWhatWasPermitted(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, src := staged(t, "a")
	src.ops[versionAt(0)] = ir.Operations{
		ir.DropTable{Table: "legacy"},
		ir.DropView{View: "listing"},
	}
	opts.Allow = Allowance{Kinds: []ir.Kind{ir.KindDropTable}}

	_, err := Apply(t.Context(), conn, opts)

	var refusal *DestructiveError
	if !errors.As(err, &refusal) {
		t.Fatalf("Apply returned %v, which is not a *DestructiveError", err)
	}

	if !strings.Contains(refusal.Error(), "This project permits drop_table") {
		t.Errorf("the refusal does not say what is already permitted:\n%s", refusal.Error())
		t.Log("An allowance that covers some of the operations reads as none at all unless the")
		t.Log("refusal says which ones it covered.")
	}

	opts.Allow = Allowance{Kinds: []ir.Kind{ir.KindDropTable, ir.KindDropView}}

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply over a fully permitted migration returned %v", err)
	}
}

func TestARollbackIsNotGatedByTheAllowance(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")
	src.ops[versionAt(0)] = ir.Operations{addIndex("a0")}

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback returned %v with nothing permitted", err)
	}

	t.Log("The inverse of add_index is drop_index, which destroys nothing -- but the inverse of")
	t.Log("create_table is drop_table, so gating a rollback would make the allowance mandatory")
	t.Log("for migr down outright. A rollback is the operator's own instruction.")
}

func TestADerivedInverseThatDropsATableStillRollsBack(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")
	src.ops[versionAt(0)] = ir.Operations{created(t, "users")}

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback of a created table returned %v with nothing permitted", err)
	}
}

// created builds a create_table whose derived inverse is the drop_table a rollback runs.
func created(t *testing.T, name string) ir.CreateTable {
	t.Helper()

	int64Type, err := schema.Parse("int64")
	if err != nil {
		t.Fatalf("parsing a column type: %v", err)
	}

	return ir.CreateTable{Table: name, Columns: []ir.Column{{Name: "id", Type: int64Type}}}
}
