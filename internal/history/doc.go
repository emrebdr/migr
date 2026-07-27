// Package history defines the migration history store contract and the
// reconciliation that resolves each migration to exactly one state: applied,
// pending, missing (recorded but no file), or drifted (checksum mismatch).
//
// This package contains no SQL. The history table itself is created through
// driver-compiled IR operations, because raw SQL lives only in driver packages
// (CLAUDE.md invariant #3).
//
// Status: scaffolding. Implemented in M2-T5 and M4-T3 (see TASKS.md).
package history
