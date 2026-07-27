// Package checksum hashes migrations and detects drift.
//
// A hash covers the normalized source plus the emitted IR, and must be stable
// across runs, machines, and Go versions. Applied migrations are immutable: a
// mismatch is a hard error, never silently repaired (CLAUDE.md invariant #6).
//
// Status: scaffolding. Implemented in M1-T6 (see TASKS.md).
package checksum
