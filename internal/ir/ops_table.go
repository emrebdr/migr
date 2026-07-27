package ir

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/schema"
)

// CreateTable creates a table with its columns and, optionally, its primary key.
type CreateTable struct {
	Table      string   `json:"table"`
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// Kind returns the operation's discriminator.
func (o CreateTable) Kind() Kind { return KindCreateTable }

// Destructive reports whether the operation can lose data.
func (o CreateTable) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o CreateTable) Reverse() (Operation, error) {
	return DropTable{Table: o.Table}, nil
}

func (o CreateTable) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o CreateTable) MarshalJSON() ([]byte, error) {
	type alias CreateTable

	return marshalOp(KindCreateTable, alias(o))
}

// Validate reports whether the operation names a table and at least one column.
func (o CreateTable) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if len(o.Columns) == 0 {
		return errors.New("columns must list at least one column")
	}

	seen := make(map[string]bool, len(o.Columns))

	for i, c := range o.Columns {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("columns[%d]: %w", i, err)
		}

		if seen[c.Name] {
			return fmt.Errorf("column %q is declared more than once", c.Name)
		}

		seen[c.Name] = true
	}

	return validatePrimaryKey(o.PrimaryKey, seen)
}

// validatePrimaryKey reports whether every key column was declared by the table.
func validatePrimaryKey(key []string, columns map[string]bool) error {
	for i, name := range key {
		if !columns[name] {
			return fmt.Errorf("primary_key[%d]: %q is not a column of this table", i, name)
		}
	}

	return nil
}

// DropTable removes a table and everything in it.
type DropTable struct {
	Table string    `json:"table"`
	From  *TableDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropTable) Kind() Kind { return KindDropTable }

// Destructive reports whether the operation can lose data.
func (o DropTable) Destructive() bool { return true }

// Reverse recreates the table when its prior definition was recorded.
func (o DropTable) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropTable, "table definition")
	}

	return CreateTable{
		Table:      o.Table,
		Columns:    o.From.Columns,
		PrimaryKey: o.From.PrimaryKey,
	}, nil
}

func (o DropTable) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropTable) MarshalJSON() ([]byte, error) {
	type alias DropTable

	return marshalOp(KindDropTable, alias(o))
}

// Validate reports whether the operation names a table.
func (o DropTable) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return o.From.Validate()
}

// RenameTable renames a table, preserving its contents.
type RenameTable struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Kind returns the operation's discriminator.
func (o RenameTable) Kind() Kind { return KindRenameTable }

// Destructive reports whether the operation can lose data.
func (o RenameTable) Destructive() bool { return false }

// Reverse renames the table back, which makes this operation its own inverse.
func (o RenameTable) Reverse() (Operation, error) {
	return RenameTable{From: o.To, To: o.From}, nil
}

func (o RenameTable) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o RenameTable) MarshalJSON() ([]byte, error) {
	type alias RenameTable

	return marshalOp(KindRenameTable, alias(o))
}

// Validate reports whether the operation names distinct source and target tables.
func (o RenameTable) Validate() error {
	return requireRename("table", o.From, o.To)
}

// requireRename reports whether a rename names two distinct, non-empty identifiers.
func requireRename(subject, from, to string) error {
	if err := requireName("from", from); err != nil {
		return err
	}

	if err := requireName("to", to); err != nil {
		return err
	}

	if from == to {
		return fmt.Errorf("%s rename has the same from and to (%q)", subject, from)
	}

	return nil
}

// AddColumn adds one column to an existing table.
type AddColumn struct {
	Table  string `json:"table"`
	Column Column `json:"column"`
}

// Kind returns the operation's discriminator.
func (o AddColumn) Kind() Kind { return KindAddColumn }

// Destructive reports whether the operation can lose data.
func (o AddColumn) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o AddColumn) Reverse() (Operation, error) {
	return DropColumn{Table: o.Table, Column: o.Column.Name}, nil
}

func (o AddColumn) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AddColumn) MarshalJSON() ([]byte, error) {
	type alias AddColumn

	return marshalOp(KindAddColumn, alias(o))
}

// Validate reports whether the operation names a table and a well-formed column.
func (o AddColumn) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return o.Column.Validate()
}

// DropColumn removes a column and the data it holds.
type DropColumn struct {
	Table  string  `json:"table"`
	Column string  `json:"column"`
	From   *Column `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropColumn) Kind() Kind { return KindDropColumn }

// Destructive reports whether the operation can lose data.
func (o DropColumn) Destructive() bool { return true }

// Reverse re-adds the column when its prior definition was recorded.
func (o DropColumn) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropColumn, "column definition")
	}

	return AddColumn{Table: o.Table, Column: *o.From}, nil
}

func (o DropColumn) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropColumn) MarshalJSON() ([]byte, error) {
	type alias DropColumn

	return marshalOp(KindDropColumn, alias(o))
}

// Validate reports whether the operation names a table and a column.
func (o DropColumn) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if err := requireName("column", o.Column); err != nil {
		return err
	}

	if o.From == nil {
		return nil
	}

	if err := o.From.Validate(); err != nil {
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
	Table string `json:"table"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Kind returns the operation's discriminator.
func (o RenameColumn) Kind() Kind { return KindRenameColumn }

// Destructive reports whether the operation can lose data.
func (o RenameColumn) Destructive() bool { return false }

// Reverse renames the column back, which makes this operation its own inverse.
func (o RenameColumn) Reverse() (Operation, error) {
	return RenameColumn{Table: o.Table, From: o.To, To: o.From}, nil
}

func (o RenameColumn) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o RenameColumn) MarshalJSON() ([]byte, error) {
	type alias RenameColumn

	return marshalOp(KindRenameColumn, alias(o))
}

// Validate reports whether the operation names a table and two distinct columns.
func (o RenameColumn) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return requireRename("column", o.From, o.To)
}

// AlterColumn changes a column's type, nullability, or default.
type AlterColumn struct {
	Table       string       `json:"table"`
	Column      string       `json:"column"`
	Type        *schema.Type `json:"type,omitempty"`
	Nullable    *bool        `json:"nullable,omitempty"`
	Default     *Default     `json:"default,omitempty"`
	DropDefault bool         `json:"drop_default,omitempty"`
	From        *ColumnState `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o AlterColumn) Kind() Kind { return KindAlterColumn }

// Destructive reports true for any retype, since this op cannot tell if one narrows.
func (o AlterColumn) Destructive() bool { return o.Type != nil }

// Reverse restores the recorded prior state of the column.
func (o AlterColumn) Reverse() (Operation, error) {
	if o.From.Empty() {
		return nil, missingFrom(KindAlterColumn, "previous column state")
	}

	return AlterColumn{
		Table:       o.Table,
		Column:      o.Column,
		Type:        o.From.Type,
		Nullable:    o.From.Nullable,
		Default:     o.From.Default,
		DropDefault: o.restoresNoDefault(),
		From:        o.currentState(),
	}, nil
}

// restoresNoDefault reports whether the prior state recorded that there was no default.
func (o AlterColumn) restoresNoDefault() bool {
	return o.From.NoDefault
}

// currentState records what this operation is setting, so its inverse inverts too.
func (o AlterColumn) currentState() *ColumnState {
	state := &ColumnState{
		Type:      o.Type,
		Nullable:  o.Nullable,
		Default:   o.Default,
		NoDefault: o.DropDefault,
	}

	if state.Empty() {
		return nil
	}

	return state
}

func (o AlterColumn) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AlterColumn) MarshalJSON() ([]byte, error) {
	type alias AlterColumn

	return marshalOp(KindAlterColumn, alias(o))
}

// Validate reports whether the operation names a column and requests a change to it.
func (o AlterColumn) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if err := requireName("column", o.Column); err != nil {
		return err
	}

	if o.Type != nil && o.Type.Zero() {
		return errors.New("type is present but empty")
	}

	if o.Default != nil && o.DropDefault {
		return errors.New("default and drop_default cannot both be set")
	}

	if err := o.Default.Validate(); err != nil {
		return err
	}

	if err := o.From.Validate(); err != nil {
		return err
	}

	return o.requireChange()
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
