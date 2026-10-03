package schema

import "slices"

// Function names a default-producing function every driver maps to its own spelling.
type Function string

// The neutral default functions. Drivers translate; neither the IR nor a snapshot invents SQL.
const (
	FuncCurrentTimestamp Function = "current_timestamp"
	FuncCurrentDate      Function = "current_date"
	FuncCurrentTime      Function = "current_time"
	FuncUUID             Function = "uuid"
)

// functions lists every accepted default function.
var functions = []Function{FuncCurrentTimestamp, FuncCurrentDate, FuncCurrentTime, FuncUUID}

// Functions returns every accepted default function, in declaration order.
func Functions() []Function {
	return slices.Clone(functions)
}

// Valid reports whether a function is one of the four this vocabulary names.
func (f Function) Valid() bool {
	return slices.Contains(functions, f)
}

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

// Actions returns every accepted referential action, in declaration order.
func Actions() []ReferentialAction {
	return slices.Clone(actions)
}

// Valid reports whether an action is one of the five this vocabulary names.
func (a ReferentialAction) Valid() bool {
	return slices.Contains(actions, a)
}

// Canonical resolves an unnamed action to no_action, which is what SQL does when nothing is named.
func (a ReferentialAction) Canonical() ReferentialAction {
	if a == "" {
		return ActionNoAction
	}

	return a
}
