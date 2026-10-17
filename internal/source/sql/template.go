package sql

// Ext is the extension a SQL migration is stored under, and the one migr create writes.
const Ext = ".sql"

// Exts are every extension this format is read from, which is the one this package parses.
var Exts = []string{Ext}

// MarkerUp opens the statements that run when a migration is applied.
const MarkerUp = "-- migr:up"

// MarkerDown opens the statements that run when it is rolled back.
const MarkerDown = "-- migr:down"

// MarkerIrreversible declares that nothing can undo this migration, and is followed by the reason.
const MarkerIrreversible = "-- migr:irreversible:"

// MarkerNoTransaction declares that the one statement below it cannot run inside a transaction.
const MarkerNoTransaction = "-- migr:no-transaction"

// Template is the file migr create writes, and it carries no SQL of its own by design.
const Template = MarkerUp + `
-- Statements here run when this migration is applied, in the order they appear.
-- A statement that cannot run inside a transaction, such as CREATE INDEX CONCURRENTLY,
-- takes a line reading ` + MarkerNoTransaction + ` directly above it.


` + MarkerDown + `
-- ... and these when it is rolled back. Write them: SQL reaches the database as a raw
-- operation, whose effect migr cannot read, so there is no inverse for it to derive.
--
-- If this migration truly cannot be undone, say so instead: replace the line above
-- with ` + MarkerIrreversible + ` and the reason it cannot.
`
