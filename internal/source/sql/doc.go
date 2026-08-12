// Package sql is the SQL migration file format: the extension, the section
// markers, and the template `migr create` writes.
//
// It is M5's interim source format, the one a person can use before M6 and M7
// land the SDKs, and it is a real format rather than a stopgap -- plenty of
// projects will never want more than SQL. What it costs is stated below.
//
// # The writer lives where the reader will
//
// M5-T1 writes files this package's parser does not exist to read yet: Emit,
// the engine.Source implementation, is M5-T2. They are one package anyway, for
// the reason internal/migration exports Filename beside Parse -- a format whose
// writer and reader live apart is a format with two definitions, and the second
// one is discovered when a file migr wrote is a file migr cannot read.
//
// # The markers
//
// A migration file is two sections, each opened by a marker on a line of its
// own: MarkerUp, then MarkerDown. Statements run in the order they appear. The
// `-- migr:` prefix is the house convention, already carried by the golden
// files' `-- migr:statement 1` and `-- migr:unsupported` headers.
//
// # What the interim format gives up
//
// SQL reaches the database as ir.Raw, and that operation has two properties the
// rest of the framework does not:
//
// It carries a driver name, so a .sql migration is written against one engine.
// The framework's first claim -- a migration says what changes, never how a
// specific engine performs it -- is the one this format trades away for being
// usable a milestone early.
//
// Its Reverse always fails, because its effect is opaque to the framework. So a
// .sql migration whose down section is empty can never have one derived for it
// (ADR 0022, C4): invariant #10 is satisfied by the author writing the undo, and
// by nothing else. The template says so where an author will read it, which is
// the file itself.
//
// # The template carries no SQL
//
// Invariant #3 is "no raw SQL outside a driver package. Ever." The one file migr
// authors that is named .sql is therefore markers and guidance comments only --
// an example CREATE TABLE, however illustrative, would be the exception that
// makes the invariant a preference. TestTheTemplateCarriesNoSQLOfItsOwn holds
// that mechanically rather than leaving it to be remembered.
package sql
