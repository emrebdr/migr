package migr

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Function names a default-producing function every driver maps to its own spelling.
type Function string

// The four neutral default functions docs/ir.md §6.3 closes the set to.
const (
	FuncCurrentTimestamp Function = "current_timestamp"
	FuncCurrentDate      Function = "current_date"
	FuncCurrentTime      Function = "current_time"
	FuncUUID             Function = "uuid"
)

// functions lists every accepted default function.
var functions = []Function{FuncCurrentTimestamp, FuncCurrentDate, FuncCurrentTime, FuncUUID}

// Functions returns every default function this IR version defines, as a copy.
func Functions() []Function {
	return slices.Clone(functions)
}

// ReferentialAction is what a foreign key does when its referenced row changes.
type ReferentialAction string

// The five referential actions docs/ir.md §6.4 closes the set to.
const (
	ActionNoAction   ReferentialAction = "no_action"
	ActionRestrict   ReferentialAction = "restrict"
	ActionCascade    ReferentialAction = "cascade"
	ActionSetNull    ReferentialAction = "set_null"
	ActionSetDefault ReferentialAction = "set_default"
)

// actions lists every accepted referential action.
var actions = []ReferentialAction{
	ActionNoAction, ActionRestrict, ActionCascade, ActionSetNull, ActionSetDefault,
}

// Actions returns every referential action this IR version defines, as a copy.
func Actions() []ReferentialAction {
	return slices.Clone(actions)
}

// Default is a column default: either a literal or a neutral function, never both.
type Default struct {
	// Literal is a constant value, always carried as a string so a round trip cannot retype it.
	Literal *string `json:"literal,omitempty"`
	// Function is one of the neutral functions, which each driver writes in its own dialect.
	Function Function `json:"function,omitempty"`
}

// Literal returns a default holding the given constant, which the IR always carries as a string.
func Literal(v string) *Default {
	return &Default{Literal: &v}
}

// Call returns a default produced by one of the four neutral functions.
func Call(f Function) *Default {
	return &Default{Function: f}
}

// check reports a default that names no value, or names two.
func (d *Default) check() error {
	if d == nil {
		return nil
	}

	if d.Literal == nil && d.Function == "" {
		return errors.New("default must set either literal or function")
	}

	if d.Literal != nil && d.Function != "" {
		return errors.New("default must set literal or function, not both")
	}

	if d.Function != "" && !slices.Contains(functions, d.Function) {
		return fmt.Errorf("unknown default function %q (expected one of: %s)",
			d.Function, joinFunctions())
	}

	return nil
}

// Column is one column of a table, named with a logical type rather than a native one.
type Column struct {
	// Name is the column's name, which is passed through exactly as written.
	Name string `json:"name"`
	// Type is the logical type, which the target driver maps to a native one.
	Type Type `json:"type"`
	// Nullable is always emitted, since a column is either nullable or it is not.
	Nullable bool `json:"nullable"`
	// Default is the column's default, omitted when it has none.
	Default *Default `json:"default,omitempty"`
}

// check reports a column that names itself with no name, or carries no usable type.
func (c Column) check() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("column name is required")
	}

	if err := c.Type.Err(); err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}

	if err := c.Default.check(); err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}

	return nil
}

// checkAction reports a referential action that is not one of the accepted set.
func checkAction(field string, a ReferentialAction) error {
	if a == "" || slices.Contains(actions, a) {
		return nil
	}

	return fmt.Errorf("unknown %s action %q (expected one of: %s)", field, a, joinActions())
}

// joinFunctions renders the default-function set for an error message.
func joinFunctions() string {
	names := make([]string, len(functions))
	for i, f := range functions {
		names[i] = string(f)
	}

	return strings.Join(names, ", ")
}

// joinActions renders the referential-action set for an error message.
func joinActions() string {
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = string(a)
	}

	return strings.Join(names, ", ")
}

// requireName reports whether a required identifier field is present.
func requireName(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}

	return nil
}

// requireColumns reports whether a required column list is present and fully named.
func requireColumns(field string, columns []string) error {
	if len(columns) == 0 {
		return fmt.Errorf("%s must list at least one column", field)
	}

	for i, c := range columns {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("%s[%d] is empty", field, i)
		}
	}

	return nil
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

// requireNamed reports whether a table and an object name are both present.
func requireNamed(table, name string) error {
	if err := requireName("table", table); err != nil {
		return err
	}

	return requireName("name", name)
}

// requireNamedColumns reports whether a named object over columns is fully specified.
func requireNamedColumns(table, name string, columns []string) error {
	if err := requireNamed(table, name); err != nil {
		return err
	}

	if err := requireColumns("columns", columns); err != nil {
		return err
	}

	return requireDistinctIn("columns", columns)
}
