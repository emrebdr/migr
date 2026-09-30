# 0058 — Compiling MySQL

Status: accepted · M9-T2 · supersedes nothing · builds on
[0057](0057-the-mysql-session.md), [0053](0053-sqlite-emulation.md),
[0012](0012-capability-resolution.md)

## Context

M9-T1 compiled two operations because invariant #3 makes the history table driver-compiled IR, and
refused the other eighteen naming this task. What was left was those eighteen, plus two things the
done-when named and one the task handed over explicitly: the column-length limits on indexed
strings, the character set and collation defaults, and the prefix-length rule — *"either this
becomes an `ErrNeedsSession` operation the way SQLite's rebuilds are, or the server's own refusal
stays what an operator sees; that is this task's call."*

Twelve measurements against MySQL 8.0.46 and MariaDB 11.4.13 came first, and four of them overturned
the plan. Every fact below with a number attached was measured on both servers.

## Decision

### A. The prefix-length rule: the server refuses, and the driver explains

MySQL will not put a text or blob column in a key without a prefix length (1170) and will not index a
key past 3072 bytes (1071). `ir.AddIndex`, `ir.AddUnique` and `ir.AddPrimaryKey` name **columns, not
types**, and `Compile` is pure — so nothing here can know that a column is a `longtext` or how wide a
`varchar` is.

The server's own refusal is therefore what an operator sees for those three, and `Exec` classifies
1170 and 1071 into an `IndexKeyError` that states the rule, says no v1 operation carries a prefix
length, and says **MariaDB indexes a `longtext` without one** so the same migration succeeds there.
The server's own message is carried rather than paraphrased, because that is the wording an operator
searches for.

An `ErrNeedsSession` expansion was rejected, and not on cost. The IR has no prefix-length field, so
the driver would have to *invent* one — and a prefixed `add_unique` is not the constraint the
migration declared: uniqueness over the first 255 bytes refuses two distinct values that happen to
share a prefix. That is the silent degradation §4.2 forbids, and it would be bought by making
**every** index `Emulated` and every index `unchecked` under `migr validate`.

**But `create_table` carries its columns' types**, so there the same two limits are refused with no
database open: a primary key over a `text` or `binary` column, and one wider than 3072 bytes. The
first is 1170 on **MariaDB as well** — where a plain index over a `longtext` is accepted — so that
refusal is not MySQL's alone. The pure half covers what is knowable and the server's half covers what
genuinely is not.

### B. `utf8mb4_bin`, declared on the table and on every column that accepts one

The two servers do not agree on their own default collation — `utf8mb4_0900_ai_ci` on MySQL 8.0.46,
`utf8mb4_uca1400_ai_ci` on MariaDB 11.4.13 — and both are case-insensitive. Under either, a unique
constraint on an email column refuses `Bob@x.com` beside `bob@x.com`, where PostgreSQL and SQLite
accept both. One `add_unique` would declare a different constraint on each of three engines, which is
the framework's central claim failing in the one place it is most load-bearing.

So the collation is pinned to `utf8mb4_bin`, which exists on both. **The cost is real and is
documented rather than hidden:** equality becomes case-sensitive and `ORDER BY` becomes code-point
order, where an operator used to MySQL may expect neither. A project wanting a case-insensitive
column has `ir.Raw`.

Four native types accept the clause — `varchar`, `longtext`, `char`, `enum` — and `json`, `longblob`
and every numeric and temporal type answer **1064** for it. That is a list of what MySQL accepts
rather than a guess at what is textual: MariaDB's `json` *is* a `longtext` and still refuses it.

Both the table default and the per-column clause are emitted, and **the per-column half is the
load-bearing one**: a column migr adds to a table migr did not create would otherwise inherit that
table's character set, changing what it can hold, its byte width, and whether it fits in a key.

Pinning it is also what makes two limits decidable with no database: a `varchar` holds at most
**16383** characters (65535/4, and error 1074 states the number itself), and a key spans at most 768.

### C. `alter_column` is a redefinition, and only `DropDefault` escapes it

MySQL has no statement that changes one attribute of a column. A type or nullability change is
`MODIFY COLUMN`, a full redefinition, and **every attribute it does not restate is dropped**:
measured, a `MODIFY COLUMN n bigint` over `int NOT NULL DEFAULT 7` left `bigint DEFAULT NULL` on both
servers. `ir.AlterColumn` carries only a delta, and its `From` records only the attributes being
changed, so it can never supply the rest.

So `alter_column` answers `driver.ErrNeedsSession`, the session is a `driver.Expander`, and the
verdict is `Emulated("column redefinition")` — ADR 0053's machinery, one engine over, for a different
reason: SQLite needs the database to build a *shadow table*, MySQL needs it to know what the column
already is.

Exactly one payload escapes. **`DropDefault` alone** compiles purely to `ALTER COLUMN … DROP DEFAULT`,
because dropping a default names no value and so needs no type; it is accepted on every native type
this driver emits, including a column that had no default to drop. That makes one `ir.Kind` carry two
verdicts by payload, which is where *"a `Rule` that receives the whole operation"* stops being merely
available and becomes load-bearing — a `map[ir.Kind]Verdict` could not express it and would have to
call the whole kind emulated.

**Setting a default does not escape, and that measurement is what narrowed the exemption to one
case.** The plan had every default-only change pure. `ALTER COLUMN … SET DEFAULT` is **1101** on a
text, blob or json column on MySQL — for the bare form *and* the parenthesised one — and a bare
function default is 1064 there. Only `MODIFY COLUMN` can set a default on such a column, and which
form is legal is a fact about the column's type rather than about the default.

#### What the expansion keeps, and what it refuses

It restates `COLUMN_TYPE` **verbatim** where the operation does not change the type. That is faithful
by construction, and it is why an `enum` survives: its labels are part of its type and nothing
re-derives them. MariaDB renders `int(11)` where MySQL renders `int`, and restating each server's own
text cannot be wrong about it.

It refuses, rather than guesses, in four cases — and those refusals are the point of the expansion
rather than its edges:

- **A default the operation does not replace.** `information_schema.COLUMN_DEFAULT` does not round
  trip: MySQL reports a string default as the raw value (`it's`) where MariaDB reports a quoted
  literal (`'it''s'`); `DEFAULT NULL` is SQL null on one and the four characters `NULL` on the other,
  which on MySQL is instead a column defaulting to the *string* `NULL`; and MySQL renders an
  expression default with **backslash escapes** (`_utf8mb4\'x\'`), a grammar ADR 0057 pins **off**. So
  only presence is read — per variant, failing closed on anything unrecognized — and the refusal
  **names the fix**: set `default` on the operation. That is more correct than any inference, because
  the migration then states what the default should be.
- **Anything in `EXTRA`.** It is empty for an ordinary column and carries `auto_increment`, an
  `on update` clause, and `VIRTUAL`/`STORED GENERATED`. The check fails closed: a string this build
  has never seen is refused. `DEFAULT_GENERATED` is stripped first — MySQL sets it for any column with
  an expression default, so refusing a non-empty `EXTRA` outright would refuse every column this
  driver gave one to. Same reasoning as the history store's `GENERATION_EXPRESSION` filter, one field
  over.
- **A collation this driver did not set.** A redefinition must restate one, and restating this
  driver's would change what equality and uniqueness mean on a column the operation only asked to
  retype.
- **A check MariaDB records under the column's own name.** MariaDB writes a column's check inside the
  column definition, so a `MODIFY` drops it: measured, a `json` column's `json_valid` check was gone
  afterwards and `'not json'` was accepted, and a hand-written `CHECK (n > 0)` went the same way and
  took `-1`. MariaDB implements `json` as a `longtext` carrying that check, so `COLUMN_TYPE` says
  nothing about it and the constraint's name is the only evidence. **MySQL keeps it** — the check is
  table-level, named after the table, and error 3819 still refused `-1` after a `MODIFY` — so this is
  asked of MariaDB alone, which is also the only variant whose `CHECK_CONSTRAINTS` has a `TABLE_NAME`
  column at all.

### D. `set_default` is refused, because both servers accept it and neither performs it

The plan said InnoDB rejects the DDL. It does not. Measured: both servers accept
`ON DELETE SET DEFAULT`; **MySQL records it in `SHOW CREATE TABLE`** so it looks present; **MariaDB
drops the clause entirely** and records a bare `FOREIGN KEY`; and **both then behave as `RESTRICT`** —
the delete fails 1451 and the child row keeps its value. PostgreSQL performs it.

So a migration declaring it gets three outcomes on three engines, two silently wrong, and one lying
in its own catalog. It is refused at compile with the measurement in the message, because the generic
*"this driver has no MySQL spelling for it"* would tell an operator nothing about valid SQL that
simply does not work.

### E. Four spellings are MySQL's own, and one of them cannot be chosen by variant

`drop_index` names its table, MySQL's indexes being table-scoped. `drop_foreign_key` is `DROP FOREIGN
KEY`. `drop_unique` is `DROP INDEX`, a unique constraint being an index here, and that form works on
every version of both servers.

`drop_check` is `DROP CONSTRAINT`, and it is the interesting one: **`DROP CHECK` is 1064 on MariaDB
11.4.13**, so it is not portable, while `DROP CONSTRAINT` arrived in MySQL only at **8.0.19**. A pure
`Compile` cannot choose a spelling by variant. So one spelling is emitted and the releases without it
are refused by the capability set instead — which means a migration can add a check on MySQL 8.0.16
and not undo it there. That is stated rather than hidden.

### F. A primary key carries no name, because MySQL has no other name for one

`ADD CONSTRAINT <name> PRIMARY KEY` is accepted and the name **discarded** on both servers — the key
is `PRIMARY`. The IR anticipated exactly this: `Name` is optional on both key operations and
`ir.DropPrimaryKey`'s own comment says *"The name is optional: MySQL has none."* So the name is
dropped rather than refused, `drop_primary_key` needs none, and M11-T1's `Introspect` will report
`PRIMARY`. Emitting it would produce a statement whose name nothing can find again.

### G. Three version gates, and the M9-T3 boundary

`checkVersion` gains three support-axis gates beside ADR 0057's expression-default one:

| Refused below | Because |
|---|---|
| `add_check` — MySQL 8.0.16 | below it MySQL **parses a `CHECK` and does not enforce it**, so the migration reports success over a constraint that does not exist |
| `drop_check` — MySQL 8.0.19 | where `DROP CONSTRAINT` arrived; see E |
| `rename_column` — MariaDB 10.5.2 | 10.5.0 and 10.5.1 are inside the supported range and have only `CHANGE COLUMN`, itself a full redefinition |

These are here rather than in M9-T3 because emitting SQL a server silently discards is a
compile-level lie, and because `verdict()` already derives `Unsupported` from `checkVersion`. **M9-T3
keeps the lock-impact axis** — `ALGORITHM` resolution — and the declaration write-up. Neither
container can reach any gate, so each is tested against a constructed version: ADR 0052's reading of
invariant #13, where the version is asked for and never hardcoded and a branch no available server
produces is still written and held to a test.

### H. `NoTransaction` stays false on every statement

It means *this statement cannot run inside a transaction*, and MySQL's DDL can be sent inside one —
it just commits it. Setting the flag would make `internal/engine` split every migration into
single-statement groups, which is **M9-T5's** model to choose rather than a flag the compiler sets.

## Consequences

`migr validate` against a `.sql` MySQL project exits **0** where it exited 2 for every operation
before. Against a Go project it reports an `alter_column` as **unchecked** in both directions and
exits 0 — ADR 0053's `Unchecked` half, reached by a third driver. `migr plan`, `up` and `down` still
name **M9-T4**, the lock being the one half missing.

`test/golden/mysql/` holds nineteen statements and one `needs-session`, and the live tests **run every
one of them against both servers** in dependency order — M8-T2's precedent, where a golden file the
engine accepted is worth more than one that is merely the right shape.

The history table's own DDL changes, gaining the character set on its table and on its four string
columns. Existing tables are unaffected: `Ensure`'s `checkColumns` compares names, not definitions.

M9-T3's check-constraint half landed here. What is left there is lock impact.

## Rejected alternatives

- **An `ErrNeedsSession` `add_index` that supplies a prefix length.** §A: the IR has no field for
  one, so the driver would invent a constraint the migration did not declare.
- **`CHARACTER SET utf8mb4` with no collation.** Pins what a column can hold and leaves what `=`
  means to each server, which is the half that matters for `add_unique`.
- **A table-level default only.** Leaves a column added to a table migr did not create inheriting
  that table's character set — the case the per-column clause exists for.
- **Refusing `alter_column`'s type half outright.** MySQL performs it natively, with `INSTANT` or
  `INPLACE`; refusing would cost the framework one of its twenty operations to avoid reading a
  catalog row.
- **Reading the default from `information_schema.COLUMN_DEFAULT`.** §C: two servers, two encodings,
  and MySQL's assumes a string grammar ADR 0057 turns off.
- **Parsing the column's line out of `SHOW CREATE TABLE`.** Faithful by construction, and it means
  writing a MySQL column-definition parser to change one token — `enum('a,b')`, `DEFAULT
  (_utf8mb4'x')`, `COMMENT 'has, commas'`, `/*!80023 INVISIBLE */`. ADR 0054 took that on for SQLite
  because a rebuild had no alternative; here the structured fields plus four refusals cover it.
- **A separate `GENERATION_EXPRESSION` check.** Written, and the mutation sweep found it survived
  every test: both servers set `EXTRA` to `VIRTUAL`/`STORED GENERATED` for the same column, so it was
  a second producer of an answer `EXTRA` already gives — the finding M8-T6 and M9-T1 each reached by
  deleting one.
- **`ALTER TABLE … RENAME TO` replaced by `RENAME TABLE`.** MySQL's atomic multi-rename is M13's
  shadow-swap tool, not a spelling choice for one operation.
