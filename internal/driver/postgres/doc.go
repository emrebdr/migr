// Package postgres implements the Driver contract for PostgreSQL using pgx.
//
// PostgreSQL is the reference driver: it supports transactional DDL and most
// operations natively, which is why the engine is built against it first. It
// provides advisory locking, and its lock-impact verdicts are keyed to the
// detected server version — ADD COLUMN with a default rewrites the table on
// PostgreSQL 10 and is instant from 11 onward.
//
// Status: scaffolding. Implemented across M3, with online strategies in
// M13-T11 (see TASKS.md).
package postgres
