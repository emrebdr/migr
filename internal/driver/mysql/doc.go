// Package mysql implements the Driver contract for MySQL and MariaDB.
//
// MySQL has no transactional DDL, so a failed migration leaves partial state. This driver is
// therefore the proving ground for the engine's statement-level recovery, dirty-state marking, and
// resume model -- and since M9-T5 it is the only engine that has ever produced that state by failing:
// a three-statement migration stopping at its second records last_statement = 1 on MySQL 8.0 and
// MariaDB 11.4 alike, with the first statement's table still in the catalog. PostgreSQL and SQLite
// both declare transactional DDL, so ADR 0024 could describe this half and never run it.
//
// # The migration lock is a user-level lock, and the name is derived
//
// GET_LOCK takes a lock that is global to the *server* rather than scoped to a database, lives on a
// session, and dies with it. So the name carries the database as well as the history table, or two
// migr installations on one server would exclude each other: it is migr1: and 32 lowercase hex
// characters of a digest over the two (M9-T4, ADR 0060). Readable names were measured and rejected --
// MySQL refuses a name over 64 characters with error 4163 where MariaDB accepts one, and MySQL
// compares names case-insensitively where MariaDB does not, so two 64-character identifiers neither
// fit nor mean one thing. Every failure renders the name, which is what IS_USED_LOCK takes.
//
// The wait is GET_LOCK's own second argument, a decimal number of seconds -- not waitSeconds, which
// renders whole ones for lock_wait_timeout and would turn every sub-second bound into an immediate
// try. Nothing is set and nothing is reset, which is where this differs from PostgreSQL bounding its
// acquire with lock_timeout: giving up is a value the server returns and leaves the session usable.
//
// Three answers, and the third is not a timeout: 1 grants, 0 is the wait giving up, and NULL is
// neither. MariaDB answers NULL for a negative timeout and for a waiter whose query is killed, where
// MySQL waits forever for the first and answers error 1317 for the second -- so no wait this driver
// renders may be negative or zero. A second acquire on one session is refused rather than counted,
// because MySQL counts a recursive GET_LOCK and would owe a second release.
//
// A holder is a connection id plus whatever else this connection may see. IS_USED_LOCK needs no
// privilege; information_schema.PROCESSLIST shows another session's row only to a user with PROCESS,
// and filters it out silently otherwise -- measured, which makes the degraded report the ordinary one
// for a production grant. So the id is always named and the rest is optional, where PostgreSQL's
// holder is one row and no row means no holder.
//
// # What a crashed migr costs, and the one case this engine cannot bound
//
// A user-level lock dies with its session, so there is no stale lock to reclaim and no staleness rule
// to write -- SQLite's file lock owes one and this does not. A crashed migr on a reachable host costs
// the length of the statement that was in flight: the kernel closes the dead process's sockets and the
// server reaps the session as soon as it next touches one. Measured at the CLI on both servers, with
// SIGKILL mid-migration: the lock was still held one second later and free by four, which is when the
// abandoned statement finished.
//
// The case this cannot bound is a client *host* that vanishes without a FIN. The server then blocks
// reading for wait_timeout, 28800 seconds on both images, and MySQL has no per-connection server-side
// keepalive a client can pin -- which is what PostgreSQL's tcp_keepalives_* in the startup packet buys
// (ADR 0020). Shortening wait_timeout for the session was rejected rather than deferred: this
// connection is idle whenever a language runtime is deciding what a migration does, so a short bound
// would disconnect migr mid-run. What a blocked run gets instead is the connection id to KILL.
//
// # Lock impact, and the two things this server says about its own ALTER
//
// Every operation but raw carries a verdict, resolved from the algorithm the detected server applies
// (M9-T3, ADR 0059). INSTANT is BriefExclusive and not LockFree: the statement is metadata-only once
// it holds its lock and it still requests an exclusive metadata lock and still waits for one, so it
// can hold a table hostage for the length of the query ahead of it. Only create_table and a
// create_view that replaces nothing are LockFree, which is PostgreSQL's answer for PostgreSQL's
// reason -- neither touches an object that already exists.
//
// Two gates move a verdict with the release, and both are MySQL's: add_column rewrote the table
// below 8.0.12 and drop_column below 8.0.29, where MariaDB has done both instantly since before this
// driver's floor. rename_column gained INSTANT at 8.0.28 and was INPLACE with no rebuild below it,
// so no gate is written for it -- a gate per release would be a table of release notes rather than a
// declaration.
//
// What decides add_column besides the release is whether this server can *store* the new column's
// default or has to write it into every row. MySQL materializes any parenthesised default, which is
// what isExpressionDefault already names; MariaDB stores all of those and materializes only a value
// that differs per row, which is uuid alone. Measured on both.
//
// alter_column carries two verdicts by payload. Naming neither a type nor a nullability means the
// redefinition restates both from the catalog verbatim and changes only the DEFAULT, which is INSTANT
// on both servers; naming either is a rebuild. A retype is declared a rewrite even where the server
// would widen a varchar in place, because telling a widening from a narrowing needs the column's
// current type and the operation carries that only as `from` -- what an author recorded rather than
// what the column is. The expansion does read the real type, and Resolve performs no I/O, so the
// precise answer lives on a path a verdict cannot reach from here. M13-T6 put the verdict on the
// planner's step (ADR 0086), which is where it could be refined, and left it declared.
//
// drop_table is the one verdict reasoned rather than measured. InnoDB evicts the table's buffer-pool
// pages and unlinks the tablespace under the metadata lock, and the attempt to measure the scaling
// could not separate 13 MB from nothing against the harness baseline -- so the pessimistic verdict
// stands. PostgreSQL's drop_table, measured at M13-T1 to scale with the table, now says the same.
//
// # An instant change depends on the table, so the session confirms it
//
// Whether an instant add_column or drop_column is used is not only the release's call. Measured on
// 8.0.46 and 11.4.13 with the table id read either side, both servers rebuild a COMPRESSED table and
// one carrying a FULLTEXT index, MySQL rebuilds a table at 64 instant row versions and drops an
// indexed column by rebuilding, and none of it warns: ALGORITHM=INSTANT is 1845, 1846 or 4092, and a
// bare ALTER TABLE quietly takes the slower path. A default change and a rename stayed instant on
// every one of those tables.
//
// Resolve cannot see a table, so those two operations are declared table_rewrite lowerable to
// brief_exclusive under the condition the table must meet, and Assess -- this session's
// driver.Assessor -- reads the table and confirms it (ADR 0081). The row format comes from
// information_schema.TABLES and the indexes from STATISTICS, which any user may read; the row-version
// count is in INNODB_TABLES, which refuses a session without PROCESS with 1227. So from 8.0.29 a
// session without that grant never lowers the verdict: the fact it would need is unread, and
// unread is pessimistic. migr never asks for the grant. A table not yet created is assessed at its
// worst, there being nothing to read.
//
// An empty table is rebuilt by MySQL's ADD COLUMN instead of being changed instantly, and a rebuild
// that copies no rows costs nothing. It surfaced as a false rewrite in a measurement whose insert had
// failed, which is why every live probe here asserts its row count.
//
// # A lock wait spent is retried by the engine
//
// lock_wait_timeout bounds a statement's wait for a metadata lock and innodb_lock_wait_timeout its
// wait for a row lock, and both end in error 1205 on both servers, which Exec wraps in
// driver.ErrStatementLockTimeout (M13-T2, ADR 0082). DDL here commits as it runs, so the engine
// continues from the statement that waited rather than sending the ones before it again -- a rerun
// from the start is error 1060, a duplicate column, measured. While it backs off the migration is
// between states with no row saying so, which is the window a crash between two statements has.
//
// # A canceled statement is killed on the server, from a second connection
//
// go-sql-driver answers a canceled context by closing its connection and telling the server nothing:
// measured at M13-T3, MySQL 8.0 left the ALTER waiting for its whole lock_wait_timeout and MariaDB
// 11.4, which polls a waiting client, stopped it about 650ms later -- and on both the session holding
// GET_LOCK was gone. So every round trip a migration drives runs on context.WithoutCancel, BeginTx
// included, and interruptible sends KILL QUERY with this session's CONNECTION_ID() from a second pool
// when the real context ends, returning once the statement's 1317 and the kill are both back. A late
// kill on an idle connection was measured harmless on both servers, a user needs no privilege to kill
// a thread of its own, and an ALTER ... FORCE killed partway left the table whole (ADR 0083).
//
// # A uuid default is refused while this server writes a binary log
//
// ALTER TABLE ... ADD COLUMN ... DEFAULT (UUID()) is error 1674 on MySQL whenever log_bin is on,
// which is 8.0's default: the value is written into every existing row, so a replica replaying the
// DDL would compute different ones. Measured, and narrow in every direction -- refused on an empty
// table and under binlog_format=ROW, accepted by CREATE TABLE and by both ways of setting a default
// afterwards, accepted for CURDATE(), and accepted by MariaDB even with a binary log. So log_bin is
// read once in Open and the refusal names the setting and the two-operation fix (ADR 0059 §G).
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
//     statement-level recovery (M9-T5) both reason against -- a dirty row names one statement index,
//     and a line holding three would make it name the wrong thing;
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
// make a migration of several recoverable. M9-T5 is where the engine's answer was measured: the
// statements that landed before the failure are still there, so the run is recorded dirty and the
// next one is refused with the row it wrote (ADR 0061).
//
// # The compiler
//
// Nineteen of the twenty operations compile with no database. The twentieth is alter_column, which is
// mostly compiled against one; see below. test/golden/mysql holds all twenty and the live tests run
// every compiled statement against both servers, so a golden file here is one the engine accepted
// rather than one that is merely the right shape.
//
// Four spellings are MySQL's own rather than the other drivers'. drop_index names its table, MySQL
// indexes being table-scoped where PostgreSQL's and SQLite's are not. drop_foreign_key is DROP
// FOREIGN KEY. drop_unique is DROP INDEX, a unique constraint being an index here, and that form
// works on every version of both servers where DROP CONSTRAINT arrived only in MySQL 8.0.19. And
// drop_check is DROP CONSTRAINT, because DROP CHECK is error 1064 on MariaDB 11.4.13 -- the reverse
// asymmetry, which is why one spelling is emitted and the releases without it are refused instead of
// Compile trying to choose by variant, which a pure function cannot do.
//
// add_primary_key carries no name. MySQL has one primary key per table and always calls it PRIMARY:
// ADD CONSTRAINT <name> PRIMARY KEY is accepted and the name discarded, measured, so emitting it
// would produce a statement whose name nothing can find again. The IR anticipated this -- Name is
// optional on both key operations, and ir.DropPrimaryKey's own comment says MySQL has none -- so the
// name is dropped rather than refused, and M11-T1's Introspect will report PRIMARY.
//
// set_default is refused, and it is the one referential action that is. Both servers accept the
// clause and neither performs it: the delete is refused with error 1451 as though the action were
// restrict and the child row keeps its value, while MySQL records ON DELETE SET DEFAULT in SHOW
// CREATE TABLE anyway and MariaDB drops the clause entirely. So the catalog is not even agreed on
// what is there. PostgreSQL performs it, so passing this through would give one migration two
// meanings on three engines.
//
// No statement this driver composes sets NoTransaction. It means a statement cannot run inside a
// transaction, and MySQL's DDL can be sent inside one -- it just commits it. Setting the flag would
// make internal/engine split every migration into single-statement parts, which is the dirty-state
// model's to choose rather than a flag the compiler sets. M9-T5 chose: a failure partway records how
// far it got. A raw operation declaring no_transaction is still carried as given, and changes where
// the history rows are written rather than what survives (ADR 0084).
//
// # Character set and collation, which are declared rather than inherited
//
// Every table declares DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin, and so does every column
// whose type accepts one. Both halves are needed and the column half is the load-bearing one: a
// column migr adds to a table migr did not create would otherwise inherit that table's character
// set, which changes what the column can hold, how many bytes wide it is, and whether it fits in a
// key.
//
// Four native types accept the clause -- varchar, longtext, char and enum -- and json, longblob and
// every numeric and temporal type answer error 1064 for it. That list is measured rather than
// reasoned from what looks textual: MariaDB's json is itself a longtext and still refuses it.
//
// utf8mb4_bin is the collation because it is what makes one migration mean one thing on every
// engine. The two servers do not agree on their own default -- utf8mb4_0900_ai_ci on MySQL 8.0.46,
// utf8mb4_uca1400_ai_ci on MariaDB 11.4.13 -- and both are case-insensitive, so under either of them
// a unique constraint on an email column refuses 'Bob@x.com' beside 'bob@x.com' where PostgreSQL and
// SQLite accept both. The same add_unique would declare a different constraint on each engine. The
// cost is the other side of the same coin and is real: equality is case-sensitive here and ORDER BY
// is code-point order, where an operator used to MySQL may expect neither. A project wanting a
// case-insensitive column has ir.Raw.
//
// Pinning the character set is also what makes two length limits decidable with no database. A
// varchar holds at most 16383 characters, which is 65535/4 and which error 1074 states itself; and
// an InnoDB key spans at most 3072 bytes, so at most 768 characters.
//
// # The two key limits, and why the server answers one of them
//
// MySQL refuses to put a text or blob column in a key without a prefix length (error 1170) and
// refuses a key past 3072 bytes (error 1071). No v1 operation carries a prefix length, and
// ir.AddIndex, ir.AddUnique and ir.AddPrimaryKey name columns rather than types -- so a pure Compile
// cannot know that a column is a longtext or how wide a varchar is.
//
// The server's own refusal is therefore what an operator sees for those three, and Exec states the
// rule beside it: what the limit is, that the operation carries no field for it, and that MariaDB
// indexes a longtext without a prefix so the same migration succeeds there. Inventing a prefix was
// rejected. It would make every index Emulated and unchecked by migr validate, and a prefixed
// add_unique is not the constraint the migration declared -- uniqueness on the first 255 bytes
// refuses two distinct values that happen to share a prefix, which is exactly the silent degradation
// a capability model exists to prevent.
//
// create_table is the one operation that does carry its columns' types, so there the same two limits
// are refused with no database open: a primary key over a text or binary column, and one wider than
// 3072 bytes. The first of those is error 1170 on MariaDB as well, measured -- where a plain index
// over a longtext is accepted there -- so that refusal is not MySQL's alone.
//
// # alter_column, which is a redefinition rather than an alteration
//
// MySQL has no statement that changes one attribute of a column. A type or nullability change is
// MODIFY COLUMN, a full redefinition, and every attribute it does not restate is dropped: measured,
// a MODIFY of the type alone lost both the NOT NULL and the DEFAULT on both servers. ir.AlterColumn
// carries only a delta, and its From records only the attributes being changed, so it can never
// supply the rest. The missing attributes live in the database and nowhere else.
//
// So alter_column answers driver.ErrNeedsSession and the session is a driver.Expander, declared
// Emulated (ADR 0053, SQLite's precedent one engine over). internal/engine asks for the expansion
// immediately before the operation runs rather than while compiling the migration (ADR 0071), which
// is what makes a second redefinition of one column restate what the first one wrote: measured on
// both servers, two in one migration land as varchar(128) NOT NULL where compiling both up front
// restated the nullability the first had just changed. Exactly one payload is exempt:
// DropDefault alone compiles to ALTER COLUMN ... DROP DEFAULT with no database, because dropping a
// default names no value and so needs no type -- accepted on every native type this driver emits,
// including a column that had no default to drop. That makes one ir.Kind carry two verdicts by
// payload, which is the case where a capability Rule receiving the whole operation is load-bearing
// rather than merely available.
//
// Setting a default is not exempt, and that measurement is what narrowed the exemption to one case:
// ALTER COLUMN ... SET DEFAULT is error 1101 on a text, blob or json column on MySQL for the bare
// form and the parenthesised one alike, and a bare function default is error 1064 there. Only MODIFY
// COLUMN can set a default on such a column, and which form is legal is a fact about the column's
// type rather than about the default.
//
// What the expansion keeps is the server's own text. COLUMN_TYPE is restated verbatim where the
// operation does not change the type, which is faithful by construction and is why an enum survives
// -- its labels are part of its type and nothing re-derives them. MariaDB renders int(11) where MySQL
// renders int, and restating each server's own rendering cannot be wrong about it.
//
// What it refuses is anything a redefinition would silently lose, and those refusals are the point
// of it rather than its edges:
//
// A default the operation does not replace. information_schema.COLUMN_DEFAULT does not round trip:
// MySQL reports a string default as the raw value where MariaDB reports a quoted literal, DEFAULT
// NULL is SQL null on one and the four characters NULL on the other -- which on MySQL is instead a
// column defaulting to the string NULL -- and MySQL renders an expression default with backslash
// escapes, a grammar this session pins off. So only the presence of a default is read, and the
// refusal names the fix: set default on the operation, which makes the migration state what it should
// be. That is more correct than any inference from a field that does not round trip.
//
// Anything in EXTRA. It is empty for an ordinary column and carries auto_increment, an ON UPDATE
// clause, and VIRTUAL or STORED GENERATED, each of which a redefinition restates nothing of. The
// check fails closed: an EXTRA string this build has never seen is refused rather than discarded.
// DEFAULT_GENERATED is stripped first, MySQL setting it for any column with an expression default --
// the same reasoning as the history store's GENERATION_EXPRESSION filter, one field over.
//
// A collation this driver did not set. A redefinition must restate one, and restating this driver's
// would change what equality and uniqueness mean on a column the operation only asked to retype.
//
// A check constraint MariaDB records under the column's own name. MariaDB writes a column's check
// inside the column definition, so a MODIFY drops it -- measured, a json column's json_valid check
// was gone afterwards and invalid JSON was accepted, and a hand-written CHECK (n > 0) went the same
// way and took -1. MariaDB implements json as a longtext carrying that check, so COLUMN_TYPE says
// nothing about it and the constraint's name is the only evidence. MySQL holds a check apart from the
// column, names it after the table, and a MODIFY keeps it -- error 3819 still refused -1 after one --
// so this is asked of MariaDB alone, which is also the only variant whose CHECK_CONSTRAINTS has a
// TABLE_NAME column at all.
//
// # What the type map decides, each with a cost worth stating
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
// An identifier longer than 64 characters, holding a NUL byte, or ending in a space is refused here
// rather than by the server mid-migration. The limit is characters, not bytes.
//
// Three version gates keyed to the server rather than assumed, beside the expression-default one
// above. A check constraint is refused below MySQL 8.0.16, where the syntax is accepted and the
// constraint does not exist -- a migration reporting success over nothing, which is worse than a
// refusal. Dropping one is refused below 8.0.19, where DROP CONSTRAINT arrived. And renaming a column
// is refused below MariaDB 10.5.2, which is inside this driver's range and has only CHANGE COLUMN --
// itself a full redefinition. Neither container can reach any of those, so each is tested against a
// constructed version, which is ADR 0052's reading of invariant #13: the version is asked for and
// never hardcoded, and a branch no available server produces is still written and held to a test.
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
// that tested at all. CI runs every tagged package against all four from one job, with no
// strategy.matrix over the engines: each driver holds its own table-driven server list, so one
// invocation of make test-integration already covers them and a per-engine matrix would repeat the
// whole suite four times for the same assertions.
//
// # The round trip, and what the server's own printer is for
//
// M9-T2 ran every compiled statement against both servers. What M9-T7 adds is the other direction:
// every operation but raw is applied, reversed through ir.Reverse, and the schema held to what it
// was -- plus the whole build undone through ir.ReverseAll, the teardown rebuilt from its from
// payloads alone, and raw held to an authored undo. That makes MySQL the first engine whose emulated
// reverse has run at all, alter_column being compiled against the database here and nowhere else.
//
// The comparison is SHOW CREATE TABLE and SHOW CREATE VIEW, which is this engine's
// pg_get_constraintdef: one answer carrying the columns, the keys, the foreign keys with their
// actions, the checks, and the table's own character set and collation -- what PostgreSQL's five
// information_schema probes have to be reassembled from. Its lines are sorted, and that is the
// load-bearing part: a reversed drop_column appends the column and no v1 operation can put it back
// where it was, MySQL's AFTER clause having no field in the IR, so the digest compares what a schema
// holds and never the order it holds it in. MySQL needs no probe for a relation with nothing in it,
// where PostgreSQL does: it refuses to drop a table's last column outright (error 1090).
//
// Two reversals do not restore what they changed, and neither is a compiler defect:
//
// A foreign key over an unindexed column. MySQL requires an index on a referencing column and makes
// one named after the constraint when it finds none, and DROP FOREIGN KEY then leaves that index
// behind -- measured on both servers. The IR has no field recording that an index was ever created,
// so nothing derives its removal. Given an index of its own the server reuses it and drops nothing,
// which is why the fixture indexes the column before declaring the key.
//
// The nullability ADD PRIMARY KEY implied. It converts a nullable column to NOT NULL and DROP
// PRIMARY KEY leaves it that way, on both servers -- PostgreSQL's gap exactly, reached from the same
// direction. A name says nothing about what the constraint implied, so a wider from payload would
// not help either. That one closed with M11-T2: engine.Unrestorable reads the live schema and
// refuses an add_primary_key over a nullable column before it runs.
//
// The foreign key's own index did not, and that is deliberate rather than unfinished. It is residue
// this server adds rather than a definition the operation set cannot record, so refusing it would
// be an engine difference inside the engine, which CLAUDE.md §11 puts in the capability system.
// Introspection suppresses it instead: an index named after a foreign key over that key's own
// columns is not reported, because no other engine creates one and reporting it would make two
// equivalent schemas compare unequal.
//
// The dirty state is proved at the engine rather than at the CLI, and a CLI-level version would buy
// nothing: engine_integration_test.go drives engine.Apply, engine.Rollback and engine.Resolve over a
// real session on both servers and reads the row the stopped run wrote, where what internal/cli adds
// above that -- config, discovery, source digests, rendering, exit codes -- names no engine and is
// covered by cmd/migr's fake-driver suite and its PostgreSQL and SQLite legs. Paying for it would
// mean a third driver child package and the paired depguard rule sets one owes.
//
// Status: connection handling, the two methods a migration's own SQL needs, transactions, the
// history store, the compiler for the two operations that store is built on, and M11-T2's
// Introspect. Implemented across M9, with online DDL in M13-T10 (see TASKS.md).
//
// # Reading a snapshot back, on two variants
//
// Three catalog columns exist on one variant and not the other, each error 1054 on the wrong one:
// CHECK_CONSTRAINTS.TABLE_NAME and TABLE_CONSTRAINTS.ENFORCED are MySQL's, and MariaDB has neither.
// So the check probe has two spellings and nothing else does.
//
// Two readings are this engine's own. MariaDB appends a display width where MySQL 8.0 strips one
// server-side, so int(11) normalizes to int -- which is what keeps an int32 column comparable
// between the two; tinyint(1) is the one width that survives, being how both spell a boolean. And
// MariaDB has no json type: a json column is a longtext plus a check named after the column whose
// clause is exactly json_valid(`col`), so absorbing that check is what tells its json from its text.
//
// The sharpest difference is a referential action. MariaDB answers RESTRICT for an omitted clause
// and its own SHOW CREATE drops an explicit one, so the distinction is not in the catalog -- this
// driver reads RESTRICT as no_action there and as restrict on MySQL, which is what lets a key with
// no actions declared compare equal across all four servers. The index this server creates for a
// foreign key over an unindexed column is suppressed, no other engine creating one. See
// docs/adr/0069-per-driver-introspection.md.
//
// # Replica lag is read on each replica
//
// MySQL and MariaDB report lag only on the replica, so ReplicaLag opens each databases.<name>.replicas
// connection string on a pool of its own, lazily and under a five-second bound, and asks SHOW REPLICA
// STATUS -- SHOW SLAVE STATUS before MySQL 8.0.22 and MariaDB 10.5.1 -- reading the row under either
// spelling of its columns, since MariaDB keeps the old ones under the new statement (M13-T8, ADR
// 0088). A stopped IO or SQL thread, a NULL lag, no row, error 1227 and an unreachable replica are
// each unknown with a reason of their own; a replica is named by host:port, never by its DSN.
//
// # Whether this server permits log-based capture
//
// Open reads log_bin, binlog_format and binlog_row_image, and the session's own global privileges
// from information_schema.USER_PRIVILEGES -- never SHOW GRANTS, which prints a password hash on
// MariaDB -- into capability.Facts.Capture (M13-T16, ADR 0090). REPLICATION CLIENT is accepted under
// MariaDB's 10.5 name, BINLOG MONITOR, too. A failed read is read as not permitted. MySQL alone also
// needs binlog_row_value_options empty, since PARTIAL_JSON sends half an image; MariaDB alone also needs
// REPLICATION MASTER ADMIN, which its SHOW SLAVE HOSTS asks for (M13-T17). Both say how long they keep
// their log -- binlog_expire_logs_seconds -- as the fact's Retention.
//
// # A shadow table, kept current from the binary log
//
// Capture reads one table's row events through go-mysql (M13-T10, ADR 0091), on a pool and a
// replication connection of its own, under a server id derived from the capture's name and moved past
// every id the server and its replicas hold. A transaction is handed over once its commit is read and
// the position moves only then: gtid:<set> where the server records GTIDs, file:<name>:<offset> where
// it does not. An ALTER, RENAME, DROP or TRUNCATE of the table stops it naming the statement, and a
// purged position names binlog_expire_logs_seconds. Each value is rendered from the decoded event into
// the text the server answers with -- an unsigned integer un-signed, an enum's label, a float as the
// exact double it is, a timestamp in UTC -- and bound back by its column's class.
//
// shadow.go is the Shadower. Admits refuses MariaDB before 11.4, MySQL before 8.0.13, foreign keys
// either way, triggers, user-named checks on MySQL, a system-versioned table on MariaDB and a type no
// replay renders. The shadow is CREATE TABLE ... LIKE, its
// changes compiled against it, an alter_column expanded from the original's column. A chunk is INSERT
// ... SELECT ... FOR SHARE ON DUPLICATE KEY UPDATE k = k, which keeps a replayed row, downgrades no
// other error the way INSERT IGNORE would, and waits out a writer whose event is readable before its
// commit is visible. A replay is REPLACE and DELETE by key. The cutover is LOCK TABLES on the table,
// the shadow and the shadow state, the catch-up bounded by safety.lock_timeout, AUTO_INCREMENT carried
// over, RENAME TABLE of both and UNLOCK TABLES -- measured, a writer queued on the lock lands in the
// new table. MySQL holds no server resource for a reader, so retention is what a detached hold rests
// on, and migr plan prints it.
//
// # The same on MariaDB
//
// MariaDB 11.4 runs it too (M13-T17, ADR 0095), with four differences, each measured on 11.4.13. Its
// log is read in go-mysql's MariaDB flavor: a group opens with its own GTID event and no BEGIN, and a
// standalone group is one statement that commits itself. Its position is gtid:<domain>-<server>-<seq>
// from @@gtid_binlog_pos, which every server writing a binary log has -- file:<name>:<offset> only
// where it has binlogged nothing yet -- and a set the other variant recorded is refused naming it. Its
// copy reads LOCK IN SHARE MODE, since FOR SHARE is a syntax error there, and waits out an in-flight
// delete the same way. CREATE TABLE ... LIKE keeps every check's name, so none is refused.
//
// Its cutover is not MySQL's, because RENAME TABLE under LOCK TABLES is error 1192 and an ALTER TABLE
// ... RENAME under the lock failed every queued writer with 1146. shadow_mariadb.go says how it works
// instead: a holder locks the table, the session catches up and commits, a renamer queues RENAME TABLE,
// a prober confirms the renamer holds every name sorting before the table's, and the holder unlocks --
// the pending exclusive request is granted first and the queued writers land in the new table.
package mysql
