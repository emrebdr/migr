# 0077. Data operations, and irVersion 2 as a superset of 1

## Status

Accepted

## Date

2026-10-14

## Context

M12-T2's done-when is that *"`data_copy`, `data_transform`, and `data_backfill` ops exist, version the
IR appropriately, and are representable in `migr plan`."*

Until now a migration could write data in two ways, and neither could be read. `raw` is engine SQL
with no inverse, which is every `.sql` migration; `host.db.exec` is committed on its own, outside the
transaction the migration's operations run in (ADR 0032). M12-T3's chunked backfill, M12-T4's
expand/contract and M13's shadow-table copy all need data writes the engine can read: a named table,
named columns, a neutral value. M12-T1 left a seam waiting for them as well — `seedKinds`, which
allowed seeds `raw` alone because nothing else wrote rows.

Two decisions were the user's: **the versioning** (B) and **the semantics** (A). The rest followed
from measurement.

### What was measured first

Throwaway probes on all five servers and against the code; nothing from them ships, and four of the
five changed what does.

**1. Assignment against `CAST`, per engine, for every logical type** — a text source assigned to a
column of each type, and the same through `CAST`:

| | plain assignment | `CAST(… AS native)` |
|---|---|---|
| PostgreSQL 16, 17 | **refused** for every non-text type (`42804`) | works for all |
| MySQL 8.0, MariaDB 11.4 | works for all | unusable: `CAST` takes `SIGNED`, `CHAR`, … never `varchar(255)` |
| SQLite | works, storing what an insert would | **changes the value**: `CAST('true' AS boolean)` is integer `0`, `CAST('2026-01-02 03:04:05' AS timestamp)` is integer `2026` |

So each driver compiles a transform its own way, and the operation must carry the target's logical
type for PostgreSQL to have anything to cast to.

**2. A literal rendered as a default renders it, into an `UPDATE`.** PostgreSQL and SQLite accept a
quoted literal for every type. **MySQL and MariaDB refuse `'true'` into the `tinyint(1)` a `bool` is**
(error 1366), and accept the keyword `TRUE`. That is why a backfill carries `type` too: the value has
to be rendered in the column's type. The same probe found that **`DEFAULT 'true'` on a `bool` column
is refused by both servers today** (error 1067), so every `bool` default of `"true"` on that driver
fails at the server — a pre-existing defect outside this task, recorded in TASKS.md rather than fixed
in passing.

**3. Lock impact, measured rather than assumed.** The plan declared all three `LongExclusive`.
SQLite's suite measures what a statement does to the file, and its own control establishes that an
`UPDATE` over every row is a rewrite: measured, **`data_backfill` and `data_transform` are
`TableRewrite`** and `data_copy`, which writes new pages into the target and rewrites nothing that
was there, is `LongExclusive`. PostgreSQL and MySQL declare the same. Their rewrite oracles —
`relfilenode` and InnoDB's `TABLE_ID` — are one-directional and do not move for an `UPDATE`, so on
those engines the verdict is the pessimistic side of what they can observe, which ADR 0016 permits
and an under-report would not be.

**4. SQLite reads a double-quoted name that resolves to no column as a string literal.** Found by
hand, after `migr validate --connect` had caught a misspelt source column and `migr up` then applied
the migration anyway: `lower("emial")` wrote the text `emial` into every row, `SELECT "emial"` copies
it, and `WHERE "nope" IS NULL` matches nothing — each silently. A table-qualified `"people"."emial"`
is an error. **SQLite's data operations qualify every column they read.** The same misfeature reaches
this driver's existing `add_index` and `add_unique`: an index over a misspelt column is built over a
constant string, and a unique one would let the table hold one row. Recorded in TASKS.md; not this
task's change.

**5. The handshake was exact on both sides** — `sdk/go/session.go` and `sdk/typescript/src/session.ts`
refused any stated `irVersion` that was not their own.

## Decision

### A. Three neutral operations, one statement each

| op | payload | |
|---|---|---|
| `data_copy` | `from`, `to`, `columns: [{from, to}]` | inserts every row of one table into another |
| `data_backfill` | `table`, `column`, `type`, `value` (a §6.2 Default) | fills that column's NULLs, and no other row |
| `data_transform` | `table`, `column`, `type`, `from`, `function` | rewrites the column of every row from another, through `copy`, `lower`, `upper` or `trim` |

None carries engine SQL; the transform functions are a closed set, so a fifth is a format change.
`lower`, `upper` and `trim` apply to the source's **text** whatever its type. Each is one statement
over the whole table — chunking it is M12-T3's subject, and is why `type` and the table are named
rather than a predicate.

### B. irVersion 2 is irVersion 1 plus the data operations, and the handshake reads it as a ceiling

`ir.Version` is 2 and `ir.Supported()` is `{1, 2}`. Three rules make the bump reach only the
documents that use what it added:

- **`Kind.Since()`** is 1 for the twenty schema operations and 2 for the three data ones, and a
  document is **stamped with the oldest version that carries every operation it holds**. A version-1
  document keeps its exact bytes; a version-1 document holding a data operation is refused on decode,
  naming the operation and both versions.
- **The engine states the newest version it reads, and that is a ceiling rather than a
  requirement.** A runtime accepts any stated version from 1 up. A newer SDK refuses, as
  `unsupported_ir` failing the `migration.execute` that tried, only an operation newer than the
  stated version — before it is sent, so an older engine never has to call it an unknown operation
  (`docs/ir.md` §2.3). An older SDK runs against a newer engine unchanged.
- **The envelope is outside the checksum boundary** (ADR 0007), so no recorded checksum moves.

That reconciles `docs/ir.md` §10.2, which called adding an operation non-breaking, with §2.3, which
says the version exists so that an engine meeting an operation it has never heard of says *upgrade*
rather than *unknown operation*: §10.2 holds for every document that does not use the operations,
§2.3 for every one that does. The protocol version does not move — and before this rule an IR
addition was a protocol break in all but name.

**No engine-side `Since` check on `host.schema.apply`.** The plan had one. The engine states its own
newest version, so anything newer than that is already an operation its decoder does not know; a
check against the stated version could never fire.

### C. Each driver compiles a transform its own way, and `type` is what makes that possible

PostgreSQL casts to the target's native type, and runs a text function over `CAST(source AS text)`.
MySQL assigns, and runs a text function over `CAST(source AS CHAR)`. SQLite assigns, qualifies every
column it reads, and never casts. A backfill's value is rendered by the driver's own default
renderer, except MySQL's `bool`, which is the keyword. The type checks every other operation applies
— PostgreSQL's enum refusal, MySQL's array refusal, SQLite's missing `uuid` function — apply here too.

### D. None has an inverse

`data_copy`'s inserted rows cannot be told from rows already there; which rows `data_backfill` found
NULL is recorded nowhere; what `data_transform` overwrote is recorded nowhere. Each is irreversible
with a reason naming the remedy — author a `Down`, usually the inverse of the `add_column` before it
— which is ADR 0008's rule unchanged. The round-trip suites leave all three out for the reason they
leave `raw` out, and assert each still reverses to `ErrIrreversible`.

### E. `data_transform` alone is destructive

It overwrites every value of the column it writes. A backfill writes only NULLs and a copy only
inserts, so neither loses a value. `ir.DestructiveKinds()` gains `data_transform`, so
`validation.allow_destructive: [data_transform]` is a permission, and `migr validate` names it
exactly that way.

### F. `internal/fold` checks names and types and changes no schema

A data operation folds to the schema it was given — and refuses a table or column it names that is
not there, and a `type` the projected column contradicts where that type is exact rather than a
catalog's guess. That is what makes M10-T3's reference check catch a backfill of a column nothing
added with no change to the check. `engine.Unrestorable` judges the same against the live schema
before a rollback, and its ledger names a data operation's columns so a backfill following its own
`add_column` is not judged against a schema that does not hold the column yet.

### G. `migr plan` says which rows each writes

A data operation renders as every operation does — kind, verdict, statements — plus a `rows:` line
and a `rows` member in `--json`: *fills the NULLs of "people.nickname" with "anonymous", and no
other row*. One statement over a table says what it writes and not how much of the table that is.

### H. The source chains declare them not renderable

The plan gave each a chain in `internal/source`. That package exists to write the migration `migr
diff` proposes, and a differ compares two schemas, which hold no rows — so a chain would be code
nothing reaches. They are `ErrNotRenderable` beside `raw`, with the reason stated in the test.

## Alternatives rejected

- **Staying at irVersion 1** (user's choice). §10.2 permitted it, but an older engine meeting a data
  operation would report it unknown — the case §2.3 says the version exists to prevent.
- **irVersion 2 with the exact-match handshake kept** (user's choice). The CLI and every SDK would
  have to upgrade together, and a version-1 SDK could not run even a migration using no data
  operation.
- **Per-driver SQL in `data_transform`** (user's choice). More expressive, and it gives up database
  independence for the one operation, as `raw` does.
- **Untyped values** (user's choice). Measured to fail on MySQL's `bool` and PostgreSQL's
  text-to-integer assignment.
- **`LongExclusive` for all three**, as planned. Measurement 3 contradicts it on the one engine where
  the work is observable, and invariant #13 says measured.
- **`CAST` on every engine**, for uniformity. Measurement 1: unusable on MySQL, and on SQLite it
  silently changes the value.
- **A derived inverse that empties what a backfill filled.** Which rows were NULL is not recorded,
  so it would empty rows that were legitimately set — a guessed rollback, which invariant #10 makes
  worse than none.

## Consequences

- The SDKs speak irVersion 2 and accept an engine stating 1; both refuse a data operation against it
  before sending anything, and a test in each holds that.
- Every exhaustive switch and every coverage test derived from `ir.Kinds()` names the three — about
  twenty packages, nine golden files, and a runtime conformance fixture both SDKs emit byte for byte.
- Seeds may emit all three (M12-T1's `seedKinds`), and a seed's allowance permits `data_transform`.
- Two pre-existing defects surfaced and are recorded rather than fixed here: MySQL's `bool` default of
  `"true"`, and SQLite indexes over a misspelt column.
