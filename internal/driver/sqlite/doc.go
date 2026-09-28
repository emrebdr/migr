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
// That no-op is also why M8-T4's table rebuild cannot turn foreign keys off: the engine runs a
// migration inside one transaction (ADR 0017). What does work inside one is defer_foreign_keys,
// which holds enforcement until COMMIT, with PRAGMA foreign_key_check before it -- measured here,
// so that task inherits the answer rather than rediscovering it.
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
// # What this task ships, and what refuses by name
//
// M8-T1 ships the driver complete and honestly unsupported, which is M3-T1's shape. All twenty
// operations are declared Unsupported naming M8-T2, Compile refuses identically because the
// declaration is derived from it, and test/golden/sqlite/ records twenty refusals -- so M8-T2
// arrives as a diff turning each into SQL rather than as twenty new files.
//
// Lock refuses, naming M8-T5: lock.strategy's file half is the one this engine can implement, and a
// lock file does not vanish when its owner dies, so it owes a staleness rule before anything may
// depend on it. History() returns a store whose every method refuses, naming M8-T6: the history
// table is itself created through driver-compiled operations (invariant #3), so it cannot precede
// the compiler. Both are values rather than nils, because the engine calls Conn.History() without
// being able to check for one.
//
// A configured schema is refused rather than ignored. A SQLite database is one file, and the only
// other namespaces are the ones ATTACH creates, which nothing here opens.
//
// Status: connection handling. The rest of M8 fills in the compiler, the capability declaration,
// the table-rebuild emulation, the file lock, and the history store (see TASKS.md).
package sqlite
