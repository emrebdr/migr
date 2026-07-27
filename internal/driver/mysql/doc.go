// Package mysql implements the Driver contract for MySQL and MariaDB.
//
// MySQL has no transactional DDL, so a failed migration leaves partial state.
// This driver is therefore the proving ground for the engine's statement-level
// recovery, dirty-state marking, and resume model. Lock impact is resolved from
// the ALTER algorithm that actually applies — INSTANT, INPLACE, or COPY —
// which depends on the column type and the specific alteration and so cannot be
// a static table.
//
// Locking uses GET_LOCK, which is connection-scoped: the same connection must
// both hold and release it.
//
// Status: scaffolding. Implemented across M9, with online DDL in M13-T10
// (see TASKS.md).
package mysql
