# 0076. Seeds: a second history on the same session, scoped by directory, run after every migration

## Status

Accepted

## Date

2026-10-13

## Context

M12-T1's done-when is that *"seeds live in a separate directory with separate history tracking, are
environment-scoped, and never mix with schema migration ordering."*

Two earlier decisions left room for this. [ADR 0013](0013-migration-history.md) C2 made the history
table's definition `history.Schema(table)` so that *"seeds get a second table of the same shape for
free"*, and `internal/history`'s own test holds that name to being configurable for exactly this
reason. `internal/cli/doc.go` assigned `migr seed` to this task, so it is the eleventh command and
the exit-code matrix's twelfth.

The split with M12-T5 was decided with the user: **this task applies seeds**, with `--dry-run`,
`--step` and `--to`, and **M12-T5 lists and rolls them back**. The task's done-when needs a run to be
observable at all — *separate history tracking* is a statement about rows a run writes.

### What was measured first

Each of these changed the plan; the first three were found by reading the code and confirmed by a
test, the last two by running the command against a real SQLite database before any test existed.

**1. The history table's name keys the migration lock.** PostgreSQL's advisory key is
`sha256(schema + table)` (`postgres/lock_advisory.go`), and MySQL's `GET_LOCK` name is derived from
the database and the table the same way. A seed run that opened its own session with
`HistoryTable: migr_seeds` would take a **different lock**: a seed would run while `migr up` held the
lock it believes excludes every other run. That interleaving is the mixing the done-when forbids.

**2. `Introspect` excludes exactly one table**, the one the session was opened with
([ADR 0069](0069-per-driver-introspection.md)). A `migr_seeds` table would appear in every snapshot,
and `migr diff` would propose dropping the seed history.

**3. The Go runtime built one runner per module**, at `.migr/go`
([ADR 0035](0035-generated-go-runner.md)). The stamp covers the migrations directory, so a seeds
package in the same module would replace the migrations' runner on every alternate command and race
it under two concurrent ones. It was correct — the stamp guarantees a hit is the right binary — and
slow, which is a cost every Go project with seeds would pay on every command.

**4. A config naming every environment's database is not the common case.** The first design refused
any subdirectory of `seeds/` that no database in `migr.yaml` runs under, as the typo guard. Run by
hand against the usual shape — one `default` database whose `environment: ${APP_ENV}` varies by
machine — it refused `seeds/production/` on a development machine, where production is perfectly
real. Hence `seeds.environments` (C).

**5. A database seeded under one environment and then another is not refused.** The plan said it
would be: the first environment's own seeds reconcile as *missing* under the second. Measured, the
engine's apply path leaves a missing version where it is — as `migr up` does — and the summary says
`1 missing`. That is consistent with migrations and honest in its output, so it stands, and the
branch written to explain a refusal that never comes was deleted.

## Decision

### A. One session, two stores, one lock

`driver.Options` gains `SeedTable` (default `migr_seeds`) and `driver.Conn` gains
`Seeds() history.Store`, bound at `Open` beside `History()` over the same pinned session. Every
driver's store was already parameterized by table name, so each driver's change is one store and the
`checkHistoryTable` refusal applied to the second name.

The lock is **still derived from the history table alone** — `Options.Namespace()` is unchanged — so
seeds and migrations exclude each other through one lock. A test per keyed engine holds that a seed
table renamed moves no lock, and PostgreSQL's integration suite holds a session's lock and watches
`migr seed` exit **3** beside it.

`internal/engine` is **unchanged**. `internal/cli` hands it a `seeding` session whose `History()`
returns `Seeds()`; everything else, the lock and the transactions included, is the real session. Apply,
record-before-commit, dirty, drift and resume therefore hold for a seed exactly as for a migration,
with no seed branch anywhere in the engine — and invariant #6 applies to seeds too: an edited seed is
exit **4**.

### B. `Introspect` excludes both of migr's tables

All three drivers exclude the history table and the seed table, each by the same parameterized query
it already had. A Go project on SQLite seeds a database and then runs `migr diff`, which finds nothing
to write.

### C. A directory per environment, and the environment is the database's

```yaml
seeds:
  dir: ./seeds                         # default, resolved against migr.yaml
  table: migr_seeds                    # default
  environments: [development, production]  # optional
databases:
  default:
    environment: ${APP_ENV:-development}   # optional; defaults to the key
```

Files directly in `seeds/` run in every environment; `seeds/<environment>/` runs only there. A run
reads exactly two flat directories, each held to §9's discovery rules, and two seeds claiming one
version **across** them are refused. The environment is `databases.<name>.environment`, defaulting
to the database's own key, so a `migr.yaml` that already names `development` and `production`
databases needs no new key at all.

A subdirectory naming no environment is **refused**, exit 2, listing the environments — §9's argument
against skipping `20260727T153000-add-users.ts` applied to `seeds/prodution/`, which would otherwise
hold seeds nothing ever runs and be found out in the environment it was meant for. What counts as an
environment is `seeds.environments` where it is given and the databases' environments where it is
not; where it is given, a database outside it fails the config load. That is measurement 4.

### D. A seed runs after every migration, and only then

`migr seed` reconciles the **migration** history first and refuses, exit **1**, unless every
migration is applied — naming `migr up` for pending, `migr resolve` for dirty, and the file for
missing and drifted, as `migr diff` does for the same reason. That is the whole mechanism behind
*never mix with schema migration ordering*: a seed always runs against the schema every migration
leaves, so no seed can sit between two of them. A project with **no** migrations at all may seed.

Seeds are ordered **among themselves alone**, by `migration.Order` over the seed set. A seed that
declares a dependency on a migration names a version that set does not carry, which is the existing
*edge naming a version nothing carries*, exit 2. A seed and a migration may share a version: they are
two histories in two tables, and nothing reconciles one against the other.

The precondition is read before the engine takes the lock, because `engine.Apply` takes it and a
session refuses a second. A `migr up` that wins the lock in between leaves a schema with more of the
migrations applied rather than fewer, so the seed still runs after every migration it knew of; a new
migration file arriving in that window is the only case, and the next `migr seed` refuses it.

### E. A seed emits data, and a schema change in one is refused

A source wrapper refuses, exit 2, any operation a seed emits that is not data — today that is every
kind but `raw`, since M12-T2's data operations do not exist yet. A table a seed created would be one
no migration knows about, which `migr diff` would then propose dropping. `raw` cannot be judged, so
the guarantee is about what the IR can say and no more.

`--allow-destructive` is **not registered**. A seed's allowance is `raw` and nothing else, and every
`raw` seed would otherwise need the flag — [ADR 0063](0063-destructive-operations.md)'s argument
against a per-file marker, which is that a marker mandatory on every file in a format is no marker.
The typed kinds the flag permits are refused by E before the gate would see them.

### F. One Go runner per directory

`.migr/go/<directory relative to the module>/`, with separators spelled `__` and the module's own
root spelled `_module`, so `migrations` and `seeds` build apart. A collision between two such names
— a directory literally called `a__b` beside `a/b` — costs a rebuild and never a wrong binary, since
the stamp covers the directory. The ignore file stays at `.migr/`, where ADR 0035 put it.

### G. A seed that only calls `host.db.exec` still meets `ErrNothingToRun`

As a migration does, and with the same consequence ADR 0032 states: the statement it sent is
committed and nothing records it. Both SDKs emit `raw`, which is transactional and recorded with the
row, so a seed has a correct way to write data today; M12-T2 is where it gets a typed one.

## Alternatives rejected

- **A field on `history.Record` instead of a second table.** ADR 0013 named fields as the cheap way
  to scope a row later, but every store would then filter one log two ways, `history.Reconcile`
  would need to be told which half it is folding, and a history row's shape is a format two migr
  versions must both read. A second table of the same shape costs a parameter that already existed.
- **A second session for seeds.** Measurement 1: it takes a different lock on two of three engines.
- **`engine.Options.Store`.** It would let any caller run migrations against an arbitrary store; the
  wrapper is local to the one command that needs it and leaves the engine with nothing to get wrong.
- **`--env` / `MIGR_ENV` naming the environment per invocation.** Nothing would tie a database to its
  environment, so one database could be seeded as two by a flag left over in a shell.
- **Each seed declaring its environments** (`-- migr:env:` in `.sql`, a method in both SDKs). A
  protocol change and two SDK changes for what a directory already says, and a scope that cannot be
  seen by listing the tree.
- **Skipping an unknown subdirectory.** Measurement 4's opposite failure: a typo that silently seeds
  nothing.
- **Running pending migrations first, then seeds.** A seed command that applies schema changes is a
  seed command that mixes with migration ordering; the operator runs `migr up`, which is gated.

## Consequences

- `driver.Conn` has a method every implementation owes, fakes included; nine were updated.
- `Introspect`'s exclusion is two names on every engine, which is invariant #3's SQL staying inside
  each driver.
- A Go project's runner cache moved from `.migr/go` to `.migr/go/migrations`, so an existing project
  rebuilds once.
- ~~`migr seed` cannot list or undo a seed until M12-T5~~ — M12-T5 added `migr seed status`,
  `history`, `down` and `resolve` (ADR 0080). `migr status` and `migr history` still do not
  mention seeds at all, by design rather than omission, and a test holds that.
- ~~PostgreSQL's integration suite drops its seed table through `pgtest.Drop`, the second thing nothing
  in a migration can clean up, until M12-T5 can roll a seed back.~~ It rolls its seed back with
  `migr seed down` since M12-T5 (ADR 0080).
