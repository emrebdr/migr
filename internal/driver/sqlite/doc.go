// Package sqlite implements the Driver contract for SQLite using modernc.org/sqlite,
// which is pure Go and so keeps CGO off and cross-compilation trivial.
//
// SQLite cannot drop or alter columns directly on older versions, so this is the driver that
// exercises the capability system's emulation path: the table-rebuild strategy must preserve
// indexes, triggers, views, and foreign keys. Version-dependent cases are detected at runtime --
// DROP COLUMN is native from 3.35 -- never assumed. Its single-writer model means most operations
// block writers for their full duration, and the capability set says so.
//
// # The engine is in-process, and three things follow from it
//
// modernc.org/sqlite is SQLite transpiled to Go and linked into this binary. There is no daemon, no
// socket, and no second process, which changes three things this driver would otherwise inherit
// from PostgreSQL's shape.
//
// A live database costs a t.TempDir() and a few milliseconds, so this driver's tests are untagged:
// CLAUDE.md §7 gates a test behind a build tag when it needs something `make test` does not have,
// and SQLite needs nothing. `make check` and both CI legs therefore exercise the real driver, where
// PostgreSQL's live behavior is reachable only through the Docker job.
//
// The version sqlite_version() reports is a fact about this build rather than about the file that
// was opened. Invariant #13 still reads correctly: it is asked for, never hardcoded, and M8-T3's
// verdicts key to what the answer says.
//
// And there is no connection string that can be wrong about where the database is in the way a host
// and port can. What a connection string can be wrong about is everything below.
//
// # One pinned connection, because a pool would be a different database
//
// database/sql is a pool. An in-memory database belongs to the connection that opened it, so a
// second connection is a second, empty database -- and the failure is silent: a migration applies,
// and the next statement sees nothing it did. Open therefore sets MaxOpenConns to one and holds one
// *sql.Conn for the session's life, which a test asserts rather than trusts.
//
// The three pragmas below are connection-scoped for the same reason, and ADR 0011 already required
// pinning for the migration lock. Here it is correctness before it is a lock strategy.
//
// # The three pragmas this driver owns, each read back
//
// foreign_keys is ON. SQLite defaults it OFF for backwards compatibility, which means a migration
// that breaks referential integrity succeeds and leaves dangling rows -- the silent-wrong this
// project refuses elsewhere. busy_timeout is safety.lock_timeout in milliseconds, and it is the
// whole of invariant #12 for this engine: SQLite has one writer, so any statement can wait on the
// write lock, and the default is 0, which means fail immediately rather than wait -- while an
// unbounded wait is what the invariant exists to prevent. query_only is OFF, and Query is what
// turns it on for the length of one read.
//
// Each is set and then read back, because SQLite reports success for a pragma it ignores. The case
// that proves it is foreign_keys inside a transaction: SQLite accepts the statement, applies
// nothing, and reads back the old value. A test drives exactly that.
//
// That no-op is also why the table rebuild below cannot turn foreign keys off: the engine runs a
// migration inside one transaction (ADR 0017). What does work inside one is defer_foreign_keys,
// which holds the enforcement until COMMIT -- and holds nothing else, which is the measurement the
// rebuild was designed around rather than the one it expected (ADR 0054).
//
// A connection string that sets one of the three through modernc's _pragma= is refused, naming the
// pragma. PostgreSQL pins lock_timeout after parsing, overriding a DSN that set it; foreign_keys(0)
// is different in kind, because it asks to change what a migration means, and answering that by
// silently ignoring the operator is worse than refusing. A query string that cannot be decoded is
// refused for the same reason: url.Query() drops what it cannot read and reports nothing, so a
// _pragma hidden behind a bad escape would read as no _pragma at all.
//
// journal_mode is not touched. WAL would make readers stop blocking writers, and it is a durable
// change to the operator's database file rather than a session setting a migration tool may make on
// their behalf.
//
// An empty connection string is refused. SQLite reads an empty filename as a private temporary
// database that is deleted when the process exits, so every migration would apply to a file nobody
// can find afterwards -- and report success.
//
// mode=ro is not refused. A read-only database fails loudly at the first write, and migr status
// against one is a legitimate thing to run.
//
// # A read is read-only because SQLite says so
//
// Query sets query_only for the length of the read, so the refusal comes from the engine rather
// than from a keyword check here, and SQLITE_READONLY is classified into driver.ErrNotReadOnly --
// which lives on the contract because internal/runtime/host cannot name a driver (ADR 0032).
//
// The case that decides it is not PostgreSQL's. SQLite has no data-modifying CTE, so ADR 0032 B's
// fixture does not port; its own equivalent is PRAGMA user_version = 5, which begins with PRAGMA,
// passes any check on the leading keyword, and writes the database header.
//
// Perform runs a migration's own statement outside every transaction and reports what it changed.
// Arguments are bound as text, positionally, and SQLite's placeholder is ?, not $1 -- which is what
// database.driver in the initialize handshake is for. Because SQLite is dynamically typed, a text
// argument compared against a column still matches: column affinity converts the operand. An
// expression is not a column and gets no such conversion.
//
// # Every value comes back as the engine's own text
//
// SQLite stores a class per value rather than per column, and modernc hands each one over as the Go
// type it was stored as. An integer and a float are rendered with strconv; a null is a nil pointer
// and never an empty string; text is taken verbatim.
//
// Bytes are rendered \x48656c6c6f, which is what PostgreSQL's text output gives a bytea, so one
// migration reading one column gets the same answer from either engine.
//
// Text that is not valid UTF-8 is refused, naming the column. SQLite permits arbitrary bytes in a
// TEXT column, every value crosses the wire as a JSON string, and encoding/json substitutes U+FFFD
// silently -- so the alternative is a migration reading a value that is not in the database with
// nothing to say so.
//
// # What a transaction is, and the one thing this driver cannot see
//
// SQLite's DDL is transactional: a schema change is a write to the same file under the same
// transaction as the data. Transact brackets one, commits only when the work returns nil, and
// discards it on anything else including a panic (ADR 0017).
//
// PostgreSQL's equivalent also asks the server for its transaction status, so a transaction this
// driver did not open is caught. SQLite exposes no such status to SQL -- it is the C API's
// sqlite3_get_autocommit -- so the guard here is this driver's own bookkeeping, and the second line
// of defense is SQLite's: a BEGIN a migration sent through host.db.exec makes the next BEGIN fail
// loudly rather than nest.
//
// database/sql rolls a transaction back itself when the context it was begun with is canceled, so
// the rollback here tolerates sql.ErrTxDone rather than reporting a second failure. A COMMIT that
// fails with SQLITE_BUSY leaves SQLite's own transaction open while database/sql considers it done;
// the run fails loudly, and closing the session -- which every command does -- is what discards it.
//
// # Identifiers are always quoted, and there is no length to check
//
// Double quotes, with an embedded quote doubled, unconditionally -- PostgreSQL's rule unchanged,
// and for the same two reasons: "order" is not a keyword and "userID" does not fold. String
// literals are single-quoted with an embedded quote doubled, which keeps them clear of SQLite's
// double-quoted-string-literal misfeature, where a double-quoted name that resolves to nothing is
// reinterpreted as a string rather than reported.
//
// SQLite has no identifier length limit, so checkIdentifier here refuses a NUL byte and nothing
// else. That makes this driver's predicate shorter than PostgreSQL's by exactly the case that does
// not exist, rather than by an omission.
//
// # Logical types
//
//	string(n)   varchar(n)    int16/32/64   integer      uuid        uuid
//	text        text          float32/64    real         json        text
//	bool        boolean       decimal(p,s)  decimal(p,s) binary      blob
//	date        date          timestamp     timestamp    enum(...)   text + CHECK
//	time        time          timestamptz   timestamptz  array(of)   refused
//
// SQLite does not have types. A declared name sets an affinity -- a preference applied when a value
// is stored -- and enforces nothing: a varchar(255) column holds a megabyte and a smallint holds
// 2^62. The names above are chosen so the affinity is right and the logical type survives into
// sqlite_master, which is what M11's introspection reads back. Three rows are not what they look
// like:
//
// One integer carries all three widths, and one real carries both floats, because SQLite has one of
// each and enforces neither width. Declaring bigint would also cost the rowid alias: INTEGER PRIMARY
// KEY is the only spelling SQLite treats as one, and a bigint primary key is an ordinary indexed
// column that assigns nothing and will hold text.
//
// json declares text rather than json, which is the one place affinity beats preserving the name: a
// type named json carries no affinity marker, so it would be NUMERIC, and a JSON document that is a
// bare number would be converted on the way in.
//
// decimal(p,s) is declared and enforced by nothing, and this one can change a value. Measured on
// 3.53.4: a decimal column given '1.10' stores the REAL 1.1. It is declared anyway because it is
// what every SQLite consumer expects to read back and because arithmetic and ORDER BY work; a
// column that must keep its cents belongs in int64 of minor units. See ADR 0051.
//
// An enum is text plus an inline CHECK holding the column to its labels, so this engine supports a
// type PostgreSQL refuses: there is no CREATE TYPE object to leak, the check dies with the column,
// and no IR operation needs to name it. What is lost is declared label ordering, which docs/ir.md
// 5.3 says is part of the type -- ORDER BY here is lexicographic. A literal default that is not one
// of the labels is refused, because it can never satisfy the constraint whatever the table holds.
//
// The default functions map to CURRENT_TIMESTAMP, CURRENT_DATE and CURRENT_TIME. uuid() is refused:
// SQLite links no UUID function, and the hex(randomblob()) idiom would write a generator into the
// operator's schema rather than name one.
//
// # What needs a table rebuild, and what only looks like it does
//
// SQLite's ALTER TABLE renames a table, renames a column, adds a column, and drops one. Seven
// operations are therefore refused with a reason naming M8-T4: alter_column, add_foreign_key,
// drop_foreign_key, add_check, drop_check, add_primary_key and drop_primary_key. Three more sit on
// the boundary and are compiled here:
//
// add_unique is CREATE UNIQUE INDEX and drop_unique is DROP INDEX, because a UNIQUE constraint is a
// unique index in SQLite and the IR gives create_table no way to declare one inline -- so every
// unique in a migr-managed database came from add_unique, which is what makes the drop find it.
//
// create_view with or_replace is two statements, DROP VIEW IF EXISTS then CREATE VIEW, since SQLite
// has no OR REPLACE. Both run inside the migration's one transaction and the DDL is transactional,
// so the replacement is atomic; unlike PostgreSQL's, it does not refuse a changed column list.
//
// drop_column is the native statement, which SQLite gained in 3.35 -- so the declaration refuses it
// below that release, since the compiler emits the same statement either way and has no session to
// ask. SQLite also refuses it for a primary key, a unique, an indexed column, or one a check, view
// or foreign key names, and Compile can see none of that either. The refusal is SQLite's, it is
// loud, and covering those cases is the rebuild.
//
// The seven are declared Emulated by a table rebuild, and Compile asks for a session rather than
// refusing: the statements depend on the table's current definition, which Compile has no
// connection to read (ADR 0053). The session's Expand is what produces them.
//
// # What a rebuild is, and why it is not SQLite's documented one
//
// SQLite documents a 12-step procedure built around ALTER TABLE ... RENAME. Three measurements
// took that apart, and each one is in ADR 0054 with what it showed:
//
// A rename at 3.25+ rewrites references to the table throughout the schema and re-parses the whole
// schema while doing it. Renaming the shadow into place fails outright when a view names the table,
// because the view is dangling at that moment; renaming the original out of the way rewrites the
// children's REFERENCES to point at the shadow, which the rebuild then drops. PRAGMA
// legacy_alter_table takes inside a transaction -- unlike foreign_keys -- and does not stop the
// foreign-key half.
//
// So this rebuild renames nothing:
//
//	PRAGMA defer_foreign_keys = 1;
//	CREATE TABLE "migr_rebuild_users" ( the changed definition );
//	INSERT INTO "migr_rebuild_users" (cols) SELECT cols FROM "users";
//	DROP TABLE "users";
//	CREATE TABLE "users" ( the changed definition );
//	CREATE INDEX ...;    -- every index, before the rows come back
//	INSERT INTO "users" (cols) SELECT cols FROM "migr_rebuild_users";
//	DROP TABLE "migr_rebuild_users";
//	CREATE TRIGGER ...;  -- every trigger, after
//
// Nothing re-parses the schema, so views survive untouched and need no dropping and recreating, and
// the children's REFERENCES still name the table they always named. It costs a second copy of the
// rows, which is already what table_rewrite means.
//
// The order of the restorations is measured too. Indexes go back before the rows, because a foreign
// key pointing at this table needs its unique index to exist or the copy fails with `foreign key
// mismatch`. Triggers go back after, or an AFTER INSERT trigger fires once per row for rows that
// were already there.
//
// defer_foreign_keys is load-bearing: while the table is gone every key pointing at it is violated,
// and deferral postpones the check to a commit by which time it is back with the same rows. What it
// does not postpone is a referential *action* -- measured: DROP TABLE fires ON DELETE CASCADE and
// SET NULL on the children, and the transaction then commits with their rows gone or nulled. So a
// rebuild is refused when any table references this one with an action that mutates, naming the
// child and telling the operator to drop the key, rebuild, and add it back.
//
// # What the rebuild reads, and what it never has to understand
//
// The definition comes from sqlite_schema.sql, which is the only source that carries CHECK
// constraints at all -- no pragma reports them. Its body is split on the commas that separate its
// items, respecting quotes, brackets, backticks, string literals, comments and nesting, and each
// item is classified only far enough to answer whether it is the one the operation names.
//
// Everything else is copied byte for byte. GENERATED ALWAYS AS, COLLATE, a column-level REFERENCES
// with DEFERRABLE, AUTOINCREMENT and the WITHOUT ROWID or STRICT suffix all survive a rebuild
// without this driver knowing what any of them are. alter_column is the only operation that edits
// inside an item, and it edits the clauses it was asked about rather than re-rendering the column.
//
// A rebuild refuses rather than guessing when the table is not there, when its definition cannot be
// split, when the operation names a constraint or column the definition does not carry, when
// add_primary_key meets a table that already declares one, and when the shadow name is taken.
//
// Two ADD COLUMN restrictions are deliberately not refused here. A NOT NULL column with no default,
// and a column with a function default, are both accepted against an empty table and refused only
// once the table holds a row -- measured. The condition is on the data, which neither Compile nor a
// capability Rule can see, and the engine's refusal is inside the migration's transaction.
//
// # What this driver does not emit
//
// No CREATE INDEX CONCURRENTLY, because SQLite has none: one writer means an index build holds the
// write lock for its duration, and saying so is the lock axis M8-T3 owes. So nothing here sets
// Statement.NoTransaction, and a migration this driver compiles is always exactly one transactional
// run. Every composed statement ends in a semicolon; a raw operation does not, because run verbatim
// is the whole contract and its author may already have written one.
//
// # What an operation costs a live database
//
//	create_table  brief_exclusive   drop_table   long_exclusive   drop_column  table_rewrite
//	create_view   brief_exclusive   add_index    long_exclusive   the seven    table_rewrite
//	drop_view     brief_exclusive   drop_index   long_exclusive   raw          unknown
//	add_column    brief_exclusive   add_unique   long_exclusive
//	rename_table  brief_exclusive   drop_unique  long_exclusive
//	rename_column brief_exclusive
//
// Nothing here is lock_free, and that is the declaration rather than a gap. SQLite takes one
// database-wide write lock and has no per-table locking at all, so even CREATE TABLE stops every
// other writer for as long as it runs -- and in the default rollback-journal mode, which this
// driver does not change, it stops readers too. PostgreSQL calls create_table lock_free because it
// blocks nothing that already exists; the same verdict here would pass --require-safe for an
// operation that halts the database.
//
// drop_column is the least obvious row. SQLite's native DROP COLUMN is not a metadata change: it
// rewrites the whole table content to omit the column, so the operation that compiles to one short
// statement is the most expensive thing above.
//
// Every row is measured rather than read out of documentation, which invariant #13 asks for and
// which only an in-process engine makes cheap. Two instruments, neither of them a clock: the
// rollback journal counts the pages a statement rewrote in place, and page_count and
// freelist_count count the pages it allocated and released. Run at two table sizes, that separates
// constant work from work proportional to the table, and a rewrite of rows already there from a
// read of them -- with an UPDATE over every row and an UPDATE over one row as the two controls.
// What it does not measure is duration, so brief and long are told apart by how much they touch
// rather than by how long they hold; separating those by time needs load, which is M13-T12's.
//
// Lock impact does not vary by version here, and that is worth stating because the support axis
// does. The operations whose cost changed across SQLite releases are the ones this driver refuses,
// and drop_column's *support* moves with the version while what it costs does not.
//
// # What refuses by name
//
// Lock refuses, naming M8-T5: lock.strategy's file half is the one this engine can implement, and a
// lock file does not vanish when its owner dies, so it owes a staleness rule before anything may
// depend on it. History() returns a store whose every method refuses, naming M8-T6. Both are values
// rather than nils, because the engine calls Conn.History() without being able to check for one.
//
// A configured schema is refused rather than ignored. A SQLite database is one file, and the only
// other namespaces are the ones ATTACH creates, which nothing here opens.
//
// Status: connection handling, the compiler, the capability declaration, and the table rebuild.
// The rest of M8 fills in the file lock and the history store (see TASKS.md).
package sqlite
