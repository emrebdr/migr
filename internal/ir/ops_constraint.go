package ir

import "fmt"

// AddIndex creates a non-unique index. A unique constraint is AddUnique.
type AddIndex struct {
	Table   string   `json:"table"`
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// Kind returns the operation's discriminator.
func (o AddIndex) Kind() Kind { return KindAddIndex }

// Destructive reports whether the operation can lose data.
func (o AddIndex) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o AddIndex) Reverse() (Operation, error) {
	return DropIndex{Table: o.Table, Name: o.Name}, nil
}

func (o AddIndex) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AddIndex) MarshalJSON() ([]byte, error) {
	type alias AddIndex

	return marshalOp(KindAddIndex, alias(o))
}

// Validate reports whether the operation names a table, an index, and its columns.
func (o AddIndex) Validate() error {
	return validateNamedColumns(o.Table, o.Name, o.Columns)
}

// DropIndex removes an index, which rebuilds from the table without data loss.
type DropIndex struct {
	Table string      `json:"table"`
	Name  string      `json:"name"`
	From  *ColumnsDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropIndex) Kind() Kind { return KindDropIndex }

// Destructive reports whether the operation can lose data.
func (o DropIndex) Destructive() bool { return false }

// Reverse recreates the index when its prior definition was recorded.
func (o DropIndex) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropIndex, "index definition")
	}

	return AddIndex{Table: o.Table, Name: o.Name, Columns: o.From.Columns}, nil
}

func (o DropIndex) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropIndex) MarshalJSON() ([]byte, error) {
	type alias DropIndex

	return marshalOp(KindDropIndex, alias(o))
}

// Validate reports whether the operation names a table and an index.
func (o DropIndex) Validate() error {
	if err := validateNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.Validate()
}

// AddForeignKey adds a foreign key from one table's columns to another's.
type AddForeignKey struct {
	Table             string            `json:"table"`
	Name              string            `json:"name"`
	Columns           []string          `json:"columns"`
	ReferencesTable   string            `json:"references_table"`
	ReferencesColumns []string          `json:"references_columns"`
	OnDelete          ReferentialAction `json:"on_delete,omitempty"`
	OnUpdate          ReferentialAction `json:"on_update,omitempty"`
}

// Kind returns the operation's discriminator.
func (o AddForeignKey) Kind() Kind { return KindAddForeignKey }

// Destructive reports whether the operation can lose data.
func (o AddForeignKey) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o AddForeignKey) Reverse() (Operation, error) {
	return DropForeignKey{Table: o.Table, Name: o.Name}, nil
}

func (o AddForeignKey) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AddForeignKey) MarshalJSON() ([]byte, error) {
	type alias AddForeignKey

	return marshalOp(KindAddForeignKey, alias(o))
}

// Validate reports whether both sides of the key are named and equally wide.
func (o AddForeignKey) Validate() error {
	if err := validateNamedColumns(o.Table, o.Name, o.Columns); err != nil {
		return err
	}

	if err := requireName("references_table", o.ReferencesTable); err != nil {
		return err
	}

	if err := requireColumns("references_columns", o.ReferencesColumns); err != nil {
		return err
	}

	if len(o.Columns) != len(o.ReferencesColumns) {
		return fmt.Errorf("columns has %d entries but references_columns has %d",
			len(o.Columns), len(o.ReferencesColumns))
	}

	if err := validateAction("on_delete", o.OnDelete); err != nil {
		return err
	}

	return validateAction("on_update", o.OnUpdate)
}

// DropForeignKey removes a foreign key, leaving both tables' rows intact.
type DropForeignKey struct {
	Table string         `json:"table"`
	Name  string         `json:"name"`
	From  *ForeignKeyDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropForeignKey) Kind() Kind { return KindDropForeignKey }

// Destructive reports whether the operation can lose data.
func (o DropForeignKey) Destructive() bool { return false }

// Reverse recreates the foreign key when its prior definition was recorded.
func (o DropForeignKey) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropForeignKey, "foreign key definition")
	}

	return AddForeignKey{
		Table:             o.Table,
		Name:              o.Name,
		Columns:           o.From.Columns,
		ReferencesTable:   o.From.ReferencesTable,
		ReferencesColumns: o.From.ReferencesColumns,
		OnDelete:          o.From.OnDelete,
		OnUpdate:          o.From.OnUpdate,
	}, nil
}

func (o DropForeignKey) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropForeignKey) MarshalJSON() ([]byte, error) {
	type alias DropForeignKey

	return marshalOp(KindDropForeignKey, alias(o))
}

// Validate reports whether the operation names a table and a key.
func (o DropForeignKey) Validate() error {
	if err := validateNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.Validate()
}

// AddUnique adds a unique constraint over one or more columns.
type AddUnique struct {
	Table   string   `json:"table"`
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// Kind returns the operation's discriminator.
func (o AddUnique) Kind() Kind { return KindAddUnique }

// Destructive reports whether the operation can lose data.
func (o AddUnique) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o AddUnique) Reverse() (Operation, error) {
	return DropUnique{Table: o.Table, Name: o.Name}, nil
}

func (o AddUnique) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AddUnique) MarshalJSON() ([]byte, error) {
	type alias AddUnique

	return marshalOp(KindAddUnique, alias(o))
}

// Validate reports whether the operation names a table, a constraint, and its columns.
func (o AddUnique) Validate() error {
	return validateNamedColumns(o.Table, o.Name, o.Columns)
}

// DropUnique removes a unique constraint, leaving the rows intact.
type DropUnique struct {
	Table string      `json:"table"`
	Name  string      `json:"name"`
	From  *ColumnsDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropUnique) Kind() Kind { return KindDropUnique }

// Destructive reports whether the operation can lose data.
func (o DropUnique) Destructive() bool { return false }

// Reverse recreates the constraint when its prior definition was recorded.
func (o DropUnique) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropUnique, "unique constraint definition")
	}

	return AddUnique{Table: o.Table, Name: o.Name, Columns: o.From.Columns}, nil
}

func (o DropUnique) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropUnique) MarshalJSON() ([]byte, error) {
	type alias DropUnique

	return marshalOp(KindDropUnique, alias(o))
}

// Validate reports whether the operation names a table and a constraint.
func (o DropUnique) Validate() error {
	if err := validateNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.Validate()
}

// AddCheck adds a check constraint whose expression is engine SQL and does not port.
type AddCheck struct {
	Table      string `json:"table"`
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

// Kind returns the operation's discriminator.
func (o AddCheck) Kind() Kind { return KindAddCheck }

// Destructive reports whether the operation can lose data.
func (o AddCheck) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o AddCheck) Reverse() (Operation, error) {
	return DropCheck{Table: o.Table, Name: o.Name}, nil
}

func (o AddCheck) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AddCheck) MarshalJSON() ([]byte, error) {
	type alias AddCheck

	return marshalOp(KindAddCheck, alias(o))
}

// Validate reports whether the operation names a constraint and carries an expression.
func (o AddCheck) Validate() error {
	if err := validateNamed(o.Table, o.Name); err != nil {
		return err
	}

	return requireName("expression", o.Expression)
}

// DropCheck removes a check constraint, leaving the rows intact.
type DropCheck struct {
	Table string    `json:"table"`
	Name  string    `json:"name"`
	From  *CheckDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropCheck) Kind() Kind { return KindDropCheck }

// Destructive reports whether the operation can lose data.
func (o DropCheck) Destructive() bool { return false }

// Reverse recreates the constraint when its prior expression was recorded.
func (o DropCheck) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropCheck, "check expression")
	}

	return AddCheck{Table: o.Table, Name: o.Name, Expression: o.From.Expression}, nil
}

func (o DropCheck) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropCheck) MarshalJSON() ([]byte, error) {
	type alias DropCheck

	return marshalOp(KindDropCheck, alias(o))
}

// Validate reports whether the operation names a table and a constraint.
func (o DropCheck) Validate() error {
	if err := validateNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.Validate()
}

// AddPrimaryKey adds a primary key to a table that has none.
type AddPrimaryKey struct {
	Table   string   `json:"table"`
	Name    string   `json:"name,omitempty"`
	Columns []string `json:"columns"`
}

// Kind returns the operation's discriminator.
func (o AddPrimaryKey) Kind() Kind { return KindAddPrimaryKey }

// Destructive reports whether the operation can lose data.
func (o AddPrimaryKey) Destructive() bool { return false }

// Reverse returns the operation that undoes this one.
func (o AddPrimaryKey) Reverse() (Operation, error) {
	return DropPrimaryKey{Table: o.Table, Name: o.Name}, nil
}

func (o AddPrimaryKey) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o AddPrimaryKey) MarshalJSON() ([]byte, error) {
	type alias AddPrimaryKey

	return marshalOp(KindAddPrimaryKey, alias(o))
}

// Validate reports whether the operation names a table and its key columns.
func (o AddPrimaryKey) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return requireColumns("columns", o.Columns)
}

// DropPrimaryKey removes a table's primary key. The name is optional: MySQL has none.
type DropPrimaryKey struct {
	Table string      `json:"table"`
	Name  string      `json:"name,omitempty"`
	From  *ColumnsDef `json:"from,omitempty"`
}

// Kind returns the operation's discriminator.
func (o DropPrimaryKey) Kind() Kind { return KindDropPrimaryKey }

// Destructive reports whether the operation can lose data.
func (o DropPrimaryKey) Destructive() bool { return false }

// Reverse recreates the key when its prior definition was recorded.
func (o DropPrimaryKey) Reverse() (Operation, error) {
	if o.From == nil {
		return nil, missingFrom(KindDropPrimaryKey, "primary key definition")
	}

	return AddPrimaryKey{Table: o.Table, Name: o.Name, Columns: o.From.Columns}, nil
}

func (o DropPrimaryKey) isOperation() {}

// MarshalJSON renders the operation with its discriminator first.
func (o DropPrimaryKey) MarshalJSON() ([]byte, error) {
	type alias DropPrimaryKey

	return marshalOp(KindDropPrimaryKey, alias(o))
}

// Validate reports whether the operation names a table.
func (o DropPrimaryKey) Validate() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return o.From.Validate()
}

// validateNamed reports whether a table and an object name are both present.
func validateNamed(table, name string) error {
	if err := requireName("table", table); err != nil {
		return err
	}

	return requireName("name", name)
}

// validateNamedColumns reports whether a named object over columns is fully specified.
func validateNamedColumns(table, name string, columns []string) error {
	if err := validateNamed(table, name); err != nil {
		return err
	}

	if err := requireColumns("columns", columns); err != nil {
		return err
	}

	return requireDistinctIn("columns", columns)
}

// compile-time proof that every constraint operation satisfies the sealed interface.
var (
	_ Operation = AddIndex{}
	_ Operation = DropIndex{}
	_ Operation = AddForeignKey{}
	_ Operation = DropForeignKey{}
	_ Operation = AddUnique{}
	_ Operation = DropUnique{}
	_ Operation = AddCheck{}
	_ Operation = DropCheck{}
	_ Operation = AddPrimaryKey{}
	_ Operation = DropPrimaryKey{}
)
