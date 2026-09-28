# 0051. SQLite's type map, and where the table rebuild begins

## Status

Accepted

## Date

2026-09-28

## Context

M8-T1 shipped the SQLite driver complete and honestly unsupported: every operation refused with one
reason naming M8-T2, and `test/golden/sqlite/` recorded twenty refusals so the compiler would arrive
as a diff. M8-T2 writes that compiler, and it has to answer two questions PostgreSQL's did not.

**SQLite does not have types.** A declared type name sets an *affinity* — a preference applied when a
value is stored — and nothing else. `varchar(255)` holds a megabyte; `smallint` holds 2^62;
`decimal(19,4)` holds whatever an 8-byte float can. Every logical type in `docs/ir.md` §5.2 therefore
has to be mapped to a name that is, at best, a description of intent. Two coherent positions exist:
declare only the five storage classes SQLite implements, or declare a name that preserves the
logical type for whoever reads the schema back.

**SQLite's `ALTER TABLE` does four things.** It renames a table, renames a column, adds a column, and
(from 3.35) drops one. Changing a column's type, nullability or default, and adding or dropping any
constraint, all require the 12-step table rebuild — create a shadow table, copy, drop, rename, and
restore every dependent object — which is M8-T4. So the compiler has a boundary to draw, and three
operations sit awkwardly on it: `add_unique`, which SQLite can do but not with `ALTER TABLE`;
`create_view` with `or_replace`, which SQLite has no syntax for; and `drop_column`, which is native
but refuses more cases than `Compile` can see.

Every behavioural claim below was **measured against `modernc.org/sqlite` v1.59.0 (SQLite 3.53.4)**
rather than read out of the documentation, and two of the measurements contradicted the plan this
task started from.

## Decision Drivers

- A silent-wrong is the failure class this project treats as most severe; a loud refusal at apply
  time is a different and much cheaper thing.
- `migr validate` opens no database (ADR 0028), so a refusal that can be decided from the payload is
  worth a whole deploy compared to one decided at apply.
- `Compile` is pure by contract (ADR 0011): no context, no connection, and therefore no knowledge of
  the server's version or of a single row in the table.
- M11 introspects the database and diffs it against a desired schema. Whatever the declared type
  name is, that is what introspection reads back.
- CLAUDE.md §11: prefer an honest `Unsupported` over a clever emulation.

## Considered Options

### A. What the type map declares

**A1. Preserve the logical name wherever the affinity allows it.** `string(n)` → `varchar(n)`,
`decimal(p,s)` → `decimal(p,s)`, `uuid` → `uuid`, `timestamptz` → `timestamptz`, `binary` → `blob`.
SQLite's affinity rules were designed so that schemas written for other engines work unmodified, and
this uses them as intended. M11 can read the logical type back.

**A2. Declare only the five storage classes.** `TEXT`, `INTEGER`, `REAL`, `BLOB`, `NUMERIC`. Says
exactly what SQLite implements and claims nothing it does not keep. It collapses eight logical types
— `string(n)`, `text`, `uuid`, `json`, `timestamp`, `timestamptz`, `date`, `time` — onto one
indistinguishable `TEXT` column, which `migr diff` could never take apart.

Two rows are exceptions to A1 and each has its own reason:

**A3. One `integer` for all three widths, and one `real` for both floats.** SQLite has exactly one
integer type and one float type and enforces neither width. Declaring `bigint` would be a claim it
does not keep — and would cost the rowid alias, because `INTEGER PRIMARY KEY` is the only spelling
SQLite treats as one. Measured: `id integer, PRIMARY KEY (id)` auto-assigns on insert;
`id bigint, PRIMARY KEY (id)` accepts the string `'not-a-number'`.

**A4. `json` declares `text`, not `json`.** A type named `json` carries no affinity marker, so SQLite
gives it NUMERIC affinity — and a JSON document that is a bare number would be converted on the way
in. This is the one row where affinity beats preserving the name.

### B. `decimal(p,s)`

Measured: a `decimal(19,4)` column given the text `'1.10'` stores the REAL `1.1`, `typeof` `real`. A
`text` column given the same value stores `'1.10'`.

**B1. Declare `decimal(p,s)` and document the loss.** What every SQLite consumer expects to read back,
and what every ORM emits. Arithmetic, `ORDER BY` and `SUM()` all work. Exactness is whatever an
8-byte float gives, and `doc.go` says so and points at `int64` of minor units.

**B2. Declare `text`.** Exact — TEXT affinity never converts — and useless: `ORDER BY` becomes
lexicographic, `SUM()` stops working, and the column is indistinguishable from `text`.

**B3. Refuse `decimal`.** The honest-`Unsupported` position. It refuses a type SQLite is used with
constantly, and every consumer has already accepted the trade it describes.

### C. `enum(a,b,c)`

PostgreSQL refuses an enum ([ADR 0015](0015-payload-dependent-refusals.md)) because it is a
`CREATE TYPE` object no v1 operation owns: nothing would drop it, so apply → roll back → apply fails
on a type that already exists. **SQLite has no such object.**

**C1. `text` with an inline `CHECK (col IN (…))`.** Membership is enforced by the engine. The check is
part of the column definition, so it dies with the column and nothing leaks; no IR operation needs to
name it. Measured: the check works in `create_table` and in `ALTER TABLE … ADD COLUMN` alike.

**C2. Refuse it, matching PostgreSQL's verdict for a different reason** — `docs/ir.md` §5.3 says
declared label order is part of the type, and SQLite compares TEXT lexicographically.

**C3. `text`, unenforced.** Accepts `'delted'` and stores it.

**C4. Refuse an enum column whose literal default is not one of its labels.** The default can never
satisfy the constraint the labels compile to, whatever the table holds. SQLite does refuse it, at
apply or at the first insert that relies on the default; `migr validate` opens no database, so
deciding it from the payload is a deploy earlier.

### D. The three operations on the boundary

**D1. `add_unique` compiles to `CREATE UNIQUE INDEX`, `drop_unique` to `DROP INDEX`.** A `UNIQUE`
constraint *is* a unique index in SQLite, and adding one to an existing table any other way means a
rebuild. The IR gives `create_table` no way to declare a unique inline, so every unique constraint in
a migr-managed database came from `add_unique` — which is what makes `DROP INDEX` always find it.

**D2. `create_view` with `or_replace` compiles to two statements**, `DROP VIEW IF EXISTS` then
`CREATE VIEW`. SQLite has no `CREATE OR REPLACE VIEW`. Both run inside the migration's one
transaction, and SQLite's DDL is transactional, so the replacement is atomic. It does not refuse a
changed column list the way PostgreSQL's does. The alternative was refusing `or_replace` outright.

**D3. `drop_column` compiles to the native `ALTER TABLE … DROP COLUMN`.** Native since 3.35 and the
linked library is 3.53, so the version-dependent case ADR 0012 named cannot fire against this build.
SQLite still refuses the statement for a primary key, a unique, an indexed column, or one a check,
view or foreign key names — measured — and `Compile` can see none of that.

### E. The two `ADD COLUMN` rules that are not payload rules

The plan for this task called for refusing two `add_column` forms at compile time, on the strength of
SQLite's documented restrictions: a NOT NULL column with no default, and a column with a non-constant
default. **Measurement contradicted it.** Both statements are *accepted* against an empty table and
refused only when the table already holds a row:

```
empty table    ALTER TABLE t ADD COLUMN a text NOT NULL                    -> accepted
               ALTER TABLE t ADD COLUMN b timestamptz DEFAULT CURRENT_TIMESTAMP -> accepted
one row        the same two statements  -> "Cannot add a NOT NULL column with default value NULL"
                                           "Cannot add a column with non-constant default"
```

**E1. Compile both and leave the refusal to SQLite.** The condition is on the table's contents, which
is neither something `Compile` can see nor something a `capability.Rule` can — a rule receives the
operation, not the rows. And there is no silent-wrong to prevent: the engine's refusal is loud,
inside the migration's transaction, and rolls it back.

**E2. Refuse both anyway.** Would reject a migration that is correct against an empty database —
which is how most SQLite projects are created, tested, and shipped.

## Decision Outcome

**A1 with A3 and A4, B1, C1 with C4, D1, D2, D3, E1.**

Thirteen operations compile — `create_table`, `drop_table`, `rename_table`, `add_column`,
`drop_column`, `rename_column`, `add_index`, `drop_index`, `add_unique`, `drop_unique`, `create_view`,
`drop_view`, `raw`. Seven refuse with a reason naming **M8-T4**: `alter_column`, `add_foreign_key`,
`drop_foreign_key`, `add_check`, `drop_check`, `add_primary_key`, `drop_primary_key`.

The declaration stays derived from the compiler (ADR 0015), so those thirteen become
`Native(LockUnknown)` and the seven stay `Unsupported`. Refining the thirteen into `Emulated` verdicts
and answering the lock axis is M8-T3's, exactly as ADR 0015 anticipated: *a `Compile` that succeeded
says nothing about how*.

Identifiers are double-quoted with an embedded quote doubled, literals single-quoted with `''`
doubling, every composed statement ends in `;`, and `raw` passes through untouched — all four are
PostgreSQL's rules unchanged. **There is no identifier length to check**: SQLite has none, so this
driver's `checkIdentifier` is shorter than PostgreSQL's by exactly the case that does not exist.

`checkHistoryTable` now compiles `history.Schema(table)` rather than checking the name alone, which is
the check M8-T1 deferred because it had no compiler.

## Consequences

### Positive

- **The golden files are executable.** A live SQLite database is a `t.TempDir()`, so a test compiles
  every operation and runs it — untagged, inside `make check` and both CI legs. PostgreSQL's driver
  can only make that claim behind Docker. The type map is asserted through `PRAGMA table_info`, and
  the decimal warning is a measurement rather than a citation.
- **SQLite supports a logical type PostgreSQL refuses.** A TypeScript migration creating a table with
  an `enum` column validates against `driver: sqlite` and is refused against `driver: postgres`. That
  is the capability system running in a direction nothing had exercised.
- A `.sql` project can be validated against SQLite at all, where M8-T1 refused `ir.Raw` along with
  everything else.
- The seven remaining refusals now say what SQLite cannot do, rather than that migr has not written
  the compiler yet.

### Negative

- **`string(255)` and `decimal(19,4)` are declarations SQLite does not enforce.** A column declared
  `varchar(255)` holds a megabyte, and a `decimal` column does not hold the digits it was given. Both
  are stated in `doc.go` and neither is detectable by migr.
- **Label ordering is lost.** `docs/ir.md` §5.3 says `enum(a,b)` and `enum(b,a)` are different types,
  because PostgreSQL compares labels by declaration order. On SQLite they are the same column and
  `ORDER BY` is lexicographic. Membership is what is preserved.
- **`drop_column` can fail at apply** for a column an index, constraint or view names. The refusal is
  SQLite's and it is loud; covering those cases is M8-T3's verdict and M8-T4's rebuild.
- **`or_replace` does not refuse a changed column list** the way PostgreSQL's `CREATE OR REPLACE VIEW`
  does, so one operation means slightly different things on the two engines.

### Neutral

- Nothing about the wire format, the IR, or the driver contract changed. This is a compiler and a
  type table inside one driver package.
- A `raw` operation is the only statement this driver does not terminate, and nothing it compiles sets
  `Statement.NoTransaction` — so a migration it compiles is always exactly one transactional run, as
  PostgreSQL's is.

### The forward note M8-T3 needs

M8-T3's done-when declares `drop_column`, `alter_column` and the constraint changes `Emulated` with
named strategies. **Declaring an operation `Emulated` while `Compile` still refuses it breaks the
biconditional the driver conformance suite exists to prove** — `Resolve(op).Allowed()` is false
exactly when `Compile` refuses, and `Emulated` is an allowed verdict. So M8-T3 either keeps the seven
rebuild operations `Unsupported` until M8-T4 lands, or the two land together. This is recorded here
because rediscovering it as a failing suite is the expensive way to learn it.

## Rejected

**A2, declaring only the five storage classes.** Honest about enforcement and useless to
introspection: eight logical types become one `TEXT` column that `migr diff` could not tell apart, so
every diff against a SQLite database would propose changes that are not changes. SQLite's affinity
system exists precisely so that a declared name can carry more than the storage class does.

**B2, `decimal` as `text`.** Exactness bought by breaking `ORDER BY`, `SUM()`, and introspection at
once — and a rebuild would copy it faithfully into a column nobody can compute with.

**B3, refusing `decimal`.** The honest-`Unsupported` argument applies to an emulation that surprises;
this would refuse a type SQLite is used with in every schema that has a price in it, to prevent a loss
its whole ecosystem has already accepted and documented.

**C2, refusing `enum`.** PostgreSQL's reason does not port: there is no object to leak. Refusing a
type this engine can enforce, because a different engine cannot, is the `if driver == "…"` reasoning
CLAUDE.md §11 warns about, aimed backwards.

**C3, unenforced `text`.** Accepts a value that is not a label and stores it. The silent-wrong shape
this project refuses everywhere else.

**Refusing `or_replace`.** `DROP VIEW IF EXISTS` plus `CREATE VIEW` is the standard SQLite idiom, a
view holds no data, and the pair is atomic inside the migration's transaction. Refusing it would make
one IR operation unusable on this engine for nothing.

**The `hex(randomblob(…))` UUID idiom.** `uuid()` is refused because SQLite links no UUID function —
verified: `modernc.org/sqlite` v1.59.0 registers no `sqlite3_uuid_init`. The `lower(hex(randomblob(4))
|| '-' || …)` expression would write a generator into the operator's schema rather than name one, and
it is not a UUID generator so much as a shape that resembles one. The refusal names it and says so.

**E2, refusing the two `ADD COLUMN` forms.** The plan called for it and the measurement killed it: the
identical statement succeeds against an empty table. A driver that refused it would reject correct
migrations on every fresh database, which is where most SQLite migrations run.

## Notes

Revisit A1 if M11's introspection finds that reading a declared name back is less useful than
expected — the whole argument for preserving the logical type rests on that reader existing.

Revisit C1 if the IR ever grows an operation that owns a named constraint separately from the column,
since the inline `CHECK` would then have a spelling that `drop_check` could reach.

D3 is worth revisiting at M8-T3: since `modernc.org/sqlite` links the library into the binary, this
driver's version floor of 3.25 cannot fire and `AtLeast(3, 35)` is always true. The version-dependent
`drop_column` case that ADR 0012 and CLAUDE.md §4.2 both use as the canonical example is, for this
driver, a fact about the dependency rather than about the database — which is a different sentence
from the one those documents currently write.
