# 0086. A plan shows each operation's lock cost, and `--require-safe` fails one above the ceiling

## Status

Accepted

## Date

2026-10-20

## Context

M13-T6 asks for two things:
- `migr plan` annotates each operation with its lock verdict, whether it rewrites the table, and
  which decompositions were applied;
- `--require-safe` exits 5 if any operation exceeds `safety.max_lock_impact`, so a CI job can block a
  merge that would take production down.

What existed:
- `migr plan` resolved `caps.Resolve(op)` at the render site and printed the support axis only. It
  left the lock axis out on purpose (M5-T4): every `.sql` operation is `raw`, which is `unknown`
  everywhere, so the column would have read `unknown` on every line.
- `capability.LockImpact.AtMost` and `safety.max_lock_impact` (default `brief_exclusive`) existed,
  and nothing read either.
- Exit code 5 was declared and had no producer (ADR 0029).
- Two things can make an operation cheaper than its declaration, and nothing read either of them:
  - `driver.Assessor` (ADR 0081) lowers a MySQL verdict that depends on the table;
  - a rewrite's own `Lock` (ADR 0085) is what a rewritten operation runs at.
- `migr validate --connect` (ADR 0064) already opened a session for the capability set.

## Decision Drivers

- The gate reads the cost the run would actually pay, never a cost it would not.
- An unrecognized verdict satisfies no ceiling, and `unknown` ranks as `table_rewrite` (ADR 0081).
- Planning stays deterministic. The one I/O-bound step, assessment, is a preview's annotation, and a
  failed read keeps the verdict at its worst.
- One answer per operation, read by `migr plan`, by `migr validate` and by the gate alike.

## Considered Options

### A. Where the verdict lives
- **A1: on `plan.Step`, filled by a `Planner.Capabilities` field** ✔. ADRs 0021 and 0064 reserved
  this. `Step.Lock()` returns:
  - nothing (`lock_free`) for an operation on a table this migration creates;
  - else the rewrite's cost where there is one;
  - else the verdict's.

  `Step.Verdict` keeps the declaration, so a plan can say what a rewrite saved.
- A2: resolve at each render site, as M5-T4 did. **Rejected**: the gate, the plan and validate
  would each combine the verdict, the rewrite and the assessment by hand, and three combinations
  drift.

### B. A table the migration creates
- **B1: an operation on it costs nothing** ✔. Nothing can be waiting on a table that did not exist
  when the migration began. This is ADR 0085's created-here rule read for cost rather than for a
  rewrite. Without it, every migration that creates a table and indexes it fails the default
  ceiling: PostgreSQL's plain `add_index` is `long_exclusive`, and it is deliberately not rewritten
  there.
- B2: judge it as declared. **Rejected**: measured on the Go e2e project, `--require-safe` failed on
  the first migration of a fresh project.

### C. Assessment
- **C1: the first queued migration only** ✔. This is the `~` rule (ADR 0069) and TASKS' note for
  this task. A preview applies nothing, so only the first migration meets its tables as they will
  be.
  - A step that is rewritten, on a new table, or unconditional is never assessed.
  - A failed read keeps the worst and is carried as `Planned.Unassessed`. The CLI logs it as a
    warning and `--json` carries it.
- C2: assess every queued migration. **Rejected**: a table an earlier queued migration changes is
  judged as it is now, which is a guess presented as a measurement.

### D. The gate
- **D1: `engine.Exceeding(plan, ceiling) []Breach`, pure** ✔. It is a sibling of `Unsupported` and
  `Destructive`.
  - It skips an operation the server refuses, since the capability check reports that.
  - A step with **no** verdict exceeds every ceiling, so a planner never given capabilities fails
    closed.
- **`engine.LockImpactError`** carries the ceiling and every breach under its migration.
  `ExitCode` resolves `engine.ErrLockImpact` to 5, as 3 and 4 are resolved, so no command tags it by
  hand. `--json` renders a `lock` sub-document.
- D2: a lint rule. **Rejected**, as M10-T5 recorded: a warning can be ignored, and a gate that can be
  ignored is not one.

### E. Where the flag goes
- **E1: `migr plan` and `migr validate`** ✔.
  - `plan --require-safe` judges what this database would run next, assessed, after printing the
    plan. It implies `--safety`, so the operator sees what failed.
  - `validate --require-safe` implies `--connect` and adds a `lock` check after `capability`.
- E2: `migr up --require-safe` as well. **Rejected for now**: up's gates refuse what is wrong or
  unpermitted, and nothing yet asks a run to refuse what is merely costly. It would also have to
  assess each migration as the run reaches it, which is a different mechanism from a preview's.
- The flag is registered on those two commands alone, as `--allow-destructive` is (ADR 0063).
  Elsewhere it would be accepted and ignored.

### F. `migr validate` specifics
- **The cost is rewrite-aware.** The session's `driver.Rewriter` is kept after the session closes:
  it is pure by contract (ADR 0085). A PostgreSQL `add_index` is therefore judged as the lock-free
  concurrent build `migr up` runs. There is no `Assess`, because validate reads no table state.
- **The up half alone is judged.** The inverse of every `create_table` is a drop, so judging a down
  would flag every project, which is invariant #7's reason. Every migration's up half is judged,
  applied ones included, since validate reads no history (ADR 0027).
- **Exit 2 wins over 5.** A broken migration is the argument being wrong, so it is fixed first. Exit
  5 means the ceiling is the only thing wrong. Both are listed.

### G. `--safety`
- **It gates the text rendering and nothing else** ✔. `--json` always carries `lock`,
  `declared_lock`, `rewrites_table`, `assessed`, `new_table`, `over_ceiling` and `max_lock_impact`.
- The heading reads, for example:
  - `add_index  native  lock_free (declared long_exclusive, rewritten)`
  - `add_column  native  table_rewrite (read from the table) -- rewrites the table`
- `▲` marks a breach, and a closing note says where the verdicts came from.

## Decision Outcome

A1, B1, C1, D1, E1, and F and G as stated.

Measured on PostgreSQL 16 and 17, through the Go e2e project:
- `plan --require-safe` and `validate --require-safe` pass under `brief_exclusive`: a rewritten check,
  a concurrent index, a nullable `add_column`, and an index on a table the first migration creates;
- with `concurrent_index: off`, both exit 5, naming the `add_index` at `long_exclusive`.

Measured on MySQL 8.0 and MariaDB 11.4: a preview queuing two migrations that each `add_column` to a
plain table reads the first as `brief_exclusive` (assessed) and the second as `table_rewrite`.

## Consequences

### Positive
- Exit code 5 has a producer, and a CI job can block a merge on it.
- The lock column says something: rewritten, assessed and new-table operations read cheaper than
  their declaration, and only where the run would really pay less.

### Negative
- **A `.sql` project passes only at `max_lock_impact: table_rewrite`**, since every `raw` is
  `unknown`. That is the honest answer about opaque SQL, and it is stated in the note and the
  failure.
- `validate --require-safe` judges applied migrations too, so a project that once accepted a costly
  operation must raise the ceiling or rewrite it. There is no per-operation acknowledgment yet.
- An operation on a fresh table is `lock_free` even on an engine whose DDL commits as it goes. There
  the table is visible between statements, but it is empty and nothing reads it yet.

### Neutral
- `migr up` is not gated.
- A MySQL retype is still declared `table_rewrite` even where the expansion would widen in place
  (ADR 0016). The verdict on the step is where that could be refined.
