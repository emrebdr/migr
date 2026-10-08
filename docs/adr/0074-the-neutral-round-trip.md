# 0074. Comparing two moments of a schema through the snapshot rather than the catalog

## Status

Accepted

## Date

2026-10-08

## Context

M11-T6's done-when is *"apply-all → introspect → diff-against-desired yields an empty diff, on all
three drivers."* Both halves of that were already asserted when the task was reached:
[ADR 0073](0073-the-schema-projection.md) added the diff conformance suite's fifth leg, which folds
the fixture onto an empty snapshot and holds it to proposing nothing against what each of the five
engines read that same fixture back as; the pure identity `Forward(a, Diff(a, b)) == b` is asserted
in `internal/fold` over every one of `internal/diff`'s fixtures; and `cmd/migr` runs `migr diff`
end to end on SQLite, where an up-to-date database proposes nothing.

So the task is what its own notes say is left: **the per-driver catalog digests to delete, plus
whatever it wants to assert that those five do not.**

### What a digest was, and why there were three

A done-when written at M3-T5 asked for *"introspection snapshot comparison"* eight milestones before
`internal/schema`'s `Snapshot` existed. With nothing to compare through, each driver's suite asked
the engine to describe itself and compared the text:

| Where | How | Sites |
|---|---|---|
| `internal/driver/postgres/pgtest` | five `pg_catalog` probes, reassembled and sorted | 61, in 5 files |
| `internal/driver/mysql` (test-local) | `SHOW CREATE TABLE` / `SHOW CREATE VIEW`, lines sorted | 22, in 1 file |
| `internal/driver/sqlite/sqlitetest` | one `sqlite_schema` probe | 2 |
| `cmd/migr`, through the two packages above | — | 52, in 9 files |

Two of them were exported *packages* rather than test helpers, and that is invariant #3: no test
above a driver composes SQL to send to a database on migr's behalf, so `cmd/migr` — the only package
that may drive the whole stack — could not hold a catalog probe. `pgtest` also carried an `Ask` seam
so one set of probes could serve a caller reading through `driver.Conn` and a caller reading from
inside an open transaction, which `ADR 0017` forbids opening a second of.

`Conn.Introspect` has been on the contract since M11-T2 ([ADR 0069](0069-per-driver-introspection.md)).
Every one of those needs is answered by it, so each digest was a second answer to a question that
now has a neutral one.

### What was measured first

Three probes against PostgreSQL 17 and SQLite; nothing from them ships, and each decided code.

**A snapshot reports a table emptied by `drop_column`.** `create_table` with one column, then
`drop_column` of it, reads back as a `schema.Table` with **zero columns** — so M3-T5's stand-in
question survives the move, and lands more precisely than before: the digest only proved a name
appeared somewhere in a text blob, where the snapshot distinguishes *emptied* from *dropped* on the
column count. PostgreSQL's `relationQuery` lists relations independently of its column probe, which
is what makes this true rather than incidental.

**A real CLI round trip compares `Equal`.** `migr up` then `migr down` over the Go end-to-end
project: the snapshot differs after the apply and is `Equal` after the rollback. And the suite's own
history table **is in the snapshot when the session was not opened with it** — `Options.Namespace`
defaults an empty `HistoryTable` to `migr_migrations`, while those suites record into
`migr_e2e_*`. The PG digest never excluded it at all and got away with it because the table is
present in both moments, which is not a property a round trip should rest on.

**`schema.Marshal` canonicalizes**, so comparing marshalled text is exactly `Snapshot.Equal`. And
`ColumnType.String()` and `ColumnDefault.String()` already render the neutral vocabulary —
`int64`, `int32? (integer)`, `unknown (tsvector)` — so a readable rendering needed no new policy
about types, only a line format.

### The finding that decided what this task asserts

`cmd/migr`'s SQLite round trip compared the recorded DDL line by line and **exempted the rebuilt
table**:

```go
if was[line] || strings.HasPrefix(line, "table rebuilt_accounts ") {
    continue
}
```

SQLite's rebuild re-renders the `CREATE TABLE` it replaces, so a text comparison cannot hold the
rebuilt table to anything — which left **the one table the emulation actually touched as the one
thing the round trip did not check.** MySQL's digest paid the same cost differently: its lines are
sorted because a reversed `drop_column` appends the column and no v1 operation can put it back where
it was, `AFTER` having no field in the IR.

A snapshot holds what a table has and never how it is worded, so neither workaround has anything to
be about. That is the assertion this task adds, and it is CLAUDE.md §7's existing claim — *"the
snapshot is also the instrument that makes that comparison right"* — demonstrated rather than stated.

## Decision

### A. One source of truth, and a rendering of it

`describeSchema`, `digest` and `sqliteDigestOf` keep their signatures and change their
implementations to `Conn.Introspect`. That is what makes ~135 call sites a mechanical change rather
than a rewrite: every `!=` and `==` comparison between two moments is untouched and is now a
snapshot comparison.

`schema.Render` renders a canonical snapshot as sorted lines, one per element, each opening with its
kind so that sorting groups them:

```
check       users ck_users_email "email <> ''"
column      users.email string(255) null default "x"
column      users.id int64 not null
index       users idx_users_email (email)
primary_key users users_pkey (id)
table       users
view        listing "SELECT id FROM users"
```

It is production API rather than a test helper because three packages need it and Go shares a
declaration no other way, and it sits beside `Marshal` as the readable counterpart — `Marshal` is
the canonical form for a machine, this one for a person. It is **not** a second source: `Marshal`
and `Render` are two views of one snapshot, where two digests were two readings of one database.

An exact column type renders as the logical type alone and a guess says so, which is
`ColumnType.String()`'s own rule. A native spelling would make two engines' renderings differ for
no reason, and the renderings are never compared across engines.

`schema.Difference` renders the lines two renderings disagree on. Both drivers already held an
identical `diffLines`/`linesMissingFrom` pair and `cmd/migr` held none, so this replaces two copies
and gives the third caller what it was missing — a failed round trip printed two whole schemas,
which on a database several suites share is most of a screen for a one-column difference.

### B. The history table is excluded by the session, not by a parameter

`Introspect` leaves out the table migr records its own runs in, so a probe session opened with the
suite's own history table cannot see it — and `digest` gained that parameter, which it had never
had. `sqliteDigestOf` already took one for its probe and now passes it to `Open` instead.

Where a test's subject *is* the history table — `checkCreatedTable`, which holds what `Ensure`
created to what the neutral schema declares — it is described from a session pointed at a
**different** history table, for which it is an ordinary table. That the name is what decides is the
point rather than a way around it, and a new test asserts the other direction: a session's own
history table is absent from its own snapshot, because read as part of the schema it would have
every `migr diff` propose dropping the migration history.

### C. The rebuilt table loses its exemption; the wording claim moves into the driver

`checkRebuildRestored` is now a plain equality over the whole schema, the rebuilt table included.

What that cannot see is paid for rather than dropped. *The recorded wording moves once and then
settles* is a fact about SQLite's rebuild, not about migr's stack, so it is asserted in
`internal/driver/sqlite` — where invariant #3 permits the SQL outright and where `recordedSQL`, the
`sqlite_schema` probe `sqlitetest` used to export, now lives as a test helper. Three of the seven
rebuilds measure it; the other four skip out loud, their fixtures carrying no `from` payload, so
`ADR 0008` refuses to invert them.

**One text comparison therefore survives on purpose**, and a test says why: a snapshot holds what
the twenty operations can create, and no operation creates a **trigger** — so a rebuild that dropped
one would compare equal. `TestARebuildLosesNothingThatReferencesTheTable` covers a table with an
index, a unique index, a trigger, a view, a check and keys in both directions, and the trigger and
the generated column are the two things only the recorded text holds.

### D. MySQL's digest goes too, though it is not one of the two

The task names *"the two per-driver catalog digests"*, which are the two packages; MySQL's is
test-local, and the fifth conformance leg already covers that engine. It is converted anyway,
because the done-when names three drivers and because its sorted-lines workaround is the sharpest
illustration of what the snapshot fixes.

Its two pinned *"measurably does not restore"* findings survive and read better: the index MySQL
leaves behind for a foreign key over an unindexed column is `len(table.ForeignKeys) == 0` plus an
index of the key's name, and the `NOT NULL` that `add_primary_key` implies is `column.Nullable`.
Both are now structural rather than a `strings.Contains` over `SHOW CREATE`. What replaces its two
digest-property tests is a scope test: one engine gives every test in that package the same
database, so the fixture's prefix is what keeps a comparison about this round trip — and a scope
that kept nothing would make every round trip there pass by comparing two empty schemas.

### E. The assertions that read native spellings now read logical ones

Five of ~135: `e2e_deps_items.sku character varying(32)` → `string(32)`,
`e2e_mixed_accounts.tier character varying(16)` → `string(16)`, `verified boolean` → `bool`,
`e2e_ts_accounts.email character varying(255)` → `string(255)`, and
`migr_gap_nullable.id bigint not null` → `int64 not null`. M11-T7's two SQLite assertions move the
same way: `"tag" varchar(64) NOT NULL` becomes `column crates.tag string(64) not null` and
`FOREIGN KEY ("crate") REFERENCES "crates" ("tag")` becomes `(crate) -> crates(tag)`.

Each of those now reads in the vocabulary the framework's central claim is about, and two of them
are stronger for it: a key being over a column is read structurally rather than by matching a
substring of the engine's own DDL.

## Alternatives rejected

- **Keep the digests beside `Introspect`.** Rejected on the task's own ground: two answers to one
  question, and the digests are the answer that cannot hold the rebuilt table or survive a reversed
  `drop_column` without sorting around it.
- **A `schematest` package for the renderer.** It would need its own doc, its own depguard
  justification, and a reason not to be in `internal/schema` — where the snapshot it renders already
  is. `Render` is to `Marshal` what `ColumnType.String()` is to its `MarshalJSON`.
- **Compare `schema.Marshal` text and skip the renderer.** It works — `Marshal` canonicalizes, so
  the comparison is `Equal` — but it is one line of JSON, so every failure prints the whole schema
  with no way to see what moved. The line format is what `Difference` needs to exist.
- **Return `*schema.Snapshot` from the helpers and convert every comparison to `.Equal`.** The
  compiler would find all ~135 sites, since a struct holding slices will not compare with `!=` — but
  each one then needs a rendering for its failure message anyway, so it is ~135 edits to reach the
  same place as ~15.
- **Keep MySQL's digest by relocating its SQL.** It is already inside the driver, so there was
  nothing to relocate; the question was only whether to convert it, and D answers that.
- **Drop the *"wording settles"* claim.** It is a measured fact about an engine whose emulation is
  the most expensive thing migr does on it. Moving it costs a test file's worth of helper and keeps
  it.

## Consequences

`cmd/migr` composes no catalog SQL at all — `pgtest` keeps only `Drop`, which M7-T10 needs to clean
up after a migration that commits through `host.db.exec` and then fails, and which nothing else can
do. `sqlitetest` is deleted, and **its depguard pair went with it**: that pair existed only because the
blanket `internal/driver/` deny blocks a driver from importing its own child, so with no child the
exemption buys nothing and the driver is back under the blanket deny — strictly tighter than the
named rule set that replaced it. Measured: an import of `internal/driver/postgres` from that subtree
is now refused by `implementations`. `postgres` keeps its pair, `Drop` still being a child.

Four documents stop promising a deletion and record one: ADRs 0040, 0056, 0065 and 0069 each carried
a line saying *"M11-T6 is where this goes away."*

A failed round trip now prints only the lines that moved, in the neutral vocabulary. Read by hand
against a deliberately broken rollback — a `.sql` migration whose `down` drops the index and leaves
the table — the output is the five elements left behind, each naming the object and its logical type.

What this does not change: `internal/schema` still imports nothing of `internal/ir`, `Render` does
no I/O, and no driver's `Introspect` moved. The claim that got stronger is the round trip's, and it
got stronger by deleting an exemption rather than by adding an assertion.
