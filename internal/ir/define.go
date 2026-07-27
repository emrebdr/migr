package ir

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/schema"
)

// TableDef is the definition a dropped table had, enough to recreate it.
type TableDef struct {
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// Validate reports whether the definition can rebuild a table.
func (d *TableDef) Validate() error {
	if d == nil {
		return nil
	}

	if len(d.Columns) == 0 {
		return errors.New("from.columns must list at least one column")
	}

	seen := make(map[string]bool, len(d.Columns))

	for i, c := range d.Columns {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("from.columns[%d]: %w", i, err)
		}

		seen[c.Name] = true
	}

	for i, name := range d.PrimaryKey {
		if !seen[name] {
			return fmt.Errorf("from.primary_key[%d]: %q is not one of from.columns", i, name)
		}
	}

	return nil
}

// ColumnsDef is a bare column list: the definition of an index, unique, or primary key.
type ColumnsDef struct {
	Columns []string `json:"columns"`
}

// Validate reports whether the column list is present and fully named.
func (d *ColumnsDef) Validate() error {
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
	Columns           []string          `json:"columns"`
	ReferencesTable   string            `json:"references_table"`
	ReferencesColumns []string          `json:"references_columns"`
	OnDelete          ReferentialAction `json:"on_delete,omitempty"`
	OnUpdate          ReferentialAction `json:"on_update,omitempty"`
}

// Validate reports whether both sides of the recorded key are named and equally wide.
func (d *ForeignKeyDef) Validate() error {
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

	if err := validateAction("from.on_delete", d.OnDelete); err != nil {
		return err
	}

	return validateAction("from.on_update", d.OnUpdate)
}

// CheckDef is the expression a dropped check constraint carried.
type CheckDef struct {
	Expression string `json:"expression"`
}

// Validate reports whether the recorded expression is present.
func (d *CheckDef) Validate() error {
	if d == nil {
		return nil
	}

	return requireName("from.expression", d.Expression)
}

// ViewDef is the query a view was defined by before it was dropped or replaced.
type ViewDef struct {
	Query string `json:"query"`
}

// Validate reports whether the recorded query is present.
func (d *ViewDef) Validate() error {
	if d == nil {
		return nil
	}

	return requireName("from.query", d.Query)
}

// ColumnState is what a column looked like before an alter_column changed it.
type ColumnState struct {
	Type     *schema.Type `json:"type,omitempty"`
	Nullable *bool        `json:"nullable,omitempty"`
	Default  *Default     `json:"default,omitempty"`
}

// Validate reports whether the recorded state is usable.
func (s *ColumnState) Validate() error {
	if s == nil {
		return nil
	}

	if s.Type != nil && s.Type.Zero() {
		return errors.New("from.type is present but empty")
	}

	if err := s.Default.Validate(); err != nil {
		return fmt.Errorf("from: %w", err)
	}

	return nil
}

// Empty reports whether the recorded state describes nothing to restore.
func (s *ColumnState) Empty() bool {
	return s == nil || (s.Type == nil && s.Nullable == nil && s.Default == nil)
}

// requireDistinctIn reports whether a named column list mentions any column twice.
func requireDistinctIn(field string, columns []string) error {
	seen := make(map[string]bool, len(columns))

	for _, c := range columns {
		if seen[c] {
			return fmt.Errorf("%s lists %q more than once", field, c)
		}

		seen[c] = true
	}

	return nil
}
