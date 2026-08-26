package migr

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CreateTable creates a table with its columns and, optionally, its primary key.
type CreateTable struct {
	// Table is the name of the table to create.
	Table string `json:"table"`
	// Columns is every column the table starts with, in order. At least one, all distinct.
	Columns []Column `json:"columns"`
	// PrimaryKey names the key columns inline, which is how a new table states its key.
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// Kind names this operation, which is create_table.
func (o CreateTable) Kind() Kind { return KindCreateTable }

// MarshalJSON renders the operation with its discriminator first.
func (o CreateTable) MarshalJSON() ([]byte, error) {
	type alias CreateTable

	return marshalOp(KindCreateTable, alias(o))
}

// check reports a table created with no name, no columns, or a key naming something else.
func (o CreateTable) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if len(o.Columns) == 0 {
		return errors.New("columns must list at least one column")
	}

	seen := make(map[string]bool, len(o.Columns))

	for i, c := range o.Columns {
		if err := c.check(); err != nil {
			return fmt.Errorf("columns[%d]: %w", i, err)
		}

		if seen[c.Name] {
			return fmt.Errorf("column %q is declared more than once", c.Name)
		}

		seen[c.Name] = true
	}

	return checkKeyColumns("primary_key", o.PrimaryKey, seen)
}

// DropTable removes a table and everything in it.
type DropTable struct {
	// Table is the name of the table to remove.
	Table string `json:"table"`
	// From records what the table held, without which the drop cannot be undone.
	From *TableDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_table.
func (o DropTable) Kind() Kind { return KindDropTable }

// MarshalJSON renders the operation with its discriminator first.
func (o DropTable) MarshalJSON() ([]byte, error) {
	type alias DropTable

	return marshalOp(KindDropTable, alias(o))
}

// check reports a drop that names no table.
func (o DropTable) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return o.From.check()
}

// RenameTable renames a table, preserving its contents.
type RenameTable struct {
	// From is the table's current name.
	From string `json:"from"`
	// To is the name it takes.
	To string `json:"to"`
}

// Kind names this operation, which is rename_table.
func (o RenameTable) Kind() Kind { return KindRenameTable }

// MarshalJSON renders the operation with its discriminator first.
func (o RenameTable) MarshalJSON() ([]byte, error) {
	type alias RenameTable

	return marshalOp(KindRenameTable, alias(o))
}

// check reports a rename that names no table, or renames one to itself.
func (o RenameTable) check() error {
	return requireRename("table", o.From, o.To)
}

// AddColumn adds one column to an existing table.
type AddColumn struct {
	// Table is the table the column joins.
	Table string `json:"table"`
	// Column is the column itself, with its logical type and nullability.
	Column Column `json:"column"`
}

// Kind names this operation, which is add_column.
func (o AddColumn) Kind() Kind { return KindAddColumn }

// MarshalJSON renders the operation with its discriminator first.
func (o AddColumn) MarshalJSON() ([]byte, error) {
	type alias AddColumn

	return marshalOp(KindAddColumn, alias(o))
}

// check reports an addition that names no table, or carries a column that is not one.
func (o AddColumn) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return o.Column.check()
}

// DropColumn removes a column and the data it holds.
type DropColumn struct {
	// Table is the table the column leaves.
	Table string `json:"table"`
	// Column is the name of the column to remove.
	Column string `json:"column"`
	// From records what the column was, without which the drop cannot be undone.
	From *Column `json:"from,omitempty"`
}

// Kind names this operation, which is drop_column.
func (o DropColumn) Kind() Kind { return KindDropColumn }

// MarshalJSON renders the operation with its discriminator first.
func (o DropColumn) MarshalJSON() ([]byte, error) {
	type alias DropColumn

	return marshalOp(KindDropColumn, alias(o))
}

// check reports a drop naming no column, or recording one that is not the column being dropped.
func (o DropColumn) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if err := requireName("column", o.Column); err != nil {
		return err
	}

	if o.From == nil {
		return nil
	}

	if err := o.From.check(); err != nil {
		return err
	}

	return o.requireRecordedNameMatches()
}

// requireRecordedNameMatches reports whether the recorded column is the one being dropped.
func (o DropColumn) requireRecordedNameMatches() error {
	if o.From.Name == o.Column {
		return nil
	}

	return fmt.Errorf("from.name %q does not name the column being dropped (%q)",
		o.From.Name, o.Column)
}

// RenameColumn renames a column, preserving its data.
type RenameColumn struct {
	// Table is the table the column belongs to.
	Table string `json:"table"`
	// From is the column's current name.
	From string `json:"from"`
	// To is the name it takes.
	To string `json:"to"`
}

// Kind names this operation, which is rename_column.
func (o RenameColumn) Kind() Kind { return KindRenameColumn }

// MarshalJSON renders the operation with its discriminator first.
func (o RenameColumn) MarshalJSON() ([]byte, error) {
	type alias RenameColumn

	return marshalOp(KindRenameColumn, alias(o))
}

// check reports a rename that names no table or column, or renames one to itself.
func (o RenameColumn) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return requireRename("column", o.From, o.To)
}

// AlterColumn changes a column's type, nullability, or default. At least one change is required.
type AlterColumn struct {
	// Table is the table the column belongs to.
	Table string `json:"table"`
	// Column is the name of the column to change.
	Column string `json:"column"`
	// Type is the new logical type, absent to leave the type alone.
	Type *Type `json:"type,omitempty"`
	// Nullable is the new nullability, absent to leave nullability alone.
	Nullable *bool `json:"nullable,omitempty"`
	// Default sets a default, and cannot appear beside DropDefault.
	Default *Default `json:"default,omitempty"`
	// DropDefault removes the existing default, which absence of Default would not.
	DropDefault bool `json:"drop_default,omitempty"`
	// From records the prior state, without which the change cannot be undone.
	From *ColumnState `json:"from,omitempty"`
}

// Kind names this operation, which is alter_column.
func (o AlterColumn) Kind() Kind { return KindAlterColumn }

// MarshalJSON renders the operation with its discriminator first.
func (o AlterColumn) MarshalJSON() ([]byte, error) {
	type alias AlterColumn

	return marshalOp(KindAlterColumn, alias(o))
}

// check reports an alteration that names no column, asks for nothing, or asks for two defaults.
func (o AlterColumn) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if err := requireName("column", o.Column); err != nil {
		return err
	}

	if err := o.checkNewState(); err != nil {
		return err
	}

	if err := o.From.check(); err != nil {
		return err
	}

	return o.requireChange()
}

// checkNewState reports a new state that is unusable, or that sets a default while dropping one.
func (o AlterColumn) checkNewState() error {
	if o.Type != nil {
		if err := o.Type.Err(); err != nil {
			return fmt.Errorf("type: %w", err)
		}
	}

	if o.Default != nil && o.DropDefault {
		return errors.New("default and drop_default cannot both be set")
	}

	return o.Default.check()
}

// requireChange reports whether the operation asks for at least one alteration.
func (o AlterColumn) requireChange() error {
	if o.Type == nil && o.Nullable == nil && o.Default == nil && !o.DropDefault {
		return errors.New("requests no change: set at least one of type, nullable, default, drop_default")
	}

	return nil
}

// compile-time proof that every table operation satisfies the sealed interface.
var (
	_ Operation      = CreateTable{}
	_ Operation      = DropTable{}
	_ Operation      = RenameTable{}
	_ Operation      = AddColumn{}
	_ Operation      = DropColumn{}
	_ Operation      = RenameColumn{}
	_ Operation      = AlterColumn{}
	_ json.Marshaler = CreateTable{}
)
