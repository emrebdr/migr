# 0097. An unattended cutover counts a stream within safety.cutover_lag as caught up, and the guide's lock tables are generated

## Status

Accepted

## Date

2026-10-23

## Context

M13-T13's done-when asks for a zero-downtime guide holding per-engine safe/unsafe operation tables,
the expand/contract playbook, the CI gating recipe, and the cutover workflow with what its lag figure
means. Two things stood in the way of writing it honestly.

**The unattended cutover did not work under load.** ADR 0093 made `migr up --unattended` cut over at
the first read of the held log that found nothing new. ADR 0096's suite ran eight writers against
the table, and no such read ever came: each poll found the writes made since the last one. A guide
that recommended `--unattended` for CI would have recommended a run that holds forever on any table
worth migrating online.

**A hand-written table drifts the day a verdict changes.** Every verdict is resolved by a driver
from the server version and the operation's payload, and PostgreSQL's rewrites lower several of
them. Copying those answers into prose produces a table that is correct once.

## Measured first

Each stream's `capture.Batch.Behind` (read from the code):

| Engine | `Behind` |
|---|---|
| PostgreSQL | 0 when a read reached the WAL end as the read began; otherwise the time since the last change read committed. |
| MySQL, MariaDB | 0 when a read reached the binary log's end as the read began; otherwise the server's clock minus the last event's timestamp, in **whole seconds**. |

So `Behind` already answers "is this read caught up", and is 0 on a healthy hold whatever the write
rate. Under the zero-downtime suite's sixteen clients, the unattended shadow case's held reads were:

| Server | Held reads (changes, `Behind`) | Worst client query |
|---|---|---|
| postgres17 | 384 at 0s, 143 at 0s | 77 ms |
| mysql80 | 136 at 0s, 16 at 0s | 119 ms |
| mysql80gtid | 203 at 0s, 18 at 0s | 162 ms |
| mariadb114binlog | 320 at 0s, 31 at 0s | 28 ms |

Every read carried changes, so ADR 0093's rule would have held each of them forever; every one read to
the log's end, so the new rule cut over at the first held read, inside the 1.5 s budget.

## Decision Drivers

- `--unattended` should cut over on a table under sustained writes, which is the table a shadow
  migration is for.
- The cutover's catch-up replays what remains **under its lock**, bounded by `safety.lock_timeout`.
  Whatever counts as caught up must fit that bound.
- A table in the guide must be the verdict `migr plan` prints, or it is worse than no table.

## Considered Options

### A. When an unattended run counts the copy caught up
- **A1: at the first read that found nothing new, or whose `Behind` is at most `safety.cutover_lag`**
  ✔ (by agreement). A read reaching the log's end reports 0 and cuts over; one that stopped short by
  more than the bound keeps holding.
- A2: reuse `safety.replica_lag_threshold`. **Rejected:** its 5 s default is longer than the 3 s
  `lock_timeout`, so a catch-up that size could time out on every attempt of the cutover.
- A3: a row-count threshold. **Rejected:** no stream reports a count of what remains, only how far
  behind it is.

### B. The key
- **`safety.cutover_lag`, default 1 s, or half of `safety.lock_timeout` where that is shorter.** An
  explicit value must be positive and shorter than `lock_timeout`. Defaulting it from `lock_timeout`
  keeps a project that shortened `lock_timeout` from being refused over a key it never set.
- Zero in `engine.Options.CutoverLag` keeps ADR 0093's rule, a read of nothing new, for a caller
  that passes no bound.
- The run reports how far behind it cut over: *cut over unattended, 0s behind the log*, and
  `behind_ms` under `--json`.

### C. The guide's tables
- **C1: generated from each driver's declaration, and held by a test** ✔ (by agreement).
  `drivertest.LockTable` resolves `drivertest.LockCases()` — every kind, plus the payload variants
  that change a cost — through the `capability.Set` `Open` would build and the session's
  `driver.Rewriter`, and compares the rendered markdown with the block between
  `<!-- migr:lock-table <engine> -->` and `<!-- /migr:lock-table -->` in `docs/zero-downtime.md`.
  `-update` rewrites the block, so `make golden-update` regenerates every table. Each driver's
  untagged test builds its declaration with no server.
- C2: hand-written tables. **Rejected:** they drift.

## Decision Outcome

A1, B, C1.

## Consequences

### Positive
- `--unattended` cuts over under sustained writes; the zero-downtime suite gained an unattended
  shadow case with no confirmer to hold it to that.
- A verdict change arrives as a failing test naming the guide, and as a diff to the guide.

### Negative
- On MySQL and MariaDB `Behind` has whole-second resolution, so any `cutover_lag` under 1 s means
  "within the same second".
- A table describes one server version per engine: PostgreSQL 16 and 17 (asserted identical), MySQL
  8.0 with a binary log, MariaDB 11.4 without one, SQLite as linked. Another version's verdicts are
  `migr plan`'s to print.

### Neutral
- Writing the guide found CLAUDE.md claiming `create_table` is `lock_free` on all three drivers.
  SQLite declares it `brief_exclusive`, since one write lock covers the whole database file. The
  declaration is right and the sentence was corrected.
