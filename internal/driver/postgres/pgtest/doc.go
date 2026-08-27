// Package pgtest renders a PostgreSQL schema as text, so two moments can be compared.
//
// # Why this is a package and not a test helper
//
// Invariant #3 bans raw SQL above a driver package, and M5-T2 stated the part that governs tests:
// no test above a driver package composes SQL to send to a database on migr's behalf. A catalog
// probe is exactly that, so it cannot live in cmd/migr -- which is the only package permitted to
// drive the whole stack, because it is the only one permitted to name a concrete driver.
//
// So the probe lives here, inside the driver's own subtree, and is exported. depguard denies the
// internal/driver/ prefix to everything but cmd/migr, so the one package that may ask is the one
// package that may import this (ADR 0040).
//
// # One set of questions, two ways of asking
//
// Digest takes an Ask rather than a connection, because its two callers cannot reach the server the
// same way. Over is the ordinary one: it asks through driver.Conn, so it needs no connection story
// of its own and inherits the read-only transaction Query already runs in (ADR 0032). But this
// driver's own suite describes a schema from *inside* an open transaction, to see what a migration
// changed before the rollback discards it -- and ADR 0017 has a session refuse a second transaction,
// so Query cannot serve that caller at all. It passes its own pgx connection instead.
//
// The seam is therefore what keeps the SQL in one place. Duplicating the probes so that each caller
// could own its fetcher would mean two digests that drift, which is the one thing a digest must not
// do: it exists to be compared with itself.
//
// # What it renders, and what it is not
//
// Everything comes from PostgreSQL's own printers -- pg_get_expr, pg_get_indexdef,
// pg_get_constraintdef -- so no oid or relfilenode reaches the text and two runs of one schema
// render identically. Relations are listed as well as described, because every other probe
// describes contents: without it, a table drop_column emptied would be indistinguishable from one
// that was dropped, and a rollback that dropped the wrong thing would compare equal.
//
// This is not introspection. internal/schema's Snapshot is M11-T1 and per-driver introspection is
// M11-T2; M11-T6 is where the round trip moves onto the neutral snapshot and this package goes
// away. Until then a done-when asking for "introspection snapshot comparison" has nothing else to
// mean, which is the same wall M3-T5 hit and recorded under snapshotSupersededBy.
package pgtest
