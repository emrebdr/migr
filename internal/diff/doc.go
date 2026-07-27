// Package diff compares two schema snapshots and emits the ordered IR
// operations that close the gap, respecting dependencies — a table is created
// before the foreign keys that reference it.
//
// Rename detection is heuristic and must be confirmed interactively rather than
// guessed: mistaking a rename for a drop-and-add destroys data.
//
// Status: scaffolding. Implemented across M11 (see TASKS.md).
package diff
