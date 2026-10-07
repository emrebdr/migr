package driver

import (
	"testing"

	"github.com/emrebdr/migr/internal/schema"
)

// The columns the mutation adds, stated once so the target and the operations cannot drift.
const (
	labelColumn = "label"
	codeColumn  = "code"
	addedColumn = "added"
)

// extra names the table the mutation adds, under the prefix the caller gave.
func (s Diff) extra() string { return s.Prefix + "_extra" }

// named renders one of the mutation's constraint names under the prefix the caller gave.
func (s Diff) named(suffix string) string { return s.Prefix + "_" + suffix }

// Every change here is one an operation can express, and together they reach every dependency the
// order exists for: a table that must be created before the key referencing it, an index that must
// go before the column it covers, and a column whose nullability moves while it carries a default --
// which is the one case a driver that changes a column by restating its whole definition refuses
// unless the operation states the default too (ADR 0058).

// mutate returns the schema this suite asks the differ to reach, built out of the one it was given.
func (s Diff) mutate(t *testing.T, before schema.Snapshot) schema.Snapshot {
	t.Helper()

	after := before.Canonical()

	parent, held := after.Table(s.base().parent())
	if !held {
		t.Fatalf("the fixture's table %q is not in the schema it was read back as", s.base().parent())
	}

	for i := range after.Tables {
		switch after.Tables[i].Name {
		case s.base().parent():
			after.Tables[i] = s.widened(t, after.Tables[i])
		case s.base().child():
			after.Tables[i] = s.narrowed(t, after.Tables[i])
		}
	}

	after.Tables = append(after.Tables, s.addedTable(t, parent))

	return after
}

// widened returns the fixture's referenced table with one more column than it was read back with.
func (s Diff) widened(t *testing.T, parent schema.Table) schema.Table {
	t.Helper()

	parent.Columns = append(parent.Columns, schema.Column{
		Name: addedColumn, Type: columnTypeOf(t, parent, stringColumn), Nullable: true,
	})

	return parent
}

// narrowed returns the fixture's referencing table with its index and one column gone, and one tightened.
func (s Diff) narrowed(t *testing.T, child schema.Table) schema.Table {
	t.Helper()

	if _, held := child.Column(dateColumn); !held {
		t.Fatalf("the mutation drops column %q, which table %q does not hold",
			dateColumn, child.Name)
	}

	child.Indexes = nil
	child.Columns = tightened(withoutColumn(child.Columns, dateColumn), stringColumn)

	return child
}

// addedTable returns the table the mutation adds, whose every type is one already in the schema.
func (s Diff) addedTable(t *testing.T, parent schema.Table) schema.Table {
	t.Helper()

	key, text := columnTypeOf(t, parent, keyColumn), columnTypeOf(t, parent, stringColumn)

	return schema.Table{
		Name: s.extra(),
		Columns: []schema.Column{
			{Name: keyColumn, Type: key},
			{Name: codeColumn, Type: text, Nullable: true},
			{Name: labelColumn, Type: text, Nullable: true},
			{Name: parentColumn, Type: key, Nullable: true},
		},
		PrimaryKey: &schema.PrimaryKey{Columns: []string{keyColumn}},
		Indexes:    []schema.Index{{Name: s.named("xidx"), Columns: []string{labelColumn}}},
		Uniques:    []schema.Unique{{Name: s.named("xuq"), Columns: []string{codeColumn}}},
		ForeignKeys: []schema.ForeignKey{{
			Name: s.named("xfk"), Columns: []string{parentColumn},
			ReferencesTable: s.base().parent(), ReferencesColumns: []string{keyColumn},
			OnDelete: schema.ActionCascade, OnUpdate: schema.ActionNoAction,
		}},
	}
}

// withoutColumn returns a column list with one of them gone, which is what a drop reaches.
func withoutColumn(columns []schema.Column, name string) []schema.Column {
	kept := make([]schema.Column, 0, len(columns))

	for _, column := range columns {
		if column.Name != name {
			kept = append(kept, column)
		}
	}

	return kept
}

// tightened returns a column list with one of them no longer accepting null.
func tightened(columns []schema.Column, name string) []schema.Column {
	changed := make([]schema.Column, 0, len(columns))

	for _, column := range columns {
		if column.Name == name {
			column.Nullable = false
		}

		changed = append(changed, column)
	}

	return changed
}

// columnTypeOf returns one column's type as this engine reads it, which is what the mutation copies.
func columnTypeOf(t *testing.T, table schema.Table, column string) schema.ColumnType {
	t.Helper()

	found, held := table.Column(column)
	if !held {
		t.Fatalf("the mutation copies the type of %q, which table %q does not hold",
			column, table.Name)
	}

	return found.Type
}
