# 0056. The SQLite history store: asking the catalog where PostgreSQL reads a code

## Status

Accepted

## Date

2026-09-29

## Context

M3-T6 gave PostgreSQL's history store a task of its own. M8 had none, and M8-T1 found out why: invariant
#3 makes the history table itself a set of driver-compiled operations — `history.Schema(table)` through
`Compile` — so the store cannot precede the compiler. It was folded into M8-T6 rather than numbered
after, because *"Up/Down round-trips restore the starting snapshot"* cannot run without it.

Since M8-T5 the migration lock is taken and released on every command that takes one, so
`pendingHistory` is the last thing between a `driver: sqlite` project and a real run. `migr up`,
`migr plan` and `migr status` all currently die in it.

**The one thing PostgreSQL's store has that SQLite's cannot.** `pgHistory` classifies every failure by
SQLSTATE: `42P01` is a missing relation, `42703` a missing column, `23505` a duplicate run. Measured on
modernc 1.59.0, SQLite gives **all three of those `SQLITE_ERROR`, which is 1** — the code a *syntax
error* also carries. Only the duplicate has a code of its own, and only because `lib.Error.Code()`
reports the **extended** code: `SQLITE_CONSTRAINT_PRIMARYKEY`, 1555, rather than the primary 19.

So the substance of this record is what a store does when the engine will not tell it what went wrong,
for the sentinel CLAUDE.md calls the worst in the contract to get wrong:

> getting it wrong is the worst failure in the contract: read as "no runs yet", a fully migrated
> production database looks virgin and the next `up` re-runs every migration it had already applied.

## Decision Drivers

- `history.ErrNoTable` on **every** read path, and nothing about the interface shape enforces it — a
  store that forgets still compiles (ADR 0019).
- Invariant #3: the history table is driver-compiled IR, and nothing above a driver composes SQL.
- ADR 0013: runs are ordered by `batch, ordinal` — the key, not the clock.
- `forbidigo` leaves a driver no clock, and `internal/history` reads none either: `AppliedAt` is the
  caller's.
- ADR 0050: a tag names a requirement, and this engine's is a `t.TempDir()`.

## Decision Outcome

### The catalog is asked, not the failure classified

`requireTable(ctx)` runs one `sqlite_schema` lookup at the top of `List`, `Latest`, `Record`,
`MarkFailed` and `MarkDirty`. It is what makes *"ErrNoTable on every read path"* structural rather than
remembered — there is one helper and every path but `Ensure` calls it, `Ensure` being the one that
branches on absence.

A string match on `no such table:` was the alternative. The message does carry the table name — measured,
and an earlier probe that suggested otherwise was using `nothing` as a table name, which is a keyword in
`ON CONFLICT DO NOTHING` and so produced a syntax error rather than the missing-table one. What it does
not carry is a distinguishable **code**, and matching English prose to decide whether a production
database has been migrated is not a trade this project makes for one saved round trip on the failure path.

The same move covers the two failures that are left. `explainWrite` asks the extended code first — the
duplicate run is the one cause SQLite names — and then **asks the catalog again**: the table gone is
`ErrNoTable`, a column missing is `ErrHistoryShape`, and anything else is the engine's own failure
wrapped. `explainCreate` does it too: a `CREATE TABLE` that failed while the table is now present is
the race, which is `42P07`'s answer reached by looking rather than by reading a code.

### `COLLATE NOCASE`, and why it is not decoration

SQLite folds ASCII case inside an identifier but compares `sqlite_schema.name` as **BINARY**. Measured:
a table created as `MixedCase` is reachable as `mixedcase`, and `WHERE name = 'mixedcase'` finds
nothing. Without the collation, `Ensure` would report a `MixedCase` history table absent on every run,
run `CREATE TABLE`, and be refused as already existing — forever.

That is PostgreSQL's `quote_ident`/`to_regclass` finding in a second dialect, and it is the second time
this exact defect has appeared in this repository. `ORDER BY type LIMIT 1` keeps the answer deterministic
where two objects fold to one name.

### `pragma_table_info`, and a generated column counting as absent

Measured: `pragma_table_info` omits a generated column, `pragma_table_xinfo` includes it, and SQLite
refuses an `INSERT` into one. `table_info` is therefore the *correct* pragma rather than the
convenient one — a generated `error` column is one this build cannot write to, so reporting it
**missing** is the honest answer, where `xinfo` would call a table that fails every insert the right
shape. `checkColumns` says so in its message.

### The instant is rendered here, not handed to the library

`applied_at` is declared `timestamptz`, which in SQLite is a name carrying **NUMERIC** affinity — it
contains none of `INT`, `CHAR`, `CLOB`, `TEXT`, `BLOB`, `REAL`, `FLOA`, `DOUB`. Measured: an RFC3339
string stays text in it, round-trips exactly, and compares `Equal` to the original instant.

A bound `time.Time` becomes `2026-09-29 11:22:33.123456789 +0530 UTC+5:30` — Go's own `String()` form,
**zone abbreviation included**, which no RFC3339 parser reads back and which no other engine would
produce. Scanning the rendered form into a `*time.Time` fails outright. So the store renders and parses
it: UTC, fixed-width fraction.

Fixed-width rather than `time.RFC3339Nano` because that layout **trims trailing zeros**, which makes
`.11Z` sort before `.1Z` as text. Nothing here orders by `applied_at` — ADR 0013 orders by
`batch, ordinal` — but a recorded column an operator will sort by hand should not lie about it. Reading
back uses `RFC3339Nano`, which accepts any fraction width, so a value some other writer trimmed still
parses.

A stamp that will not parse is a `history.CorruptError` naming `applied_at`, alongside the three
PostgreSQL already reports that way.

### `sqlitetest`, and the second depguard pair

The round trip compares a before and after schema, `cmd/migr` is the only package that may drive the
whole stack, and invariant #3 will not let it compose the catalog query. So the probe lived in
`internal/driver/sqlite/sqlitetest` and was exported — pgtest's argument unchanged (ADR 0040).
**M11-T6 deleted that package** ([ADR 0074](0074-the-neutral-round-trip.md)): `Conn.Introspect`
answers `cmd/migr` neutrally, so nothing above the driver needs a probe, and what remains of the
`sqlite_schema` reading is a test helper inside the driver — kept because a trigger and a generated
column are two things only the recorded statement holds, and a rebuild must not lose either.

The driver's own rebuild suite reads the same digest, so the driver imports its own child, so
`.golangci.yml` owes the pair CLAUDE.md already anticipated: `internal/driver/sqlite/` exempted from the
blanket `internal/driver/` deny, and held instead by a rule set denying `postgres`, `mysql` and every
language runtime by name. Exempting without the pair would trade invariant #2 for nothing. **The pair
outlives the child package**: it holds invariant #2 regardless, and PostgreSQL still has a child.

One probe rather than PostgreSQL's five, because everything SQLite knows about its schema is in
`sqlite_schema`. The `Ask` seam is kept anyway — not because a second fetcher is needed today, but
because it is what stops one growing, and with it a second digest.

### The round trip is untagged, and what it can and cannot claim

SQLite needs no daemon, so `cmd/migr` gains the **first end-to-end CLI round trip inside `make check`**
and on both CI legs. Paying for it meant moving the engine-neutral half of that package's harness —
`migr`, `migrOut`, `drained`, `migrFailure`, `stateOf`, `countRuns` — out of the `integration`-tagged
file, since a tagged file may use untagged declarations and not the other way round.

It is **two projects**, and the reason is worth stating: every `.sql` migration reaches the database as
`ir.Raw`, so a `.sql` project can never produce an emulated operation however it is written. The
`.sql` project therefore covers the native path — and its round trip is **byte-identical**, including
SQLite's native `DROP COLUMN`, which rewrites the whole table content and its recorded `CREATE TABLE`
with it; measured, dropping a column and adding it back leaves the definition exactly as it was. A **Go**
project covers the rebuild, which is where `migr plan` finally labels an emulation a real database
produced.

**A rebuild cannot restore the wording of the table it replaced.** It re-renders the `CREATE TABLE` from
its own splitter, so the columns, the key, the indexes and the constraints come back and the recorded
text is the rebuild's. That is not new — M8-T4's `checkOther` already exempts the rebuilt table's own
line — and the end-to-end suite exempts exactly that one line and then asserts the stronger property
instead: a **second** round trip from the rebuilt state is byte-identical, so the wording moves once and
then stops.

## Considered Options

### A. How `history.ErrNoTable` is decided

- **A1: ask `sqlite_schema` before every path that touches the table** ✔.
- A2: match `no such table:` on the failure. **Rejected**: `SQLITE_ERROR` is shared with syntax errors,
  so the message is the only signal, and this is the sentinel whose failure mode is re-running every
  migration against a live database.
- A3: pre-check reads, classify writes. **Rejected**: two mechanisms for one sentinel, and the next path
  added has to pick which — exactly how ADR 0019's "a store that forgets still compiles" happens.

### B. How the other two write failures are explained

- **B1: ask the catalog again on the failure path** ✔ — deterministic, and it costs nothing on the path
  that succeeds.
- B2: leave them as a wrapped engine failure. **Rejected**: `ErrHistoryShape` is what tells an operator
  their history table is unusable rather than their migration broken, and PostgreSQL reports it.
- B3: match the messages (`no such column`, `already exists`). **Rejected** for A2's reason.

### C. How `applied_at` crosses

- **C1: rendered here, UTC, fixed-width fraction** ✔.
- C2: bind the `time.Time`. **Rejected on measurement**: the stored form is Go's `String()` with a zone
  abbreviation, unparseable and unportable, and scanning it back into a `time.Time` fails.
- C3: Unix milliseconds. **Rejected**: it loses the nanoseconds `Record` carries, and a history table an
  operator opens with `sqlite3` should be readable.

### D. Which pragma `checkColumns` reads

- **D1: `pragma_table_info`** ✔ — a generated column counts as absent, which is what writing to one
  would do anyway.
- D2: `pragma_table_xinfo`. **Rejected**: it would call a table that fails every insert the right shape.

### E. Where the digest lives

- **E1: `sqlitetest`, with the driver's own suite moved onto it and the depguard pair paid** ✔.
- E2: `sqlitetest` for `cmd/migr` only, the driver keeping its local digest. **Rejected**: two digests
  describing one catalog, which ADR 0040 called the one thing a digest must not be.
- E3: export a helper from the driver package itself. **Rejected**: test-only code in the shipped
  package, which is the thing `pgtest` exists to avoid.

### F. Whether the round trip is tagged

- **F1: untagged, harness split** ✔ — ADR 0050's rule, and the first CLI round trip in `make check`.
- F2: tagged `integration`. **Rejected**: it would gate a test on a Docker daemon it does not use.

## Consequences

### Positive

- A `driver: sqlite` project runs: `migr up`, `migr down`, `migr status`, `migr history`, `migr plan`
  and `migr validate` all work end to end, and **M8 is complete**.
- `migr plan` labels an emulated operation against a real database for the first time, printing the
  rebuild's own statements rather than a fake's.
- The whole end-to-end round trip is in `make check` with no Docker, on both CI legs.
- `internal/driver/sqlite/pending.go` is gone.

### Negative

- Every read and write costs one extra `sqlite_schema` lookup. It is an in-process b-tree probe against
  a table SQLite keeps in its page cache, and the alternative was matching prose.
- A table a rebuild has touched keeps the rebuild's rendering of its definition, so one round trip is
  not text-exact for an emulated operation. Stated here, in `doc.go`, and asserted as the
  stability property instead.
- `cmd/migr`'s harness is now split across a tagged and an untagged file, so a reader looking for
  `migr()` no longer finds it beside the PostgreSQL cases.

### Neutral

- `HistoryError` now renders `migration history "x": …` rather than `history.table "x": …`, matching
  PostgreSQL's, since the type carries runtime failures as well as name refusals. The two
  construction-time refusals name `history.table` in their detail instead, so the config key survives.
- `checkHistoryTable` already compiled the schema (M8-T1), so the task's note describing it as a name
  check alone was stale; it is corrected in `TASKS.md` rather than acted on.

## Notes

The measurements are in the task's findings in `TASKS.md`. Two of them changed the design rather than
confirming it: extended result codes survive `lib.Error.Code()`, which is what lets the duplicate run be
detected precisely instead of by message; and `sqlite_schema.name` comparing BINARY is what put
`COLLATE NOCASE` in the lookup rather than leaving `Ensure` to loop forever on a `MixedCase` table.

`Introspect` is M11-T1, and M11-T6 deleted `sqlitetest` on the back of it (ADR 0074).
`--require-safe` and the lock axis in `migr plan` remain M13-T6.
