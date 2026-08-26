package migr

import (
	"errors"
	"fmt"
)

// TableDef is the definition a dropped table had, enough to recreate it.
type TableDef struct {
	// Columns is every column the table carried, in order.
	Columns []Column `json:"columns"`
	// PrimaryKey names the key columns, omitted when the table had none.
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// check reports a recorded definition that could not rebuild a table.
func (d *TableDef) check() error {
	if d == nil {
		return nil
	}

	if len(d.Columns) == 0 {
		return errors.New("from.columns must list at least one column")
	}

	seen := make(map[string]bool, len(d.Columns))

	for i, c := range d.Columns {
		if err := c.check(); err != nil {
			return fmt.Errorf("from.columns[%d]: %w", i, err)
		}

		seen[c.Name] = true
	}

	return checkKeyColumns("from.primary_key", d.PrimaryKey, seen)
}

// ColumnsDef is a bare column list: the definition of an index, unique, or primary key.
type ColumnsDef struct {
	// Columns names the columns the dropped object covered, in order.
	Columns []string `json:"columns"`
}

// check reports a recorded column list that is absent, unnamed, or repeated.
func (d *ColumnsDef) check() error {
	if d == nil {
		return nil
	}

	if err := requireColumns("from.columns", d.Columns); err != nil {
		return err
	}

	return requireDistinctIn("from.columns", d.Columns)
}

// ForeignKeyDef is the definition a dropped foreign key had.
type ForeignKeyDef struct {
	// Columns names the referencing side, in order.
	Columns []string `json:"columns"`
	// ReferencesTable is the table referred to.
	ReferencesTable string `json:"references_table"`
	// ReferencesColumns names the referenced side, in the same order and of the same width.
	ReferencesColumns []string `json:"references_columns"`
	// OnDelete is what happens to a referencing row when its referent is deleted.
	OnDelete ReferentialAction `json:"on_delete,omitempty"`
	// OnUpdate is what happens to a referencing row when its referent's key changes.
	OnUpdate ReferentialAction `json:"on_update,omitempty"`
}

// check reports whether both sides of the recorded key are named and equally wide.
func (d *ForeignKeyDef) check() error {
	if d == nil {
		return nil
	}

	if err := requireColumns("from.columns", d.Columns); err != nil {
		return err
	}

	if err := requireName("from.references_table", d.ReferencesTable); err != nil {
		return err
	}

	if err := requireColumns("from.references_columns", d.ReferencesColumns); err != nil {
		return err
	}

	if len(d.Columns) != len(d.ReferencesColumns) {
		return fmt.Errorf("from.columns has %d entries but from.references_columns has %d",
			len(d.Columns), len(d.ReferencesColumns))
	}

	if err := checkAction("from.on_delete", d.OnDelete); err != nil {
		return err
	}

	return checkAction("from.on_update", d.OnUpdate)
}

// CheckDef is the expression a dropped check constraint carried.
type CheckDef struct {
	// Expression is the constraint's own SQL, which is engine dialect and does not port.
	Expression string `json:"expression"`
}

// check reports a recorded expression that is absent.
func (d *CheckDef) check() error {
	if d == nil {
		return nil
	}

	return requireName("from.expression", d.Expression)
}

// ViewDef is the query a view was defined by before it was dropped or replaced.
type ViewDef struct {
	// Query is the view's own SQL, which is engine dialect and does not port.
	Query string `json:"query"`
}

// check reports a recorded query that is absent.
func (d *ViewDef) check() error {
	if d == nil {
		return nil
	}

	return requireName("from.query", d.Query)
}

// ColumnState is what a column looked like before an alter_column changed it.
type ColumnState struct {
	// Type is the prior logical type, absent when the alteration left the type alone.
	Type *Type `json:"type,omitempty"`
	// Nullable is the prior nullability, absent when the alteration left it alone.
	Nullable *bool `json:"nullable,omitempty"`
	// Default is the prior default, absent when the alteration left it alone.
	Default *Default `json:"default,omitempty"`
	// NoDefault records that the column had no default, which is what inverts a set.
	NoDefault bool `json:"no_default,omitempty"`
}

// check reports a recorded prior state that could not be restored.
func (s *ColumnState) check() error {
	if s == nil {
		return nil
	}

	if s.Type != nil {
		if err := s.Type.Err(); err != nil {
			return fmt.Errorf("from.type: %w", err)
		}
	}

	if s.Default != nil && s.NoDefault {
		return errors.New("from.default and from.no_default cannot both be set")
	}

	if err := s.Default.check(); err != nil {
		return fmt.Errorf("from: %w", err)
	}

	return nil
}

// Empty reports whether the recorded state describes nothing to restore.
func (s *ColumnState) Empty() bool {
	if s == nil {
		return true
	}

	return s.Type == nil && s.Nullable == nil && s.Default == nil && !s.NoDefault
}

// checkKeyColumns reports whether every key column was declared by the table it keys.
func checkKeyColumns(field string, key []string, columns map[string]bool) error {
	for i, name := range key {
		if !columns[name] {
			return fmt.Errorf("%s[%d]: %q is not a column of this table", field, i, name)
		}
	}

	return nil
}
