package schema

import (
	"errors"
	"fmt"
	"strings"
)

// Unrepresented records a definition the v1 operation set cannot express, in the engine's own words.
type Unrepresented struct {
	// Detail states what the engine holds that nothing here can express, in one line.
	Detail string `json:"detail"`
}

// Validate reports whether the marker says what it is, a nil one marking nothing at all.
func (u *Unrepresented) Validate() error {
	if u == nil {
		return nil
	}

	if strings.TrimSpace(u.Detail) == "" {
		return errors.New("something is marked as beyond the operation set without saying what")
	}

	return nil
}

// Equal reports whether two markers describe the same thing, an absent one included.
func (u *Unrepresented) Equal(other *Unrepresented) bool {
	if u == nil || other == nil {
		return u == nil && other == nil
	}

	return u.Detail == other.Detail
}

// Element names the kind of thing a snapshot holds, which is what a finding reports about.
type Element string

// The eight kinds of thing a snapshot can hold, each of which can be beyond the operation set.
const (
	ElementTable      Element = "table"
	ElementColumn     Element = "column"
	ElementPrimaryKey Element = "primary_key"
	ElementIndex      Element = "index"
	ElementUnique     Element = "unique"
	ElementCheck      Element = "check"
	ElementForeignKey Element = "foreign_key"
	ElementView       Element = "view"
)

// Finding is one thing in a snapshot that migr can see and must not act on.
type Finding struct {
	// Table is the table it belongs to, empty for a view, which belongs to no table.
	Table string
	// Element is what kind of thing it is.
	Element Element
	// Object is its own name, empty for an element the engine names nothing.
	Object string
	// Detail is what the engine holds that nothing here can express.
	Detail string
}

// Describe states one finding in a line, naming where it is before what it is.
func (f Finding) Describe() string {
	return fmt.Sprintf("%s %s: %s", f.Element, f.where(), f.Detail)
}

// where names the object a finding is about, as far as the engine named it.
func (f Finding) where() string {
	switch {
	case f.Table == "":
		return fmt.Sprintf("%q", f.Object)
	case f.Object == "":
		return fmt.Sprintf("on %q", f.Table)
	default:
		return fmt.Sprintf("%q on %q", f.Object, f.Table)
	}
}

// Unrepresentable lists everything in a snapshot that migr can see and must not act on.
func Unrepresentable(s Snapshot) []Finding {
	canonical := s.Canonical()

	var found []Finding

	for _, table := range canonical.Tables {
		found = append(found, unrepresentableIn(table)...)
	}

	for _, view := range canonical.Views {
		found = append(found, marked(Finding{Element: ElementView, Object: view.Name},
			view.Beyond)...)
	}

	return found
}

// unrepresentableIn lists everything about one table that nothing here can express.
func unrepresentableIn(t Table) []Finding {
	found := marked(Finding{Table: t.Name, Element: ElementTable}, t.Beyond)

	for _, column := range t.Columns {
		found = append(found, marked(Finding{Table: t.Name, Element: ElementColumn,
			Object: column.Name}, column.Beyond)...)
	}

	found = append(found, marked(Finding{Table: t.Name, Element: ElementPrimaryKey,
		Object: primaryKeyName(t.PrimaryKey)}, primaryKeyBeyond(t.PrimaryKey))...)

	for _, index := range t.Indexes {
		found = append(found, marked(Finding{Table: t.Name, Element: ElementIndex,
			Object: index.Name}, index.Beyond)...)
	}

	for _, unique := range t.Uniques {
		found = append(found, marked(Finding{Table: t.Name, Element: ElementUnique,
			Object: unique.Name}, unique.Beyond)...)
	}

	for _, check := range t.Checks {
		found = append(found, marked(Finding{Table: t.Name, Element: ElementCheck,
			Object: check.Name}, check.Beyond)...)
	}

	for _, key := range t.ForeignKeys {
		found = append(found, marked(Finding{Table: t.Name, Element: ElementForeignKey,
			Object: key.Name}, key.Beyond)...)
	}

	return found
}

// marked returns the finding a marker makes, and nothing at all where there is no marker.
func marked(where Finding, beyond *Unrepresented) []Finding {
	if beyond == nil {
		return nil
	}

	where.Detail = beyond.Detail

	return []Finding{where}
}

// primaryKeyName names a table's primary key, which a table without one has none of.
func primaryKeyName(key *PrimaryKey) string {
	if key == nil {
		return ""
	}

	return key.Name
}

// primaryKeyBeyond returns a primary key's marker, which a table without one has none of.
func primaryKeyBeyond(key *PrimaryKey) *Unrepresented {
	if key == nil {
		return nil
	}

	return key.Beyond
}
