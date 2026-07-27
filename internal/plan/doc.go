// Package plan turns Operation IR into an ordered, compiled execution plan.
//
// Planning is deterministic: no time.Now, no randomness, no map-iteration-order
// dependence, so golden tests stay stable (CLAUDE.md invariant #8). The planner
// also decomposes unsafe operations into safe sequences where the target engine
// permits, and every rewrite it applies is visible in migr plan output rather
// than silent.
//
// Status: scaffolding. Implemented in M4 and M13-T5 (see TASKS.md).
package plan
