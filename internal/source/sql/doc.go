// Package sql is the SQL migration file format: the extension, the section
// markers, and the template `migr create` writes.
//
// It is M5's interim source format, the one a person can use before M6 and M7
// land the SDKs, and it is a real format rather than a stopgap -- plenty of
// projects will never want more than SQL. What it costs is stated below.
//
// # The writer lives where the reader does
//
// M5-T1 wrote the template and M5-T2 the parser that reads it back. They are one
// package for the reason internal/migration exports Filename beside Parse -- a
// format whose writer and reader live apart is a format with two definitions,
// and the second one is discovered when a file migr wrote is a file migr cannot
// read. A test emits the template and asserts it yields nothing and authors no
// down, which is that agreement held mechanically.
//
// # The markers
//
// A migration file is two sections, each opened by a marker on a line of its
// own: MarkerUp, then MarkerDown. Statements run in the order they appear. The
// `-- migr:` prefix is the house convention, already carried by the golden
// files' `-- migr:statement 1` and `-- migr:unsupported` headers.
//
// Every rule about those sections is a refusal rather than a guess:
//
// MarkerUp is required. A file without one is refused rather than read as
// all-up, because `-- migr:Up` is then a typo that runs the down section during
// an apply -- creating a table and dropping it again in one migration.
//
// SQL above the first marker is refused, naming its line, since it belongs to
// neither direction and would otherwise be dropped in silence. Comments and
// blank lines above it are fine, and a header comment is the reason.
//
// MarkerDown is optional, and one with nothing under it is the same as none:
// both report engine.ErrNoDownAuthored, which is what has the engine derive an
// inverse instead. A marker carried twice, or a down above an up,
// is refused naming the lines.
//
// # A statement that cannot run inside a transaction
//
// MarkerNoTransaction, alone on a line directly above a statement, declares that
// statement cannot run inside a transaction, and it becomes an ir.Raw carrying
// no_transaction (M13-T4). It marks one statement rather than a file,
// so one migration can create a table in a transaction and build its index
// concurrently outside one. The scanner reads it, so a marker inside a quoted
// body or a block comment is not one; and it is taken out of the statement it
// declares, being migr's and not the server's. A marker that declares nothing is
// refused naming its line: inside a statement no semicolon has ended, twice in a
// row, with no statement after it in its section, above the up marker, and below
// a declaration -- each a migration whose author believes a statement runs alone
// when it would not.
//
// # One operation per statement
//
// Statements splits a section on the semicolons no quoting hides, and each one
// becomes its own ir.Raw. That is what keeps migr's statement model meaningful
// for this format: statements are counted, timed, positioned in a dirty run's
// row, and resumed from in M9-T6. A section handed over as one blob would report
// "1 statement" forever and could never be resumed inside.
//
// Splitting is a lexical property of the file rather than of the engine --
// semicolons, comments and quotes are common to PostgreSQL, MySQL and SQLite.
// The scanner tracks line comments, block comments (nesting, as PostgreSQL
// allows), single- and double-quoted strings, and dollar-quoted bodies. That
// last one is the case that costs data rather than tidiness: cut at the first
// semicolon inside a $$ ... $$ body and a PL/pgSQL function becomes two
// statements that are both invalid. MySQL's DELIMITER is a client directive
// rather than SQL, and the driver that needs it can say so in M9.
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
// .sql migration whose down section is empty can never have one derived for it:
// invariant #10 is satisfied by the author writing the undo, and
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
//
// # The mode is a parameter this source is right to ignore
//
// engine.Source.Emit states whether the engine is running a migration or asking
// it a question. A .sql migration is a text file: it makes no
// callbacks, reads nothing, and cannot branch, so both modes read the same bytes
// and emit the same operations -- which a test asserts rather than assumes,
// because it is the floor the host's identical-IR proof holds a language runtime
// to.
package sql
