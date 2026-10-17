# 0085. A session performs an operation more safely than its plain compile, and the plan says so

## Status

Accepted

## Date

2026-10-18

## Context

M13-T5 asks the planner to rewrite unsafe operations into safe sequences where the engine permits:
- `ADD CONSTRAINT … NOT VALID` followed by `VALIDATE CONSTRAINT`;
- `SET NOT NULL` backed by a validated `CHECK` on PostgreSQL 12+;
- `CREATE INDEX CONCURRENTLY` as the PostgreSQL default.

Every rewrite must be visible in `migr plan`, individually disableable, and never silent.

What existed:
- PostgreSQL compiled all four as plain statements, which are `LongExclusive` (or a scan under an
  exclusive lock, for `SET NOT NULL`).
- `safety.auto_rewrite` was a `bool` that nothing read, and it defaulted to false.
- `plan.Step` already pairs an operation with its statements, which ADR 0021 C kept for this task.

The premise was measured before anything was built. On PostgreSQL 16 and 17 a writer inserted in a
loop, each insert bounded by a short `lock_timeout`, while each statement ran over 15 million rows:

| statement | duration | writes blocked |
|---|---|---|
| plain `ADD CHECK` | 9.5 s | 34 of 35 |
| `ADD … NOT VALID`, then `VALIDATE`, each committed | 9.8 s | 0 of 159 |
| the same two inside **one** transaction | 9.4 s | 34 of 35 |
| plain `SET NOT NULL` | 0.68 s | 2 of 2 |
| validated `IS NOT NULL` check, `SET NOT NULL`, drop the check | 0.68 s | 0 of 10 |
| plain `ADD FOREIGN KEY` | 5.2 s | 18 of 19 |
| foreign key `NOT VALID`, then `VALIDATE` | 5.5 s | 0 of 85 |

`SET NOT NULL` behind a validated check took 0.7 ms on 15 million rows, against 367 ms without one,
so the server skipped the scan. `CREATE INDEX CONCURRENTLY` blocked no write where the plain build
did. The third row is the decisive one. `ADD … NOT VALID` takes `ACCESS EXCLUSIVE`, and a lock is
held to commit, so a `VALIDATE` in the same transaction scans under it.

## Decision Drivers

- A rewrite changes how an operation runs and never what it leaves.
- The rewrite depends on the server version, so it cannot be `Compile`'s, which is version-blind;
  yet planning must stay deterministic and need no database I/O.
- An operator reads, in `migr plan`, that one declared operation became several statements, and the
  key that turns it off.
- No IR change and no SDK change.

## Considered Options

### Option 1: `driver.Rewriter` on the session, applied by the planner

**Pros**

- The session knows its version from `Open`, so a rewrite is pure and still version-aware. This is
  the reason a capability set lives on the session (ADR 0012).
- The planner replaces a step's statements and keeps its operation, so every gate that reads
  operations is unchanged: destructive, unsupported, unrestorable, the derived inverse, and the
  checksum.

**Cons**

- `migr validate` without `--connect` has no session, and so compiles plainly. That is fine, since a
  rewrite never makes an operation invalid.

### Option 2: Decompose into IR operations

**Rejected because:** the IR has no way to say NOT VALID, VALIDATE or CONCURRENTLY, and adding one
would put engine detail into the neutral middle. A rewrite is how one engine performs an operation,
which is a driver's knowledge (invariant #1).

### Option 3: `VALIDATE` in the migration's transaction

**Rejected because:** measured, it blocks every write for the whole scan, exactly as the plain
statement does.

### Option 4: A per-migration opt-out

**Rejected for now:** it needs an IR field and SDK surface. The config switch per rewrite answers
"individually disableable" without either.

### Option 5: MySQL `ALGORITHM`/`LOCK` hints

**Rejected because:** naming an algorithm makes the server refuse what it cannot do that way (errors
1845 and 1846), which is a refusal and not a rewrite. ADR 0059 already keeps the clause out of what
migr sends.

## Decision Outcome

Option 1.

**The contract.**
- `driver.Rewriter.Rewrite(op) (Rewrite, bool)` returns:
  - `Name`: `not_valid`, `not_null_check` or `concurrent_index`, a closed set;
  - the statements;
  - a one-line `Reason`;
  - `Lock`, the sequence's cost, which M13-T6 reads.
- It never answers for an operation `Compile` refuses.

**PostgreSQL.**
- `add_check` and `add_foreign_key` become `ADD … NOT VALID` and then `VALIDATE CONSTRAINT`, the
  `VALIDATE` bare.
- `alter_column` narrowing to `NOT NULL` with no retype, on 12 and later, becomes:
  1. an `IS NOT NULL` check, `NOT VALID`;
  2. its `VALIDATE`, bare;
  3. `SET NOT NULL`;
  4. the check dropped;
  5. then the operation's default change, in its usual order.

  The check's name is `<table>_<column>_nn`, hashed to fit 63 bytes on a character boundary. On 11
  or with a retype there is no rewrite, since the server scans regardless.
- `add_index` becomes `CREATE INDEX CONCURRENTLY`, bare.
- The bare statements are why M13-T4 came first: the engine splits around them.

**The planner.** `Planner.Rewriter` is the session and `Planner.Rewrites` the enabled names.
- `Compile` replaces a step's statements and records `Step.Rewrite` (name, reason, lock, and how many
  statements were declared).
- **An operation on a table this migration created is skipped**, following renames. Nothing waits on
  a table that did not exist, and a split would cost atomicity for nothing.
- Both directions are rewritten.

**Config.**
- `safety.auto_rewrite` is now optional and defaults to true. It is the master switch.
- `safety.rewrites.<name>` is `on` or `off`, defaulting to `on`.
- An unknown name or value fails the load, as `validation.rules` does.

**Never silent.**
- `migr plan` prints `↳ rewritten (safety.rewrites.<name>)` under the operation, with what it became
  and why.
- A closing note names each rewrite used and the key that turns it off.
- `--json` carries `rewrite`.
- `--dry-run` and `migr up` count operations rewritten.
- `-v` logs each rewrite as it runs.

**Failures.**
- A bare statement is not retried after a lock wait (ADR 0084). For `VALIDATE` that is conservative
  rather than necessary.
- A `VALIDATE` that finds violations leaves the constraint `NOT VALID` and the run dirty after the
  `ADD`. Fixing the rows and running `migr resolve --resume` runs `VALIDATE` alone. A resume
  recompiles with the rewrites configured now, so changing `safety.rewrites` between a stop and its
  resume moves the statement positions. That is stated rather than guarded.
- A concurrent build that fails returns `postgres.InvalidIndexError`. It names the index PostgreSQL
  left INVALID and the `DROP INDEX CONCURRENTLY` to run before `migr resolve`, read from `pg_index` on
  the session that survived (ADR 0083).

## Consequences

### Positive

- On a live table, adding a check, a foreign key, a NOT NULL or an index no longer blocks writes for
  the length of a scan. Each driver suite holds that against a control on the plain statement.
- The schema a rewritten run leaves is snapshot-equal to the plain run's.

### Negative

- **A rewritten migration is no longer one transaction.** A failure partway leaves it dirty, as
  ADR 0084 describes.
- **The 30 s `statement_timeout` now bounds a concurrent build too**, so a large index fails and
  leaves an INVALID index until the bound is raised or M13-T11 cleans up.
- `migr validate` offline shows the plain statements.

### Neutral

- MySQL and SQLite rewrite nothing. MySQL already chooses its online algorithm itself, and SQLite
  has a single writer.
- Left to M13-T11: `add_unique` and `add_primary_key` via an index built concurrently,
  `DROP INDEX CONCURRENTLY`, `REINDEX CONCURRENTLY`, the invalid-index cleanup, and lifting the
  no-retry rule for its own builds.
