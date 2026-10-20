# 0079. Replacing a column through four migrations, each a phase of one declared change

## Status

Accepted. Ordering the phases across deploys is ADR 0087's release gate.

## Date

2026-10-14

## Context

M12-T4's done-when is that *"the add-column → backfill → swap → drop-old-column pattern is
expressible as a guided sequence in both SDKs, with the unsafe steps clearly marked"*, and it leaves
*"the multi-deploy release gating that makes this genuinely zero-downtime"* to M13-T7.

Each of the four steps was already expressible: `add_column`, `data_transform`, `alter_column`,
`rename_column` and `drop_column` all exist in both SDKs. What made the pattern hard to get right by
hand was a handful of details that every hand-written version has to rediscover:

- **the new column must be added nullable**, whatever it ends as, or every app version still writing
  the table without it is refused;
- **the backfill must be a migration of its own**, because only a data-only migration runs in chunks
  (ADR 0078), and one statement over a large table is the outage chunking exists to prevent;
- **the old column must stop being required before the app stops writing it**, or the first insert
  from the new app version fails NOT NULL on the column it no longer knows;
- **the drop's `from` must record the old column as swap left it** — nullable — or `engine.Unrestorable`
  refuses it against the live schema, and a derived inverse would re-add it NOT NULL onto a populated
  table, which no server performs.

## Decision

### A. One declared change, and each phase is a migration

`migr.ReplaceColumn(table, column, was)` in Go and `replaceColumn(table, column, was)` in TypeScript
declare the change: the column as it is, what it becomes (`With`/`with`, `Null`, `Default`), what
the backfill applies (`Through`/`through`, `copy` by default), and optionally its new name
(`RenameTo`). Four methods each return a **migration**, not a helper to call from one:

```go
func init() { migr.Register(migr.ReplaceColumn("users", "email", migr.String(255)).With(migr.String(320)).Expand()) }
```

```ts
export default replaceColumn("users", "email", t.string(255)).with(t.string(320)).expand();
```

So two phases of one change **cannot** land in one migration — there is one value to register per
file — and the backfill always runs in chunks. Every phase emits through the ordinary chain with
complete `from` payloads, so every gate that judges a hand-written migration judges it: destruction,
capability, `Unrestorable`, and `migr validate`'s reference check.

**The declaration is restated in every phase file.** A shared definition in another package would
be shorter, and it would let an edit there change what an *applied* migration emits without moving
its source digest — which is exactly the hole ADR 0035 records for a package the migrations import.
Restating it costs a line per file and keeps invariant #6 meaning what it says.

The value is immutable in both SDKs: each link returns a new declaration, so deriving a second
change from a first cannot alter it.

### B. Two forms, chosen by `RenameTo`

| Phase | Keep-name form (default) | Rename form |
|---|---|---|
| expand | add `<column>_next`, nullable, with the new type and default | add the new name |
| backfill | `data_transform` `<column>_next` ← `<column>` | the same into the new name |
| swap | relax the old column, tighten the new, rename old → `<column>_old` and next → `<column>` | relax and tighten only |
| contract | drop `<column>_old` | drop the original |

`Via` and `Retire` override the two derived names, and are refused beside `RenameTo`, which names
neither. A declaration that changes nothing — same name, type, nullability, default, and a `copy` —
is refused by every phase, as are blank or colliding names and an unknown function.

### C. How each phase is undone

| Phase | Down |
|---|---|
| expand | drop the new column, with its `from` |
| backfill | **nothing** (D) |
| swap | the exact reverse: names back, then nullability |
| contract | re-add the old column nullable and `copy` the values back — or, after a `lower`/`upper`/`trim` backfill, **irreversible**, naming the function |

The lossy case is irreversible rather than approximately reversible: a down that copied lowercased
addresses into the old column would report a rollback that restored something it did not. The swap's
down re-tightens the old column, so a row the copy missed fails loudly there rather than silently.

### D. An authored down may emit nothing

The backfill has nothing of its own to undo — the expand's down drops the column it filled — but the
engine refused any migration whose statement list was empty (`ErrNothingToRun`), and a data
operation has no inverse to derive. So `checkRunnable` now accepts exactly one more shape: **a down
that emitted no operations**. The run takes a transaction holding only its history row, and is
recorded rolled back with 0 statements; `migr plan --down` says *"nothing to undo"* and `--dry-run`
counts 0 statements.

The boundary is drawn so that nothing else moves:

- **an up that emits nothing is still refused** — reporting success for a schema change that never
  happened is the failure the rule exists for;
- **operations that compile to nothing are still refused**, by the planner's own
  `ErrNothingCompiled` — that is a driver reporting success for work it did not do;
- **a derived down of an up that now emits nothing is refused** with `ErrNothingToRun`, since a
  migration that branches on live data can emit nothing when asked again, and only an author may say
  there is nothing to undo;
- **a `.sql` migration cannot reach it**: an empty `-- migr:down` section means no down was authored.

### E. Unsafe steps are marked by a warning sent in both modes

Swap (both directions), contract's up and expand's down each send a `host.log` warning naming the
step and what it breaks for an app version still serving — *"unsafe step: swap: "users"."email" now
names the new column …"*. migr prints warnings at its default verbosity, and a phase sends its warning
in **plan mode too**, so `migr plan` shows it before `migr up` runs it. Contract's `drop_column` is
also gated by `allow_destructive`, as any drop is.

**The backfill is gated as well, and that was found rather than planned.** `data_transform` is
destructive by kind (invariant #7, ADR 0077), and the engine cannot know that its target is the empty
column the expand just added. Exempting it would need the gate to read the live schema or a marker
the IR has no field for, so a project running this sequence permits `data_transform` (and
`drop_column` for the contract) the way it permits any destruction: in `migr.yaml`, where a review
sees it, or for one run with `--allow-destructive`.

### F. Measured

- Both SDKs emit byte-identical IR for six fixtures (`test/conformance/runtime`, 120014–120019): the
  four keep-name phases, a rename form's expand carrying a default, and its contract after a `lower`
  backfill, which states the irreversible reason.
- On SQLite, untagged in `cmd/migr`: 200 rows replaced phase by phase, the backfill in 5 chunks of
  50, the swap's two `alter_column`s compiled as table rebuilds beside its renames, the contract
  refused until `drop_column` is permitted, a check migration reading every row's own value under the
  original name, then `migr down` over all five restoring the snapshot exactly and the whole sequence
  running a second time.
- On PostgreSQL 17 through the whole stack, and on MySQL 8.0 and MariaDB 11.4 through `engine.Apply`
  and `engine.Rollback` over the same IR.

## Consequences

- **The phases are ordered by version and by nothing else.** Nothing stops `migr up` running all
  four in one invocation against a database whose app has not been redeployed in between — which is
  the outage M13-T7 makes unrepresentable. Until then the warning is the guard, and the docs say the
  deploy has to order the phases.
- **Dual writes are the app's.** Rows the old app writes after the backfill run reach the new column
  only if the app writes both; a second backfill phase may be registered before the swap (the
  transform is idempotent and chunked), and the swap's `NOT NULL` fails loudly on any row left behind
  where the column ends required. Triggers were rejected for the reason ADR 0014 rejects them.
- **On MySQL and MariaDB the swap is not one moment.** Every DDL statement commits on its own, so
  between the two renames the column name an app reads does not exist. The warning says the swap is
  unsafe without saying this, because on PostgreSQL and SQLite it is atomic and the step is unsafe
  for the app-version reason alone.
- M13-T7 attaches a declared phase to these four types: they already know which phase they are.

## Rejected

- **Helpers called inside a hand-written `Up`.** More flexible, and nothing would stop all four
  phases landing in one migration — which also loses chunking for the backfill.
- **A phase declared on the wire now.** That is M13-T7's *"a migration can declare a required prior
  phase"*, and doing it here would bump the protocol for a field nothing yet reads.
- **The backfill declared irreversible.** No engine change, but `migr down` would halt at every
  expand/contract sequence ever applied, and `migr resolve` would become the routine way past one.
- **A trigger keeping the new column in step.** Synchronous work on every production write for as
  long as the change takes, which is the cost ADR 0014 refuses for shadow tables.
- **Exempting the backfill's `data_transform` from the destructive gate.** See E.
