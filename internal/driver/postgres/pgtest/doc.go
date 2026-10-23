// Package pgtest undoes what a test could not undo any other way.
//
// # Why this is a package and not a test helper
//
// Invariant #3 bans raw SQL above a driver package, and M5-T2 stated the part that governs tests:
// no test above a driver package composes SQL to send to a database on migr's behalf. A DROP TABLE
// is exactly that, so it cannot live in cmd/migr -- which is the only package permitted to drive
// the whole stack, because it is the only one permitted to name a concrete driver.
//
// So it lives here, inside the driver's own subtree, and is exported. depguard denies the
// internal/driver/ prefix to everything but cmd/migr, so the one package that may ask is the one
// package that may import this.
//
// # What is left here, and what used to be
//
// Drop removes a table if it is there, and exists for one case M7-T10 needed: a migration that
// sends a statement through host.db.exec and then fails leaves that statement committed,
// and proving it means leaving a table behind in a database other suites compare. Nothing in a
// migration can clean that up. A compensating migration whose only effect is an exec compiles to no
// statements and the engine refuses it; one that dropped the table as an operation would be recorded
// as applied, so a second run would skip it and meet the table still there.
//
// It takes a driver.Conn and uses Perform, since a drop is not a question. It interpolates two
// identifiers, so it refuses anything but lowercase letters, digits and underscores -- which is
// every table a test stages, and is a narrower rule than quoting because the alternative is a helper
// that will interpolate anything.
//
// This package also held a catalog digest until M11-T6: five probes asking PostgreSQL to describe
// one schema, rendered as sorted text, so that two moments could be compared. It existed because
// M3-T5's done-when asked for a snapshot comparison eight milestones before the snapshot existed.
// Conn.Introspect is on the contract as of M11-T2, so that question has a neutral answer and the
// digest was the second one -- deleted, along with the Ask seam that let one set of probes serve a
// caller reading through driver.Conn and a caller reading from inside an open transaction. The
// contract absorbed that too: pgConn.probe reads through this session's transaction when it has one.
package pgtest
