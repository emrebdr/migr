// Package sqlitetest renders a SQLite database's schema as text, so two moments can be compared.
//
// # Why this is a package and not a test helper
//
// Invariant #3 bans raw SQL above a driver package, and M5-T2 stated the part that governs tests: no
// test above a driver package composes SQL to send to a database on migr's behalf. A catalog probe is
// exactly that, so it cannot live in cmd/migr -- which is the only package permitted to drive the
// whole stack, because it is the only one permitted to name a concrete driver.
//
// So the probe lives here, inside the driver's own subtree, and is exported. This is pgtest's
// argument unchanged (ADR 0040), and it costs the same depguard pair: the blanket deny on the
// internal/driver/ prefix blocks a driver from importing its own child, so internal/driver/sqlite is
// exempted from it and held instead by a rule set denying the other drivers and every language
// runtime by name.
//
// # One question, asked one way
//
// Everything SQLite knows about its own schema is in sqlite_schema, so there is one probe rather than
// PostgreSQL's five, and both callers reach it through driver.Conn.Query -- neither takes its digest
// from inside an open transaction, which is the case that forced pgtest's Ask to exist. The Ask seam
// is kept anyway, because it is what stops a second caller from growing a second fetcher and, with
// it, a second digest: a digest exists to be compared with itself.
//
// # What it renders
//
// The recorded CREATE statement of every object, with newlines and tabs flattened so one object is
// one line, and SQLite's own internal objects excluded. An index SQLite records no statement for --
// the autoindex a UNIQUE or a composite primary key creates -- is rendered as its name plus (none),
// so an index that appears or disappears is still a diff.
//
// Objects belonging to one table can be excluded, which is what lets a round trip ignore the
// migration history: migr up creates the history table, so a digest taken before the first run and
// one taken after the rollback would otherwise differ by the table recording the rollback.
//
// This is not introspection. internal/schema's Snapshot is M11-T1 and per-driver introspection is
// M11-T2; M11-T6 is where the round trip moves onto the neutral snapshot and this package goes away,
// alongside pgtest.
package sqlitetest
