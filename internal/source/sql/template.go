package sql

// Ext is the extension a SQL migration is stored under, and the one migr create writes.
const Ext = ".sql"

// MarkerUp opens the statements that run when a migration is applied.
const MarkerUp = "-- migr:up"

// MarkerDown opens the statements that run when it is rolled back.
const MarkerDown = "-- migr:down"

// Template is the file migr create writes, and it carries no SQL of its own by design.
const Template = MarkerUp + `
-- Statements here run when this migration is applied, in the order they appear.


` + MarkerDown + `
-- ... and these when it is rolled back. Write them: SQL reaches the database as a raw
-- operation, whose effect migr cannot read, so there is no inverse for it to derive.
`
