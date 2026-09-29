# 0057 — The MySQL session: what a read-only transaction does not refuse

Status: Accepted
Date: 2026-09-29
Task: M9-T1

## Context

`internal/driver/mysql` held a `doc.go` and nothing else. M9-T1 builds the session: the connection
string, the pool, the version, the two methods M6-T3 added to `Conn`, transactions, and — because
invariant #3 makes the history table driver-compiled IR — the history store and the two operations
it is created through.

MySQL is the third engine, and the shape is settled by `internal/driver/postgres` (M2-T1) and
`internal/driver/sqlite` (M8-T1). What follows is only where this engine forced a different answer.

Nine measurements against `mysql:8.0.46` and `mariadb:11.4.13` preceded the code. Four of them
changed the design, and two of those overturned what the plan said.

## Decision

### A. A read-only transaction is not read-only, because DDL commits out of it

The task text says `Query` is enforced read-only "by the **server** through `START TRANSACTION READ
ONLY`". That is necessary and not sufficient.

Measured, on both servers: under `START TRANSACTION READ ONLY`, an `INSERT`, an `UPDATE`, a `DELETE`
and a `REPLACE` are refused with error 1792 — and `CREATE TABLE`, `ALTER TABLE`, `CREATE INDEX`,
`TRUNCATE` and `DROP TABLE` all **succeed**, and survive the rollback. Each of them implicitly
commits, which ends the read-only transaction before the server ever checks its access mode.

So a migration could have created, altered, truncated or dropped a table through `host.db.query` —
the callback whose whole contract is that it reads.

`SET SESSION transaction_read_only = ON` closes it, and was measured doing so: under it, a
`CREATE TABLE` sent with autocommit on is refused 1792, because the statement's own implicit
transaction is read-only too. `Query` therefore pins the session variable *and* opens the read-only
transaction, and puts the session back on a context that outlives whatever canceled the read —
SQLite's `query_only` shape, arrived at from the opposite direction.

**Rejected:** the transaction alone, which is what the task asked for and what PostgreSQL needs.
**Rejected:** a host-side check on the leading keyword, for ADR 0032's original reason — it is the
thing the server enforcement exists to replace, and `SELECT ... FOR UPDATE` is exactly why.

### B. The fixture is `SELECT ... FOR UPDATE`

ADR 0032 B requires each engine to name a statement that reads like one and writes, because that is
what a leading-keyword check would wave through. PostgreSQL's is a data-modifying CTE; SQLite's is
`PRAGMA user_version = 5`. MySQL has no data-modifying CTE.

`SELECT ... FOR UPDATE` is this engine's: it begins with `SELECT`, and it takes exclusive row locks
that block every writer on those rows. Refused 1792 / 25006 on both servers.

A `SELECT` calling a writing stored function was the other candidate and is refused on MariaDB —
but MySQL would not let the function be created at all under its default `log_bin_trust_function_creators`,
so it is not a portable fixture. The DDL cases above are in the same test, because they are what the
session variable is for.

### C. `Perform` reports the server's own count

MySQL's affected-rows counts rows **changed**; PostgreSQL's command tag counts rows **matched**. A
no-op `UPDATE` is 0 here and 1 there — measured. `clientFoundRows` flips MySQL to matched and would
make the two agree.

It is **off**, and refused in the connection string. The rule this project already has is that every
value comes back as the server's own, and this number is one of them. The counter-argument is real
and is M8-T1's: a blob is rendered `\x…` on every engine precisely so one migration gets one answer.
The distinction taken here is that a blob's rendering is migr's, where an affected-rows count is the
server's — and `clientFoundRows` also changes what the migration's own `SELECT ROW_COUNT()` returns,
which migr would then be silently rewriting.

`doc.go` states the difference rather than hiding it.

### D. `NO_BACKSLASH_ESCAPES` is pinned, and it costs something

`Compile` is pure by contract, so it cannot read a session setting to know the string literal
grammar. PostgreSQL's driver pins `standard_conforming_strings` for exactly this. MySQL's mirror is
`NO_BACKSLASH_ESCAPES`, which **both servers leave off by default**.

Pinning it on makes doubling the quote the whole defense, identically to PostgreSQL. The cost is
that it changes what a migration's own inline literals mean: measured, `'a\nb'` is four characters
with it and three without, and `DEFAULT 'a\b'` stores two characters with it and one control
character without.

**Rejected:** pinning it off (MySQL's default) and having M9-T2 escape backslashes as well. That is
the lower-surprise option for a migration's own SQL and was seriously considered; it was rejected
because it makes the compiler's escaping engine-specific where it does not need to be, and because
either choice is an override of whatever the operator's server had. The cost is documented rather
than traded away.

Only that one mode is touched. The rest of `sql_mode` — the operator's strictness settings — is left
alone, which is why `withMode` adds rather than replaces and why the read-back asks whether the mode
is in the list: the server reorders `sql_mode` when it takes it, measured on both.

### E. A numeric session variable takes no bound parameter

`SET SESSION lock_wait_timeout = ?` is error 1232 and `SET SESSION transaction_read_only = ?` is
1231: a bound parameter reaches the server as a string, and a numeric or boolean system variable
will not take one. `time_zone` and `sql_mode`, being string-valued, do.

So the numeric ones are written into the statement. `checkDigits` refuses a value that is not a
number before it is written, which keeps that structurally safe rather than safe because today's
callers happen to pass package constants.

Every owned variable is **read back**, because MySQL clamps out of range and reports success:
`lock_wait_timeout = 0` silently becomes 1 on MySQL (and stays 0 on MariaDB), and a value past a
year becomes 31536000. Both measured. That is why `safety.lock_timeout` under one second is refused
at construction, naming the key, rather than rounded.

### F. The variant is read from two witnesses, and the floor is per variant

MariaDB prepends `5.5.5-` to the version it announces so that clients too old to read a two-digit
major do not misparse 10.x, and some builds carry that into `VERSION()`. A driver reading the first
number alone takes MariaDB 11.4 for MySQL 5.5 — below its own floor — and refuses to run.

`VERSION()` and `@@version_comment` are both read; either can name MariaDB and the prefix is
stripped explicitly. Anything that names neither is MySQL, which is what Percona is.

The floor is MySQL 8.0 and MariaDB 10.5, and it has to be per variant: the two version lines share
no scale, so a single floor of 8.0 would accept MariaDB 10.4 and a single floor of 10.5 would refuse
MySQL 8.0. A version this driver cannot compare is refused rather than assumed (invariant #13).

`ServerVersion()` carries the variant — `"MariaDB 11.4.13"` — because `database.driver` says `mysql`
for both and `host.info`'s `server` field is the only thing a migration can tell them apart by.

### G. The history store is built here, and `Ensure` is re-entrant rather than atomic

M9's task list has no history-store task, and M9-T5 cannot record a last-statement index without
one. It is built here, which pulls **two operations of M9-T2 forward** — `create_table` and
`add_index`, the two the history table is created through under invariant #3 — along with the
identifier quoting, the logical type map and the default rendering they need. The other eighteen
refuse naming M9-T2, and `test/golden/mysql/` holds all twenty.

`Ensure` cannot be atomic. `CREATE TABLE` commits itself, so a run interrupted between it and the
index leaves a table with no index on `version`, and nothing can roll that back. SQLite's `Ensure`
runs its statements as one unit and a test there asserts a failed one leaves nothing behind; the
honest answer here is that `Ensure` repeats what the schema declares after the table and treats
"already there" as done. A test drives exactly that.

### H. MySQL numbers its causes apart, so a write is classified — and the catalog is still asked

M8-T6 asks the catalog before deciding anything because SQLite gives a missing table, a missing
column and a syntax error one result code. MySQL does not: 1146, 1054 and 1064, measured on both
servers, along with 1062 for a duplicate key and 1050 for a table that already exists.

So a failed write is classified rather than explained by a second round trip. The catalog is still
asked before every path, because **a name held by a view is selectable and no error number says so**
— which is the one thing classification cannot cover.

Two details of that lookup are measurements rather than choices:

- the stored name comes back and is compared **in Go**, because
  `information_schema.TABLES.TABLE_NAME` is `utf8mb3_bin` on 8.0.46 and `utf8mb3_general_ci` on
  11.4.13, and a case-insensitive comparison on a server that stores names case-sensitively could
  answer about a different table;
- which spelling to ask for comes from `lower_case_table_names`, read at `Open`. Setting 1
  lowercases a name on the way in, and asking for the wrong spelling means `Ensure` reports the
  table absent and tries to create it on every run, forever. That is SQLite's `COLLATE NOCASE`
  finding in a third dialect, arrived at from the opposite direction.

A generated column counts as absent, since nothing can `INSERT` into one — and
`GENERATION_EXPRESSION` is what says so where `EXTRA` cannot: MySQL 8 sets `EXTRA` to
`DEFAULT_GENERATED` for **any** column with an expression default, so filtering it on `GENERATED`
would call five writable columns missing.

### I. `applied_at` is rendered here, in MySQL's own literal form

An RFC3339 string is error 1292 on both servers, so the format is MySQL's: a space rather than a
`T`, and no zone. Exactly six fractional digits are written, which is what a `datetime(6)` keeps —
and rendering them here rather than handing the server more is what makes the two engines agree:
given nine digits **MySQL rounds them up and MariaDB truncates them**, so the same instant would be
recorded as two different microseconds.

`timestamptz` maps to `datetime(6)` rather than `timestamp(6)`. MySQL's one zone-aware type spans
1970 to 2038 and so cannot carry a column migr writes forever; `DATETIME` has the range and no zone
semantics, and the session's `time_zone` is pinned to `+00:00` so the two are the same thing for
anything this driver writes. The cost is that `timestamp` and `timestamptz` map to one native type,
which M11-T1's `Introspect` will not be able to tell apart.

### J. The column is what says a value is bytes

MySQL hands text and bytes to a client as the same Go type: everything but an integer and a double
arrives as `[]byte`, measured across twenty column types. Unlike SQLite — which sends a storage class
per value — the value itself cannot say which it is, so `ColumnTypes` is read and the library's own
`BLOB`/`TEXT` and `BINARY`/`CHAR` distinction, which it derives from the field's character set, is
what decides.

The UTF-8 check that follows is a **forward-compatibility guard rather than a routine one**, and the
measurement is what established that: the server transcodes every text column to the connection's
utf8mb4 on the way out, so a latin1 `0xFF` arrives as the two bytes of U+00FF and an invalid byte
cast to `CHAR` is replaced by the server itself. What is left for it is a field type this build of
the library cannot name — it returns an empty type name — whose bytes would otherwise reach
`encoding/json` and come back with each invalid byte replaced by U+FFFD. A unit test drives that
case directly, since no server can produce it.

`character_set_connection` is therefore **checked and never set**: it must be utf8mb4, and the
refusal names the connection-string parameter that fixes it. A character set is the operator's
statement about their server.

### K. Both servers are in compose, for the reason two PostgreSQL majors are

A driver that detects a variant and only ever meets one of them has not had that tested at all.
`mysql:8.0` and `mariadb:11.4` join `test/docker-compose.yml`, which is the half of M9-T7 this task
needed. Every live test runs against both from one table-driven list, and the differences that
surfaced — the clamp of `lock_wait_timeout = 0`, the rounding of a ninth fractional digit, the
`information_schema` collation, the stored-function restriction, MariaDB indexing a `longtext`
without a prefix — are all in that list rather than in prose.

## Consequences

`migr config` resolves a `driver: mysql` project, `migr validate` reports the M9-T2 refusal per
operation at exit 2, and `migr status` opens a real session against either engine and prints ADR
0019's notice. `migr up` names M9-T4, the lock being the one half still missing — the state M8-T1
left SQLite in.

`TransactionalDDL` is `false` from this commit, which makes `host.info` and `engine.DirtyError`
correct before M9-T5 builds the model they describe.

M9-T2 is now a diff of eighteen operations rather than twenty, and it inherits the type map, the
identifier rules and the default rendering. What it still owns: the remaining operations, charset and
collation at the table and column level, and whatever replaces the server's own refusal of an index
over a `longtext` — which a pure `Compile` cannot avoid, `ir.AddIndex` naming columns and not types.

## Rejected alternatives

- **A keyword check in front of `Query`.** ADR 0032 settled this and `SELECT ... FOR UPDATE` is the
  case that settles it again.
- **Pinning `clientFoundRows` on** so the two engines report the same number. It would rewrite the
  migration's own `ROW_COUNT()` as well.
- **Refusing `charset` and `collation` in the connection string.** The library keeps the charset in
  an unexported field, so detecting it would have meant re-parsing the connection string this driver
  had already handed to the library. Checking the resulting `character_set_connection` is both
  simpler and stricter.
- **Mapping `timestamptz` to `timestamp(6)`**, which is semantically closer and stops working in
  2038.
- **Deferring the history store to M9-T5.** The store would then have arrived inside a task about
  recovery, and `migr status` would have had nothing to read until then.
