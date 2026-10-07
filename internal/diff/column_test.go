package diff

import (
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// altered returns the one alter_column a case proposes, failing on anything else.
func altered(t *testing.T, from, to schema.Snapshot) ir.AlterColumn {
	t.Helper()

	ops, refused := Diff(from, to)

	if len(refused) != 0 {
		t.Fatalf("this change was refused: %s", describeAll(refused))
	}

	if len(ops) != 1 {
		t.Fatalf("it proposed %v, want one alter_column", kindsOf(ops))
	}

	op, ok := ops[0].(ir.AlterColumn)
	if !ok {
		t.Fatalf("it proposed a %T", ops[0])
	}

	return op
}

// tighten returns the one-table schema with one column no longer accepting null.
func tighten(t *testing.T, name string) schema.Snapshot {
	t.Helper()

	return changed(users(t), "users", func(table schema.Table) schema.Table {
		return rewriting(table, name, tightened)
	})
}

func TestItStatesADefaultItIsNotChanging(t *testing.T) {
	op := altered(t, users(t), tighten(t, "note"))

	if op.Default == nil {
		t.Error("it changed a column's nullability without stating the default the column keeps")
		t.Log("MySQL and MariaDB both refuse that: a column changes only by restating its whole")
		t.Log("definition, and a redefinition drops every attribute it was not told about")
		t.Log("(ADR 0058). Measured on both servers -- a minimal delta is error-free SQL that")
		t.Log("silently loses the default, which is why the driver refuses it rather than emits it.")

		return
	}

	if op.Default.Literal == nil || *op.Default.Literal != "" {
		t.Errorf("it states a default of %v, and the column's own is the empty literal", op.Default)
	}

	if op.From.Default == nil || *op.From.Default.Literal != "" {
		t.Errorf("it records a prior default of %v, so its inverse would not restore one",
			op.From.Default)
	}
}

func TestItDoesNotRestateWhatItIsNotChanging(t *testing.T) {
	op := altered(t, users(t), tighten(t, "note"))

	if op.Type != nil {
		t.Errorf("it restates the type as %s, which it is not changing", op.Type)
		t.Log("ir.AlterColumn.Destructive() keys on the type's presence, so restating an unchanged")
		t.Log("one would make every column change need permitting by --allow-destructive.")
	}

	if op.Destructive() {
		t.Error("a nullability change is reported as destructive")
	}

	retyped := changed(users(t), "users", func(table schema.Table) schema.Table {
		return rewriting(table, "email", func(column schema.Column) schema.Column {
			column.Type = exact(t, "text")

			return column
		})
	})

	widened := altered(t, users(t), retyped)

	if widened.Nullable != nil {
		t.Errorf("it restates the nullability as %v, which it is not changing", *widened.Nullable)
		t.Log("PostgreSQL validates every row on SET NOT NULL, so restating an unchanged one turns")
		t.Log("a catalog-only change into a full scan -- which is invariant #12's concern.")
	}
}

func TestItRecordsOnlyWhatItIsChanging(t *testing.T) {
	op := altered(t, users(t), tighten(t, "note"))

	if op.From.Nullable == nil || *op.From.Nullable != true {
		t.Errorf("it records a prior nullability of %v, so its inverse would not restore it",
			op.From.Nullable)
	}

	if op.From.Type != nil {
		t.Errorf("it records a prior type of %s for a change that does not touch the type",
			op.From.Type)
	}
}

func TestItDropsADefaultTheTargetDoesNotHave(t *testing.T) {
	op := altered(t, users(t), changed(users(t), "users", func(table schema.Table) schema.Table {
		return rewriting(table, "note", func(column schema.Column) schema.Column {
			column.Default = nil

			return column
		})
	}))

	if !op.DropDefault {
		t.Error("it did not take the default away")
	}

	if op.From.Default == nil {
		t.Error("it records nothing of the default it took away")
	}
}

func TestItCreatesOffAGuessBecauseThereIsNothingToCompare(t *testing.T) {
	guessed := changed(users(t), "users", func(table schema.Table) schema.Table {
		return withColumn(table, schema.Column{Name: "count",
			Type: ambiguous(t, "int64", "integer"), Nullable: true})
	})

	ops, refused := Diff(users(t), guessed)

	if len(refused) != 0 {
		t.Fatalf("adding a column whose type the catalog only guesses at was refused: %s",
			describeAll(refused))
	}

	if len(ops) != 1 {
		t.Fatalf("it proposed %v, want one add_column", kindsOf(ops))
	}

	added, ok := ops[0].(ir.AddColumn)
	if !ok {
		t.Fatalf("it proposed a %T", ops[0])
	}

	if added.Column.Type.String() != "int64" {
		t.Errorf("it adds the column as %s, and the best answer available is int64",
			added.Column.Type)
	}

	t.Log("Creating is not changing, and the asymmetry is deliberate: there is nothing to compare")
	t.Log("a new column against, the guess is the only answer there is, and no data is at risk.")
	t.Log("A change off a guess is a rewrite proposed from one, which is what is refused.")
}

func TestItProposesNothingWhenAGuessAgrees(t *testing.T) {
	guessed := changed(users(t), "users", func(table schema.Table) schema.Table {
		return rewriting(table, "id", func(column schema.Column) schema.Column {
			column.Type = ambiguous(t, "int64", "integer")

			return column
		})
	})

	ops, refused := Diff(users(t), guessed)

	if len(ops) != 0 || len(refused) != 0 {
		t.Errorf("an exact int64 against a guessed one proposed %v and refused %s",
			kindsOf(ops), describeAll(refused))
		t.Log("ColumnType.Equal is deliberate that a guess never equals a fact (ADR 0065), so the")
		t.Log("differ compares the logical answer instead: on SQLite every integer column reads")
		t.Log("back ambiguous, and taking the model's equality here would refuse all of them.")
	}
}

func TestItRecordsThatThereWasNoDefaultToRestore(t *testing.T) {
	op := altered(t, users(t), changed(users(t), "users", func(table schema.Table) schema.Table {
		return rewriting(table, "email", func(column schema.Column) schema.Column {
			column.Default = literal("nobody@example.com")

			return column
		})
	}))

	if op.Default == nil {
		t.Fatal("it did not give the column the default the target holds")
	}

	if !op.From.NoDefault {
		t.Error("it does not record that the column had no default, so its inverse would leave one")
		t.Log("ir.ColumnState distinguishes a recorded default from a recorded absence for exactly")
		t.Log("this: nil means unchanged, and no_default means there was none.")
	}
}
