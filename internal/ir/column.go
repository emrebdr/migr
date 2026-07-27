package ir

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// Function names a default-producing function every driver maps to its own spelling.
type Function string

// The neutral default functions. Drivers translate; the IR never carries engine SQL.
const (
	FuncCurrentTimestamp Function = "current_timestamp"
	FuncCurrentDate      Function = "current_date"
	FuncCurrentTime      Function = "current_time"
	FuncUUID             Function = "uuid"
)

// functions lists every accepted default function.
var functions = []Function{FuncCurrentTimestamp, FuncCurrentDate, FuncCurrentTime, FuncUUID}

// ReferentialAction is what a foreign key does when its referenced row changes.
type ReferentialAction string

// The referential actions, spelled as SQL names them.
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

// Default is a column default: either a literal or a neutral function, never both.
type Default struct {
	Literal  *string  `json:"literal,omitempty"`
	Function Function `json:"function,omitempty"`
}

// Literal returns a default holding the given literal, encoded as a string.
func Literal(v string) *Default {
	return &Default{Literal: &v}
}

// Call returns a default produced by one of the neutral functions.
func Call(f Function) *Default {
	return &Default{Function: f}
}

// Validate reports whether the default names exactly one source of its value.
func (d *Default) Validate() error {
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
	Name     string      `json:"name"`
	Type     schema.Type `json:"type"`
	Nullable bool        `json:"nullable"`
	Default  *Default    `json:"default,omitempty"`
}

// Validate reports whether the column names itself and carries a usable type.
func (c Column) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("column name is required")
	}

	if c.Type.Zero() {
		return fmt.Errorf("column %q has no type", c.Name)
	}

	if err := c.Default.Validate(); err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}

	return nil
}

// validateAction reports whether a referential action is one of the accepted set.
func validateAction(field string, a ReferentialAction) error {
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
