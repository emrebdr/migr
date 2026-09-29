// Package mysql implements the Driver contract for MySQL and MariaDB.
//
// MySQL has no transactional DDL, so a failed migration leaves partial state. This driver is
// therefore the proving ground for the engine's statement-level recovery, dirty-state marking, and
// resume model. Lock impact is resolved from the ALTER algorithm that actually applies -- INSTANT,
// INPLACE, or COPY -- which depends on the column type and the specific alteration and so cannot be
// a static table. That is M9-T3; every operation declares LockUnknown until then, which ranks equal
// to TableRewrite for every threshold comparison.
//
// Locking uses GET_LOCK, which is connection-scoped: the same connection must both hold and release
// it. That is M9-T4, and Lock refuses by name until it lands.
//
// # Two engines, one driver, and a version that lies
//
// MariaDB is a fork with its own version line, and telling the two apart is part of what Open does.
// VERSION() and version_comment are both read, because either alone is insufficient: MariaDB
// prepends "5.5.5-" to the version it announces so that clients too old to read a two-digit major do
// not misparse 10.x, and some builds carry that prefix into VERSION() -- so a driver reading the
// first number alone takes MariaDB 11.4 for MySQL 5.5. version_comment names the distribution
// ("MySQL Community Server - GPL", "mariadb.org binary distribution") and settles it when the
// version string does not.
//
// The floor is per variant: MySQL 8.0, MariaDB 10.5. The two version lines share no scale, so one
// number could only be wrong about one of them -- a single floor of 8.0 would accept MariaDB 10.4,
// and a single floor of 10.5 would refuse MySQL 8.0. A server that reports something this driver
// cannot compare is refused rather than assumed, per invariant #13.
//
// Percona and anything else that does not say MariaDB is taken as MySQL, which is what it is.
//
// # One pinned connection, because the migration lock belongs to one
//
// database/sql is a pool. GET_LOCK is connection-scoped, so acquiring on one pooled connection and
// releasing on another silently fails to release -- ADR 0011's requirement, arriving here before
// there is a lock. Open sets MaxOpenConns to one and holds one *sql.Conn for the session's life, and
// a test asserts the pool will hand out no second connection rather than trusting it.
//
// The done-when for this task says "connection pooling"; the contract says one pinned session.
// Pinning out of a configured pool is how both are true.
//
// # The four connection-string flags this driver owns
//
// Each changes what a statement or a value means, so each is refused rather than silently
// overridden -- SQLite's _pragma rule in a second dialect:
//
//   - clientFoundRows would make Perform report rows matched where MySQL reports rows changed;
//   - multiStatements would let one Query carry several, which the read-only enforcement below and
//     M9-T5's statement-level recovery both reason against;
//   - parseTime would hand some values over as time.Time, where every value is the server's own text;
//   - interpolateParams would splice arguments into the statement rather than bind them.
//
// A connection string setting one of the five session variables below is refused for a related
// reason: this driver sets them after connecting and reads them back, so a value the operator asked
// for would be overwritten with nobody told.
//
// A connection string naming no database is refused. MySQL resolves every unqualified name against
// one, so without it the first statement of the first migration fails -- after the migration lock
// has been taken and the history table has been asked for. MySQL's SCHEMA is a synonym for its
// DATABASE, so migr.yaml's schema and the connection string name the same thing: a schema that
// restates the database is accepted and one that names another is refused, two sources for one
// namespace being able only to disagree.
//
// # The five session variables this driver owns, each read back
//
// sql_mode gains NO_BACKSLASH_ESCAPES, and nothing else about it is touched. This is PostgreSQL's
// standard_conforming_strings in MySQL's spelling and the reason is the same: Compile is pure by
// contract and cannot read a session setting, so the session is pinned to the grammar the compiler
// quotes for rather than trusted to already have it. Doubling the quote is then the whole defense.
//
// Both servers leave that mode off by default, so this is an override with a real cost, stated
// rather than buried: a migration's own inline 'a\nb' is four characters with it and three without,
// and DEFAULT 'a\b' stores two characters with it and one control character without. An operator
// whose migrations write inline 'C:\path' or '\n' will find them meaning something else here.
//
// time_zone is +00:00, so an instant rendered as UTC text is stored as itself.
//
// lock_wait_timeout and innodb_lock_wait_timeout both carry safety.lock_timeout, and both are
// invariant #12 for this engine: the first bounds the metadata lock every DDL statement takes, the
// second bounds a row lock, and they are separate queues with defaults of a year and fifty seconds.
// They take whole seconds, so a sub-second safety.lock_timeout is refused at construction rather
// than rounded: MySQL refuses a fraction outright and turns 0 into 1 while reporting success.
//
// transaction_read_only is off, and Query is what turns it on for the length of one read.
//
// Each is set and then read back, because MySQL clamps a value out of range and reports success. A
// numeric or boolean system variable will not take a bound parameter -- SET SESSION
// lock_wait_timeout = ? is error 1232 and transaction_read_only = ? is 1231 -- so those are written
// into the statement, and checkDigits is what keeps that structurally safe rather than safe because
// today's callers happen to pass constants.
//
// character_set_connection is checked and never set. It must be utf8mb4, because every value crosses
// the wire as a JSON string and a connection that transcodes on the way out hands a migration
// characters that are not in the database. A character set is the operator's statement about their
// server, so the refusal names the connection-string parameter that fixes it.
//
// # A read is read-only because the server says so, and a transaction is not enough
//
// Query opens a read-only transaction, which is what ADR 0032 asks for, and pins
// transaction_read_only around it, which is what makes it true.
//
// START TRANSACTION READ ONLY alone refuses an INSERT, an UPDATE, a DELETE and a REPLACE -- and lets
// CREATE TABLE, ALTER TABLE, CREATE INDEX, TRUNCATE and DROP TABLE straight through. Each of those
// implicitly commits, which ends the read-only transaction before the server checks it, and the
// change survives the rollback. Measured on 8.0.46 and 11.4.13. The session variable closes the hole
// because the DDL's own implicit transaction is read-only too.
//
// The case that decides read-only is SELECT ... FOR UPDATE. MySQL has no data-modifying CTE, so ADR
// 0032 B's PostgreSQL fixture does not port -- but FOR UPDATE begins with SELECT, passes any check
// on the leading keyword, and takes exclusive row locks that block every writer on those rows. It is
// refused as error 1792 on both servers, which is classified into driver.ErrNotReadOnly because
// internal/runtime/host cannot name a driver.
//
// Perform runs a migration's own statement outside every transaction and reports RowsAffected
// verbatim. MySQL counts rows changed where PostgreSQL's command tag counts rows matched, so the
// same no-op UPDATE is 0 here and 1 there. clientFoundRows would make them agree and is refused
// instead: every value a read returns is the server's own, and this number is one of them.
//
// Arguments bind positionally as text, and the placeholder is ?, not $1 -- which is what
// database.driver in the initialize handshake is for. A text argument compared against a numeric
// column still matches, because MySQL converts the operand.
//
// # Every value comes back as the server's own text, and the column is what says which kind
//
// MySQL hands text and bytes to a client as the same Go type: everything but an integer and a double
// arrives as []byte. Unlike SQLite, which sends a storage class per value, the value itself cannot
// say which it is -- so ColumnTypes is read, and go-sql-driver derives BLOB from TEXT and BINARY
// from CHAR by whether the field's character set is the binary one.
//
// Bytes render \x48656c6c6f, which is what PostgreSQL's text output gives a bytea, so one migration
// reading one column gets the same answer from any of the three engines. A null is a nil pointer and
// never an empty string. An unsigned bigint past 2^63 arrives as a uint64 and is rendered whole.
//
// Text that is not valid UTF-8 is refused, naming the column, and that check is a
// forward-compatibility guard rather than a routine one: the server transcodes every text column to
// the connection's utf8mb4 on the way out, so a latin1 0xFF arrives as the two bytes of U+00FF and an
// invalid byte cast to CHAR is replaced by the server itself. What is left for it is a field type
// this build of the library cannot name -- it returns an empty type name for one -- whose bytes would
// otherwise reach encoding/json and come back with each invalid byte replaced by U+FFFD.
//
// # What a transaction is here, and the one thing it cannot promise
//
// Transact brackets one transaction and commits only when the work returns nil, discarding it on
// anything else including a panic (ADR 0017). database/sql rolls a transaction back itself when the
// context it was begun with is canceled, so the rollback tolerates sql.ErrTxDone.
//
// MySQL commits implicitly on DDL. A rollback after a CREATE TABLE reports success and undoes
// nothing, and -- worse -- the DDL commits whatever the transaction had already done: a row inserted
// before it survives the rollback too. Measured on both servers, and it is why TransactionalDDL is
// false. From 8.0 a single DDL statement is itself atomic, the data dictionary being transactional,
// so a failed ALTER leaves no half-built table; that bounds the damage to one statement and does not
// make a migration of several recoverable. M9-T5 is where the engine's answer is built.
//
// # The compiler, which is two operations rather than twenty
//
// The history table is created through driver-compiled operations (invariant #3), so create_table
// and add_index have to compile before there is a history store at all. They do, completely --
// backtick quoting, the full logical type map, nullability, composite primary keys, and every
// default form. The other eighteen refuse naming M9-T2, and test/golden/mysql holds all twenty, so
// that task arrives as a diff of eighteen.
//
// Three things the type map decides, each with a cost worth stating:
//
// text is longtext and binary is longblob, because the logical types mean an unbounded string and
// unbounded bytes where MySQL's TEXT and BLOB stop at 65535 bytes.
//
// timestamp and timestamptz both map to datetime(6). MySQL's one zone-aware type, TIMESTAMP, spans
// 1970 to 2038 and so cannot carry a column migr writes forever; DATETIME has the range and no zone
// semantics. This driver chooses the range, renders every instant as UTC, and pins the session's
// time_zone to +00:00 so the two are the same thing for anything it writes. The cost is that two
// logical types map to one native type, which M11-T1's Introspect will not be able to tell apart.
//
// enum is native, which is one of the few places MySQL is more expressive than the IR.
//
// Defaults are where MySQL and MariaDB actually differ, and the rendering is the one spelling both
// accept. CURRENT_TIMESTAMP must carry the column's precision -- a bare one on a datetime(6) is
// error 1067 on MySQL and fine on MariaDB. current_date, current_time and uuid are parenthesised,
// because the bare form is a syntax error on MySQL and legal on MariaDB. A literal default on a
// text, blob or json column is parenthesised too, MySQL refusing the bare form with error 1101. All
// three parenthesised forms are expression defaults, which MySQL gained in 8.0.13 -- so the
// capability rule refuses them below that, keyed to the server rather than assumed. MariaDB has had
// them since 10.2, below this driver's floor, so the gate is MySQL's alone.
//
// add_index carries no prefix length, and that is a limit of the operation rather than of this
// driver: ir.AddIndex names columns and not types, and Compile is pure, so nothing here can know
// that a column is a longtext -- which MySQL will not index without one (error 1170) -- or a varchar
// past 3072 bytes (error 1071). MariaDB indexes both. The server's own refusal is what an operator
// sees, and M9-T2 owns whatever replaces that.
//
// An identifier longer than 64 characters, holding a NUL byte, or ending in a space is refused here
// rather than by the server mid-migration. The limit is characters, not bytes.
//
// # The history store, and where this engine's premise reaches it
//
// MySQL numbers its causes apart where SQLite gives them one result code: 1146 for a missing table,
// 1054 for a missing column, 1064 for a syntax error, 1062 for a duplicate key. So a failed write is
// classified rather than explained by a second round trip, and a write asks the catalog nothing
// before it runs -- a pre-check would be a lookup on every successful write to answer a question the
// failure already answers, which is M8-T6's own finding reached by deleting the same redundant call.
//
// The catalog is still asked before every read, and absence is deliberately not what it answers.
// Every statement that reaches a missing table fails 1146 and is classified into history.ErrNoTable
// there, so reporting it from the lookup as well would be two places producing one answer. What only
// a lookup can find is a name held by a view: a view is selectable, so a read would answer from one,
// and no error number says so.
//
// A failed write does consult the catalog, once, and only for the cause the number cannot give: 1054
// says a column was unknown and not which one this build needed.
//
// The catalog lookup returns the stored name and compares it in Go, because the collation of
// information_schema.TABLES.TABLE_NAME is not the same on the two servers -- utf8mb3_bin on 8.0.46
// and utf8mb3_general_ci on 11.4.13 -- and a case-insensitive comparison on a server that stores
// names case-sensitively could answer about a different table. Which spelling to ask for comes from
// lower_case_table_names, read at Open: setting 1 lowercases a name on the way in, and asking for
// the wrong spelling means Ensure reports the table absent and tries to create it forever.
//
// A generated column counts as absent, because nothing can INSERT into one. GENERATION_EXPRESSION is
// what says so and EXTRA is not: MySQL 8 sets EXTRA to "DEFAULT_GENERATED" for any column with an
// expression default, so filtering it on "GENERATED" would call five writable columns missing.
//
// applied_at is rendered and parsed by this driver rather than handed to the library. The literal
// form is MySQL's own -- a space rather than a T, no zone -- because an RFC3339 string is error 1292
// on both. Exactly six fractional digits are written, which is what a datetime(6) keeps: given nine,
// MySQL rounds them up and MariaDB truncates them, so the same instant would be recorded as two
// different microseconds on the two engines.
//
// Ensure is re-entrant rather than atomic, and that is this engine's premise reaching the store.
// CREATE TABLE commits itself, so a run interrupted between it and the index leaves a table with no
// index on version and nothing can roll that back. SQLite's Ensure runs its statements as one unit
// and a test there proves a failed one leaves nothing behind; here the honest answer is that Ensure
// repeats what the schema declares after the table and treats "already there" as done.
//
// # This driver's live tests are tagged, and SQLite's are not
//
// ADR 0050's rule is that a build tag names a requirement. SQLite needs a t.TempDir(); MySQL needs a
// daemon, so everything that touches a real server is behind the integration tag and runs in the
// Docker job. What is untagged is everything that needs none: the connection string and its
// refusals, version and variant interpretation from constructed strings, the compiler and its golden
// files, the conformance suite over a declaration built with no server, the value rendering, and the
// pure halves of the history store.
//
// Both servers are in test/docker-compose.yml, and the reason MariaDB is there is the reason two
// PostgreSQL majors are: a driver that detects a variant and only ever meets one of them has not had
// that tested at all.
//
// Status: connection handling, the two methods a migration's own SQL needs, transactions, the
// history store, and the compiler for the two operations that store is built on. Implemented across
// M9, with online DDL in M13-T10 (see TASKS.md).
package mysql
