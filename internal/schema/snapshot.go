package schema

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Snapshot is one namespace's schema, as a driver read it: every table and every view.
type Snapshot struct {
	// Tables is every table the namespace holds.
	Tables []Table `json:"tables,omitempty"`
	// Views is every view it holds, which share the table namespace on all three engines.
	Views []View `json:"views,omitempty"`
}

// Table is one table: its columns, its key, and everything constraining it.
type Table struct {
	// Name is the table's own name, unqualified, as the operations name it.
	Name string `json:"name"`
	// Columns are its columns, which an engine can hold none of.
	Columns []Column `json:"columns,omitempty"`
	// PrimaryKey is its primary key, absent where it has none.
	PrimaryKey *PrimaryKey `json:"primary_key,omitempty"`
	// Indexes are its secondary indexes.
	Indexes []Index `json:"indexes,omitempty"`
	// Uniques are its unique constraints, which two engines cannot tell from unique indexes.
	Uniques []Unique `json:"uniques,omitempty"`
	// Checks are its check constraints, whose expressions are engine text.
	Checks []Check `json:"checks,omitempty"`
	// ForeignKeys are the references it makes to other tables.
	ForeignKeys []ForeignKey `json:"foreign_keys,omitempty"`
	// Beyond marks a table the v1 operation set cannot express -- WITHOUT ROWID, for instance.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// View is one view, whose body is engine SQL and does not port.
type View struct {
	// Name is the view's own name, unqualified.
	Name string `json:"name"`
	// Query is the body as the engine stores it, which is text rather than anything portable.
	Query string `json:"query"`
	// Beyond marks a view the v1 operation set cannot express -- a materialized one, for instance.
	Beyond *Unrepresented `json:"beyond,omitempty"`
}

// Table returns the table of the given name, and whether the namespace holds one.
func (s Snapshot) Table(name string) (Table, bool) {
	for _, table := range s.Tables {
		if table.Name == name {
			return table, true
		}
	}

	return Table{}, false
}

// View returns the view of the given name, and whether the namespace holds one.
func (s Snapshot) View(name string) (View, bool) {
	for _, view := range s.Views {
		if view.Name == name {
			return view, true
		}
	}

	return View{}, false
}

// Column returns the column of the given name, and whether the table holds one.
func (t Table) Column(name string) (Column, bool) {
	for _, column := range t.Columns {
		if column.Name == name {
			return column, true
		}
	}

	return Column{}, false
}

// Empty reports whether the namespace holds nothing at all, which an unmigrated database does not.
func (s Snapshot) Empty() bool {
	return len(s.Tables) == 0 && len(s.Views) == 0
}

// Validate reports the first thing wrong with a snapshot, a zero one being an empty database.
func (s Snapshot) Validate() error {
	if err := s.validateNames(); err != nil {
		return err
	}

	for _, table := range s.Tables {
		if err := table.Validate(); err != nil {
			return err
		}
	}

	for _, view := range s.Views {
		if err := view.Validate(); err != nil {
			return err
		}
	}

	return nil
}

// validateNames reports a name two objects claim, which no engine's namespace permits.
func (s Snapshot) validateNames() error {
	claimed := make(map[string]Element, len(s.Tables)+len(s.Views))

	for _, table := range s.Tables {
		if strings.TrimSpace(table.Name) == "" {
			return errors.New("a table has no name")
		}

		if held, taken := claimed[table.Name]; taken {
			return fmt.Errorf("two objects claim the name %q, a table and a %s", table.Name, held)
		}

		claimed[table.Name] = ElementTable
	}

	for _, view := range s.Views {
		if strings.TrimSpace(view.Name) == "" {
			return errors.New("a view has no name")
		}

		if held, taken := claimed[view.Name]; taken {
			return fmt.Errorf("two objects claim the name %q, a view and a %s", view.Name, held)
		}

		claimed[view.Name] = ElementView
	}

	return nil
}

// Validate reports the first thing wrong with a table, which can legitimately hold no columns.
func (t Table) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("a table has no name")
	}

	if err := t.validateColumns(); err != nil {
		return fmt.Errorf("table %q: %w", t.Name, err)
	}

	if err := t.validateConstraints(); err != nil {
		return fmt.Errorf("table %q: %w", t.Name, err)
	}

	return t.Beyond.Validate()
}

// validateColumns reports a column that is not usable, or a name two of them claim.
func (t Table) validateColumns() error {
	claimed := make(map[string]bool, len(t.Columns))

	for _, column := range t.Columns {
		if err := column.Validate(); err != nil {
			return err
		}

		if claimed[column.Name] {
			return fmt.Errorf("two columns claim the name %q", column.Name)
		}

		claimed[column.Name] = true
	}

	return nil
}

// validateConstraints reports a constraint that is not usable, or one naming a column not there.
func (t Table) validateConstraints() error {
	for _, constraint := range t.constraints() {
		if err := constraint.validate(); err != nil {
			return err
		}

		if err := t.validateCovered(constraint); err != nil {
			return err
		}
	}

	return nil
}

// validateCovered reports a constraint over a column this table does not hold.
func (t Table) validateCovered(c constrained) error {
	for _, column := range c.covers() {
		if _, held := t.Column(column); !held {
			return fmt.Errorf("%s covers %q, which this table has no column of",
				c.describe(), column)
		}
	}

	return nil
}

// constrained is one thing constraining a table, which validation treats alike whatever it is.
type constrained interface {
	validate() error
	covers() []string
	describe() string
}

// constraints lists everything constraining this table, in the order validation reports on them.
func (t Table) constraints() []constrained {
	found := make([]constrained, 0, 1+len(t.Indexes)+len(t.Uniques)+len(t.ForeignKeys))

	if t.PrimaryKey != nil {
		found = append(found, keyConstraint{t.PrimaryKey})
	}

	for _, index := range t.Indexes {
		found = append(found, indexConstraint{index})
	}

	for _, unique := range t.Uniques {
		found = append(found, uniqueConstraint{unique})
	}

	for _, check := range t.Checks {
		found = append(found, checkConstraint{check})
	}

	for _, key := range t.ForeignKeys {
		found = append(found, foreignConstraint{key})
	}

	return found
}

// keyConstraint adapts a primary key to what validation asks of everything constraining a table.
type keyConstraint struct{ key *PrimaryKey }

func (c keyConstraint) validate() error  { return c.key.Validate() }
func (c keyConstraint) covers() []string { return c.key.Columns }
func (c keyConstraint) describe() string { return "the primary key" }

// indexConstraint adapts an index to the same.
type indexConstraint struct{ index Index }

func (c indexConstraint) validate() error  { return c.index.Validate() }
func (c indexConstraint) covers() []string { return c.index.Columns }
func (c indexConstraint) describe() string { return fmt.Sprintf("index %q", c.index.Name) }

// uniqueConstraint adapts a unique constraint to the same.
type uniqueConstraint struct{ unique Unique }

func (c uniqueConstraint) validate() error  { return c.unique.Validate() }
func (c uniqueConstraint) covers() []string { return c.unique.Columns }
func (c uniqueConstraint) describe() string {
	return fmt.Sprintf("unique constraint %q", c.unique.Name)
}

// checkConstraint adapts a check constraint, which names no columns an expression is not made of.
type checkConstraint struct{ check Check }

func (c checkConstraint) validate() error  { return c.check.Validate() }
func (c checkConstraint) covers() []string { return nil }
func (c checkConstraint) describe() string {
	return fmt.Sprintf("check constraint %q", c.check.Name)
}

// foreignConstraint adapts a foreign key, whose referenced side may live outside this snapshot.
type foreignConstraint struct{ key ForeignKey }

func (c foreignConstraint) validate() error  { return c.key.Validate() }
func (c foreignConstraint) covers() []string { return c.key.Columns }
func (c foreignConstraint) describe() string { return c.key.label() }

// Validate reports whether the view names itself and carries the body the engine holds.
func (v View) Validate() error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("a view has no name")
	}

	if strings.TrimSpace(v.Query) == "" {
		return fmt.Errorf("view %q carries no query", v.Name)
	}

	return v.Beyond.Validate()
}

// Canonical returns the snapshot in its canonical order, which is what comparison is defined over.
func (s Snapshot) Canonical() Snapshot {
	canonical := Snapshot{}

	if len(s.Tables) > 0 {
		canonical.Tables = make([]Table, 0, len(s.Tables))
		for _, table := range s.Tables {
			canonical.Tables = append(canonical.Tables, table.Canonical())
		}

		slices.SortStableFunc(canonical.Tables, func(a, b Table) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	if len(s.Views) > 0 {
		canonical.Views = slices.Clone(s.Views)
		slices.SortStableFunc(canonical.Views, func(a, b View) int {
			return strings.Compare(a.Name, b.Name)
		})
	}

	return canonical
}

// Canonical returns the table with its sets sorted and its actions resolved.
func (t Table) Canonical() Table {
	canonical := t
	canonical.Columns = sortedByName(t.Columns, func(c Column) string { return c.Name })
	canonical.Indexes = sortedByName(t.Indexes, func(i Index) string { return i.Name })
	canonical.Uniques = sortedByName(t.Uniques, func(u Unique) string { return u.Name })
	canonical.Checks = sortedByName(t.Checks, func(c Check) string {
		return c.Name + "\x00" + c.Expression
	})
	canonical.ForeignKeys = sortedByName(canonicalKeys(t.ForeignKeys), ForeignKey.shape)

	return canonical
}

// canonicalKeys resolves every key's actions, so an unnamed action and no_action are one value.
func canonicalKeys(keys []ForeignKey) []ForeignKey {
	if len(keys) == 0 {
		return nil
	}

	canonical := make([]ForeignKey, 0, len(keys))
	for _, key := range keys {
		canonical = append(canonical, key.Canonical())
	}

	return canonical
}

// sortedByName returns a copy of a set in the order its own key sorts it, and nil for an empty one.
func sortedByName[T any](items []T, key func(T) string) []T {
	if len(items) == 0 {
		return nil
	}

	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b T) int {
		return strings.Compare(key(a), key(b))
	})

	return sorted
}

// Equal reports whether two snapshots describe the same schema, in whatever order each holds it.
func (s Snapshot) Equal(other Snapshot) bool {
	left, right := s.Canonical(), other.Canonical()

	if len(left.Tables) != len(right.Tables) || len(left.Views) != len(right.Views) {
		return false
	}

	for i := range left.Tables {
		if !left.Tables[i].Equal(right.Tables[i]) {
			return false
		}
	}

	for i := range left.Views {
		if !left.Views[i].Equal(right.Views[i]) {
			return false
		}
	}

	return true
}

// Equal reports whether two tables hold the same columns, key, indexes and constraints.
func (t Table) Equal(other Table) bool {
	left, right := t.Canonical(), other.Canonical()

	return left.Name == right.Name &&
		left.PrimaryKey.Equal(right.PrimaryKey) &&
		left.Beyond.Equal(right.Beyond) &&
		sameElements(left.Columns, right.Columns, Column.Equal) &&
		sameElements(left.Indexes, right.Indexes, Index.Equal) &&
		sameElements(left.Uniques, right.Uniques, Unique.Equal) &&
		sameElements(left.Checks, right.Checks, Check.Equal) &&
		sameElements(left.ForeignKeys, right.ForeignKeys, ForeignKey.Equal)
}

// Equal reports whether two views are the same view, their bodies compared as the text they are.
func (v View) Equal(other View) bool {
	return v.Name == other.Name && v.Query == other.Query && v.Beyond.Equal(other.Beyond)
}

// sameElements reports whether two canonically ordered sets hold the same things.
func sameElements[T any](left, right []T, equal func(T, T) bool) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if !equal(left[i], right[i]) {
			return false
		}
	}

	return true
}
