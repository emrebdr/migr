// Package engine orchestrates migration execution: discovery, dependency
// ordering, locking, transactions, apply and rollback, and batch semantics.
//
// The executor runs each migration in a transaction where the driver supports
// it, and must also accept operations that declare they cannot run inside one —
// CREATE INDEX CONCURRENTLY being the motivating case. When a driver has no
// transactional DDL, a partial failure marks the migration dirty with the index
// of the last successful statement, and the next apply refuses to proceed until
// that state is explicitly resolved.
//
// Status: scaffolding. Implemented across M4, with the transaction opt-out
// completed in M13-T4 (see TASKS.md).
package engine
