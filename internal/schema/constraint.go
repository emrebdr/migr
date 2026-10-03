package schema

import (
	"errors"
	"fmt"
	"strings"
)

// PrimaryKey is a table's primary key: the columns, in the order the key declares them.
type PrimaryKey struct {
	// Name is what the engine calls the key, absent where the engine names it nothing.
	Name string `json:"name,omitempty"`
	// Columns are the key's columns, in order, which is part of the key rather than a rendering.
	Columns []string `json:"columns"`
	// Beyond marks a key the v1 operation set cannot express.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Validate reports whether the key names columns it could be built from.
func (k *PrimaryKey) Validate() error {
	if k == nil {
		return nil
	}

	if err := requireColumns("primary key", k.Columns); err != nil {
		return err
	}

	return k.Beyond.Validate()
}

// Equal reports whether two primary keys are the same key, ignoring what the engine calls it.
func (k *PrimaryKey) Equal(other *PrimaryKey) bool {
	if k == nil || other == nil {
		return k == nil && other == nil
	}

	return sameColumns(k.Columns, other.Columns) && k.Beyond.Equal(other.Beyond)
}

// Index is a secondary index over a table's columns, in the order the index declares them.
type Index struct {
	// Name is the index's own name, which every engine records for an index.
	Name string `json:"name"`
	// Columns are the indexed columns, in order, which is part of the index.
	Columns []string `json:"columns"`
	// Unique reports whether the index enforces uniqueness as well as ordering.
	Unique bool `json:"unique,omitempty"`
	// Beyond marks an index the v1 operation set cannot express -- partial, expression, prefixed.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Validate reports whether the index names itself and the columns it covers.
func (i Index) Validate() error {
	if strings.TrimSpace(i.Name) == "" {
		return errors.New("an index has no name")
	}

	if err := requireColumns(fmt.Sprintf("index %q", i.Name), i.Columns); err != nil {
		return err
	}

	return i.Beyond.Validate()
}

// Equal reports whether two indexes are the same index.
func (i Index) Equal(other Index) bool {
	return i.Name == other.Name &&
		sameColumns(i.Columns, other.Columns) &&
		i.Unique == other.Unique &&
		i.Beyond.Equal(other.Beyond)
}

// Unique is a unique constraint over a table's columns, which is not an Index carrying Unique.
type Unique struct {
	// Name is the constraint's own name, which every engine records for one.
	Name string `json:"name"`
	// Columns are the constrained columns, in order.
	Columns []string `json:"columns"`
	// Beyond marks a constraint the v1 operation set cannot express.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Validate reports whether the constraint names itself and the columns it covers.
func (u Unique) Validate() error {
	if strings.TrimSpace(u.Name) == "" {
		return errors.New("a unique constraint has no name")
	}

	if err := requireColumns(fmt.Sprintf("unique constraint %q", u.Name), u.Columns); err != nil {
		return err
	}

	return u.Beyond.Validate()
}

// Equal reports whether two unique constraints are the same constraint.
func (u Unique) Equal(other Unique) bool {
	return u.Name == other.Name &&
		sameColumns(u.Columns, other.Columns) &&
		u.Beyond.Equal(other.Beyond)
}

// Check is a check constraint, whose expression is engine SQL and does not port.
type Check struct {
	// Name is the constraint's own name, absent where the engine holds one with none.
	Name string `json:"name,omitempty"`
	// Expression is the engine's own text for the condition, which does not port between engines.
	Expression string `json:"expression"`
	// Beyond marks a constraint the v1 operation set cannot express.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Validate reports whether the constraint carries the condition it checks.
func (c Check) Validate() error {
	if strings.TrimSpace(c.Expression) == "" {
		return fmt.Errorf("check constraint %q carries no expression", c.Name)
	}

	return c.Beyond.Validate()
}

// Equal reports whether two check constraints are the same constraint.
func (c Check) Equal(other Check) bool {
	return c.Name == other.Name &&
		c.Expression == other.Expression &&
		c.Beyond.Equal(other.Beyond)
}

// ForeignKey is a reference from one table's columns to another's, identified by its shape.
type ForeignKey struct {
	// Name is what the engine calls the key, absent where the engine reports none.
	Name string `json:"name,omitempty"`
	// Columns are the referencing columns, in order, which pairs them with the referenced ones.
	Columns []string `json:"columns"`
	// ReferencesTable is the table referred to, which may be one this snapshot does not hold.
	ReferencesTable string `json:"references_table"`
	// ReferencesColumns are the referenced columns, in the order that pairs them.
	ReferencesColumns []string `json:"references_columns"`
	// OnDelete is what happens to a referencing row when the referenced one is deleted.
	OnDelete ReferentialAction `json:"on_delete,omitempty"`
	// OnUpdate is what happens to it when the referenced row's key is changed.
	OnUpdate ReferentialAction `json:"on_update,omitempty"`
	// Beyond marks a key the v1 operation set cannot express -- a deferrable one, for instance.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Validate reports whether both sides of the key are named and equally wide.
func (f ForeignKey) Validate() error {
	label := f.label()

	if err := requireColumns(label, f.Columns); err != nil {
		return err
	}

	if strings.TrimSpace(f.ReferencesTable) == "" {
		return fmt.Errorf("%s references no table", label)
	}

	if err := requireColumns(label+" referenced side", f.ReferencesColumns); err != nil {
		return err
	}

	if len(f.Columns) != len(f.ReferencesColumns) {
		return fmt.Errorf("%s has %d columns and references %d, which cannot pair",
			label, len(f.Columns), len(f.ReferencesColumns))
	}

	return f.validateActions(label)
}

// validateActions reports an action this vocabulary does not name, on either side of the key.
func (f ForeignKey) validateActions(label string) error {
	for _, named := range []struct {
		field  string
		action ReferentialAction
	}{{"on_delete", f.OnDelete}, {"on_update", f.OnUpdate}} {
		if named.action != "" && !named.action.Valid() {
			return fmt.Errorf("%s names %s %q (expected one of: %s)",
				label, named.field, string(named.action), joinActions())
		}
	}

	return f.Beyond.Validate()
}

// label names a foreign key as far as the engine named it, for a message about it.
func (f ForeignKey) label() string {
	if f.Name == "" {
		return "an unnamed foreign key"
	}

	return fmt.Sprintf("foreign key %q", f.Name)
}

// Canonical returns the key with both actions resolved, so an unnamed one and no_action agree.
func (f ForeignKey) Canonical() ForeignKey {
	f.OnDelete = f.OnDelete.Canonical()
	f.OnUpdate = f.OnUpdate.Canonical()

	return f
}

// Equal reports whether two foreign keys are the same key, ignoring what the engine calls it.
func (f ForeignKey) Equal(other ForeignKey) bool {
	left, right := f.Canonical(), other.Canonical()

	return sameColumns(left.Columns, right.Columns) &&
		left.ReferencesTable == right.ReferencesTable &&
		sameColumns(left.ReferencesColumns, right.ReferencesColumns) &&
		left.OnDelete == right.OnDelete &&
		left.OnUpdate == right.OnUpdate &&
		left.Beyond.Equal(right.Beyond)
}

// shape renders what identifies a foreign key, which is what canonical order sorts it by.
func (f ForeignKey) shape() string {
	return strings.Join([]string{
		strings.Join(f.Columns, ","),
		f.ReferencesTable,
		strings.Join(f.ReferencesColumns, ","),
		f.Name,
	}, "\x00")
}

// requireColumns reports whether a column list is present, fully named, and free of repeats.
func requireColumns(label string, columns []string) error {
	if len(columns) == 0 {
		return fmt.Errorf("%s names no columns", label)
	}

	seen := make(map[string]bool, len(columns))

	for i, column := range columns {
		if strings.TrimSpace(column) == "" {
			return fmt.Errorf("%s names an empty column at position %d", label, i+1)
		}

		if seen[column] {
			return fmt.Errorf("%s names %q more than once", label, column)
		}

		seen[column] = true
	}

	return nil
}

// sameColumns reports whether two column lists are the same list in the same order.
func sameColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}

// joinActions renders the referential-action vocabulary for an error message.
func joinActions() string {
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = string(a)
	}

	return strings.Join(names, ", ")
}
