// Package fold applies IR operations to a schema snapshot, which is the opposite
// direction from internal/diff.
//
// Status: complete (M11-T5).
//
// # What it is a function of
//
// Forward takes a snapshot and a list of operations and returns the schema they
// leave, plus every operation it could not fold. Pure, deterministic, no error --
// the shape diff.Diff, engine.Destructive and schema.Unrepresentable already have:
// one policy, read by a report and by a command alike.
//
// It is a package of its own rather than a file in internal/diff because the
// differ's names-only availability model must not grow into a projection,
// and rather than a method on schema.Snapshot because nothing may let
// internal/schema from importing internal/ir at all.
//
// Over a list the differ produced the two compose to the identity: Forward(a,
// Diff(a, b)) is b. That is what judges this package, since nothing else can -- a
// hand-written expectation per operation says only that it matches itself.
//
// # It folds and it does not validate
//
// An operation naming something that is not there is a Refusal rather than a panic
// or a silent create: an add_column onto a table nothing created, an index over a
// column nothing added, a reference to a table this schema does not hold. That is
// what makes the same fold readable as internal/validate's reference-integrity
// check (M10-T3), where those refusals are the findings rather than a problem --
// folded from two starting points, since neither the live schema nor the project's
// own beginning is a sound basis alone.
//
// A refusal stops that operation and nothing after it. One exception ends the
// projection entirely, and it is the reason this package has a caller that refuses
// whole projects: raw is engine SQL, whose effect on the schema nothing here can
// read, so a list carrying one leaves every schema after it unknowable. Diffing
// against a partial reading would propose dropping whatever the statement built.
//
// # What it does that no payload records
//
// Two things, and both are what every engine does on its own. A primary key makes
// the columns it covers NOT NULL, and a dropped column takes its index, its unique
// constraint, its foreign key and its place in the primary key with it. Both are
// measured gaps (M3-T7, M9-T7) that engine.Unrestorable's implied half refuses
// over, so a projection that left them out would describe a schema no server
// produces -- and would then have migr diff propose tightening columns the engine
// had already tightened.
//
// # A projected column type
//
// schema.ColumnType requires a native spelling and a projection has never seen an
// engine, so it carries the logical type's own spelling as its native one. Nothing
// downstream can tell: ColumnType.Equal compares two exact types on the logical
// type alone, so a projected int32 equals PostgreSQL's integer and MySQL's int.
//
// What it does mean is that a projected type is always exact, so meeting a live
// type the catalog could only guess at is a refusal by design.
// Measured: SQLite declares int16, int32 and float32 ambiguously, and MySQL
// declares timestamp and timestamptz ambiguously. SQLite's int64 and text are
// ambiguous and guess correctly, so those compare.
//
// # Comparable, and why it has to exist
//
// A view's body and a check's expression are engine text, and four of the five
// engines rewrite their own. One check expression reads back four ways across the
// four servers -- bare on SQLite, double-parenthesised on PostgreSQL, and with the
// column backquoted on both MySQL variants -- and one view asked for as
// SELECT id FROM t comes back expanded by PostgreSQL and database-qualified by
// MySQL. The exact spellings differ per engine.
//
// So Comparable carries the live text across for every element both schemas hold
// under one name, and only a genuine arrival or departure is left to propose.
// Without it every diff over a database that is exactly up to date would propose
// replacing every view and every check it can see. An element only one side holds
// keeps its own text, which is what lets the operation that creates it carry the
// real body.
//
// The third engine-text field needs none of this: every driver normalizes a column
// default into the neutral vocabulary, and what it cannot normalize it
// records as an expression, which internal/diff refuses rather than comparing.
package fold
