package migr

import "fmt"

// AddIndex creates a non-unique index. A unique constraint is AddUnique.
type AddIndex struct {
	// Table is the table the index covers.
	Table string `json:"table"`
	// Name is the index's own name.
	Name string `json:"name"`
	// Columns names the indexed columns, in order. At least one, all distinct.
	Columns []string `json:"columns"`
}

// Kind names this operation, which is add_index.
func (o AddIndex) Kind() Kind { return KindAddIndex }

// MarshalJSON renders the operation with its discriminator first.
func (o AddIndex) MarshalJSON() ([]byte, error) {
	type alias AddIndex

	return marshalOp(KindAddIndex, alias(o))
}

// check reports an index that names no table, no index, or no columns.
func (o AddIndex) check() error {
	return requireNamedColumns(o.Table, o.Name, o.Columns)
}

// DropIndex removes an index, which rebuilds from the table without data loss.
type DropIndex struct {
	// Table is the table the index covered.
	Table string `json:"table"`
	// Name is the index's own name.
	Name string `json:"name"`
	// From records which columns it covered, without which the drop cannot be undone.
	From *ColumnsDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_index.
func (o DropIndex) Kind() Kind { return KindDropIndex }

// MarshalJSON renders the operation with its discriminator first.
func (o DropIndex) MarshalJSON() ([]byte, error) {
	type alias DropIndex

	return marshalOp(KindDropIndex, alias(o))
}

// check reports a drop that names no table or no index.
func (o DropIndex) check() error {
	if err := requireNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.check()
}

// AddForeignKey adds a foreign key from one table's columns to another's.
type AddForeignKey struct {
	// Table is the referencing table.
	Table string `json:"table"`
	// Name is the constraint's own name.
	Name string `json:"name"`
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

// Kind names this operation, which is add_foreign_key.
func (o AddForeignKey) Kind() Kind { return KindAddForeignKey }

// MarshalJSON renders the operation with its discriminator first.
func (o AddForeignKey) MarshalJSON() ([]byte, error) {
	type alias AddForeignKey

	return marshalOp(KindAddForeignKey, alias(o))
}

// check reports a key whose two sides are unnamed, unequal in width, or oddly actioned.
func (o AddForeignKey) check() error {
	if err := requireNamedColumns(o.Table, o.Name, o.Columns); err != nil {
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

	if err := checkAction("on_delete", o.OnDelete); err != nil {
		return err
	}

	return checkAction("on_update", o.OnUpdate)
}

// DropForeignKey removes a foreign key, leaving both tables' rows intact.
type DropForeignKey struct {
	// Table is the referencing table.
	Table string `json:"table"`
	// Name is the constraint's own name.
	Name string `json:"name"`
	// From records what the key referred to, without which the drop cannot be undone.
	From *ForeignKeyDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_foreign_key.
func (o DropForeignKey) Kind() Kind { return KindDropForeignKey }

// MarshalJSON renders the operation with its discriminator first.
func (o DropForeignKey) MarshalJSON() ([]byte, error) {
	type alias DropForeignKey

	return marshalOp(KindDropForeignKey, alias(o))
}

// check reports a drop that names no table or no constraint.
func (o DropForeignKey) check() error {
	if err := requireNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.check()
}

// AddUnique adds a unique constraint over one or more columns.
type AddUnique struct {
	// Table is the table the constraint covers.
	Table string `json:"table"`
	// Name is the constraint's own name.
	Name string `json:"name"`
	// Columns names the constrained columns, in order. At least one, all distinct.
	Columns []string `json:"columns"`
}

// Kind names this operation, which is add_unique.
func (o AddUnique) Kind() Kind { return KindAddUnique }

// MarshalJSON renders the operation with its discriminator first.
func (o AddUnique) MarshalJSON() ([]byte, error) {
	type alias AddUnique

	return marshalOp(KindAddUnique, alias(o))
}

// check reports a constraint that names no table, no constraint, or no columns.
func (o AddUnique) check() error {
	return requireNamedColumns(o.Table, o.Name, o.Columns)
}

// DropUnique removes a unique constraint, leaving the rows intact.
type DropUnique struct {
	// Table is the table the constraint covered.
	Table string `json:"table"`
	// Name is the constraint's own name.
	Name string `json:"name"`
	// From records which columns it covered, without which the drop cannot be undone.
	From *ColumnsDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_unique.
func (o DropUnique) Kind() Kind { return KindDropUnique }

// MarshalJSON renders the operation with its discriminator first.
func (o DropUnique) MarshalJSON() ([]byte, error) {
	type alias DropUnique

	return marshalOp(KindDropUnique, alias(o))
}

// check reports a drop that names no table or no constraint.
func (o DropUnique) check() error {
	if err := requireNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.check()
}

// AddCheck adds a check constraint whose expression is engine SQL and does not port.
type AddCheck struct {
	// Table is the table the constraint covers.
	Table string `json:"table"`
	// Name is the constraint's own name.
	Name string `json:"name"`
	// Expression is the constraint's own SQL, in the target engine's dialect.
	Expression string `json:"expression"`
}

// Kind names this operation, which is add_check.
func (o AddCheck) Kind() Kind { return KindAddCheck }

// MarshalJSON renders the operation with its discriminator first.
func (o AddCheck) MarshalJSON() ([]byte, error) {
	type alias AddCheck

	return marshalOp(KindAddCheck, alias(o))
}

// check reports a constraint that names nothing, or carries no expression.
func (o AddCheck) check() error {
	if err := requireNamed(o.Table, o.Name); err != nil {
		return err
	}

	return requireName("expression", o.Expression)
}

// DropCheck removes a check constraint, leaving the rows intact.
type DropCheck struct {
	// Table is the table the constraint covered.
	Table string `json:"table"`
	// Name is the constraint's own name.
	Name string `json:"name"`
	// From records the expression it carried, without which the drop cannot be undone.
	From *CheckDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_check.
func (o DropCheck) Kind() Kind { return KindDropCheck }

// MarshalJSON renders the operation with its discriminator first.
func (o DropCheck) MarshalJSON() ([]byte, error) {
	type alias DropCheck

	return marshalOp(KindDropCheck, alias(o))
}

// check reports a drop that names no table or no constraint.
func (o DropCheck) check() error {
	if err := requireNamed(o.Table, o.Name); err != nil {
		return err
	}

	return o.From.check()
}

// AddPrimaryKey adds a primary key to a table that has none.
type AddPrimaryKey struct {
	// Table is the table the key covers.
	Table string `json:"table"`
	// Name is the constraint's own name, omitted where the engine names it for you.
	Name string `json:"name,omitempty"`
	// Columns names the key columns, in order. At least one, all distinct.
	Columns []string `json:"columns"`
}

// Kind names this operation, which is add_primary_key.
func (o AddPrimaryKey) Kind() Kind { return KindAddPrimaryKey }

// MarshalJSON renders the operation with its discriminator first.
func (o AddPrimaryKey) MarshalJSON() ([]byte, error) {
	type alias AddPrimaryKey

	return marshalOp(KindAddPrimaryKey, alias(o))
}

// check reports a key that names no table or no columns.
func (o AddPrimaryKey) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	if err := requireColumns("columns", o.Columns); err != nil {
		return err
	}

	return requireDistinctIn("columns", o.Columns)
}

// DropPrimaryKey removes a table's primary key. The name is optional: MySQL has none.
type DropPrimaryKey struct {
	// Table is the table the key covered.
	Table string `json:"table"`
	// Name is the constraint's own name, omitted where the engine has none.
	Name string `json:"name,omitempty"`
	// From records which columns it covered, without which the drop cannot be undone.
	From *ColumnsDef `json:"from,omitempty"`
}

// Kind names this operation, which is drop_primary_key.
func (o DropPrimaryKey) Kind() Kind { return KindDropPrimaryKey }

// MarshalJSON renders the operation with its discriminator first.
func (o DropPrimaryKey) MarshalJSON() ([]byte, error) {
	type alias DropPrimaryKey

	return marshalOp(KindDropPrimaryKey, alias(o))
}

// check reports a drop that names no table.
func (o DropPrimaryKey) check() error {
	if err := requireName("table", o.Table); err != nil {
		return err
	}

	return o.From.check()
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
