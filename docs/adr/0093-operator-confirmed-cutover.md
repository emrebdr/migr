# 0093. A held shadow table is a sixth state, its lag is recorded by the run holding it, and `migr cutover` confirms it

## Status

Accepted

## Date

2026-10-21

## Context

M13-T14's done-when:
- a shadow-table migration holds in sync after the copy and performs the rename only on an explicit
  confirmation;
- the hold survives a process restart;
- `migr cutover` reports which migrations are waiting and how far behind each shadow is, and confirms
  one by name;
- `--unattended` opts out, for CI and small tables.

The task adds two requirements of its own:
- **The lag figure is the point.** The operator is authorizing the one irreversible step in the system,
  so how current the copy is has to be measured and shown, not asserted.
- **`migr up` returning no longer means the schema changed.** The exit codes, the reconciled states and
  `migr status` all need a way to say "waiting for confirmation", and it has to be a sixth
  `history.State`.

[ADR 0089](0089-shadow-table-lifecycle.md) built the hold. Its loop is: read the log, apply what it
read, read a `confirmed` column the engine never writes, and sleep. What it left open:
- **Nothing wrote the confirmation.** `history.Shadows.ConfirmShadow` existed on all three stores, and
  only tests called it.
- **Whether confirming needs the migration lock** was an open question.
- **The lag reached only a log line.** `capture.Batch.Behind` was measured on every read and kept in
  memory, so no other process could show it.
- **A held migration reconciled as `dirty`**, since `history.Reconcile` reads the run log alone.
- **Ctrl-C detached and exited 1**, the code of a failure.

## Decision Drivers

- An operator must never act on a lag figure that is out of date without being told it is.
- A command that confirms must not wait on the run it is confirming.
- A deploy script must be able to tell "changed" from "not changed yet" by the exit code.
- No switch over `history.State` may have a `default` (ADR 0019), so the compiler finds every place
  that has to handle the new state.

## Considered Options

### A. How waiting is represented
- **A1: a sixth `history.State`, `waiting`** ✔ (by the task):
  - it is derived, like every state: **a dirty forward run whose shadow row is at stage `syncing`**;
  - `history.Read` also reads the shadow rows where the store keeps them, and `Log.WithShadows`
    carries the versions that are syncing, so `Reconcile`'s signature and its three callers don't
    change;
  - a migration still **copying** stays `dirty`, since there is nothing yet to confirm. A dirty
    rollback, a cut-over row and an applied migration never wait.
- A2: a flag on `dirty`. **Rejected** for the reason M4-T3 made dirty a state of its own: `migr status`
  has to print something true, and "between states" is not true of a held copy.

### B. Where the lag figure comes from
- **B1: the run holding the copy records it** ✔ (by agreement):
  - `ShadowState.Behind` is stored as a new `behind` column (milliseconds), from the batch's own
    `Behind`, which is the server's measurement;
  - while syncing, `UpdatedAt` means "when the log was last read";
  - an idle hold saves its row whenever the position or the lag moved, and **at least every
    `engine.ShadowHeartbeat` (5 s)**, so a hold of days is not a write a second;
  - **a reading older than six heartbeats (30 s) is shown as stale**: *no run is holding it in sync
    … migr up resumes the hold*. A dead holder is never shown as current.
- B2: `migr cutover` measures the lag itself, through a new driver interface. **Rejected.** MySQL can
  say how many transactions or bytes behind a recorded position is, but not how long, without reading
  the events. PostgreSQL's answer (M13-T15) would read the slot instead. It would be a third measurement
  beside the one the holder already takes.

### C. Whether confirming takes the migration lock
- **C1: no** ✔ (decided here). The run holding the copy holds the migration lock for the whole hold
  (ADR 0089 C), so a confirmation that took it would wait for the very hold it ends. The confirmation is
  one column, read by the holder and never written by the engine, so the write can't race a save.
- C2: take it. **Rejected:** it deadlocks the only case that matters.

`migr cutover --confirm VERSION`:
- refuses a version nothing carries (exit **2**, a typo);
- refuses one that is not waiting, for example still copying (exit **1**, the world isn't ready);
- says so when it is already confirmed (exit **0**);
- otherwise writes the confirmation and reads it back, so an abandon in between is exit **1**.

It prints the lag the confirmation was given against, and says what performs the swap: the live holder
at its next read, or `migr up` once resumed.

### D. What a detached hold exits with
- **D1: a new exit code, 6**, *waiting for a cutover confirmation* ✔ (by agreement).
  - A context canceled (Ctrl-C or SIGTERM) while the stage is `syncing` returns `engine.WaitingError`,
    which wraps `engine.ErrAwaitingCutover`.
  - The run is still marked dirty and the stream is closed, not dropped, exactly as before.
  - The `--json` failure document carries a `waiting` sub-document.
  - Any other failure while syncing stays a `ShadowError` and exits 1. Cancellation while copying is
    unchanged.
- D2: exit 0, as a release hold does. **Rejected:** a script reading 0 as "the schema changed" would be
  wrong.
- D3: exit 1, as before. **Rejected:** a detach is not a failure, and nothing would tell the two apart.

### E. `--unattended`
- **E1: a `migr up` flag, or `MIGR_UNATTENDED`** ✔ (by agreement). A shadow-table migration cuts over
  at the first read that finds nothing new, which is when the stream is caught up, without reading a
  confirmation.
  - It skips the confirmation, not the catching up, since the swap catches up under its lock.
  - The summary says *cut over unattended*, and `--json` carries `unattended`.
  - A `MIGR_UNATTENDED` that `strconv.ParseBool` cannot read is exit **2**.
  - It is registered on `up` alone, for `--allow-destructive`'s reason.
- E2: a config key. **Rejected:** a project-wide switch puts back the risk the confirmation removes,
  and it is invisible in the CI script that relies on it.
- E3: a per-migration declaration. **Rejected:** it is a protocol and SDK change, for what is a decision
  about a run.

## Decision Outcome

A1, B1, C1, D1, E1.

**What else the sixth state touched:**
- **`migr up`** carries a waiting migration on: the hold resumes from the recorded position, and a
  confirmation given while nothing held it is read at the first poll.
- **`migr down`** is refused while one waits, naming both ways on (`engine.ErrWaiting`, exit 1).
- **`migr resolve --rolled-back`** abandons a waiting migration as it does a dirty one.
- **`migr diff` and `migr seed`** each name the cutover among their remedies.
- **`migr status`** prints a waiting migration's lag, how long ago it was read, whether it is
  confirmed, and the command that confirms it. `--json` carries `behind_ms`, `read_at`, `held` and
  `confirmed`.

**"Survives a process restart" is what the parts above add up to.** Nothing in the hold depends on the
process that started it:
- the position, the lag and the confirmation are rows;
- the capture resource is kept on a detach;
- the next `migr up` reads on from the recorded position.

The end-to-end suite holds this on mysql80 with a real `bin/migr` process:
1. it is interrupted (exit 6, `waiting`);
2. it is restarted and killed (`migr cutover` reports the reading as stale);
3. it is restarted, then confirmed with `migr cutover --confirm`, and cuts over.

## Consequences

### Positive
- An operator authorizing the swap sees how far behind the copy was, how old that reading is, and
  whether anything is holding it.
- A deploy script reads exit 6 as "waiting", where it previously read a failure.

### Negative
- **An existing `<history.table>_shadows` table has no `behind` column.** The project is pre-alpha
  with no release, so there is no upgrade path, and an older table has to be dropped.
- An idle hold writes its row every five seconds.
- The staleness threshold is a fixed rule, not a measurement of the holder's liveness. A holder stalled
  for over 30 s inside one read is shown as stale, which is pessimistic rather than wrong.

### Neutral
- `migr cutover` is a report: it takes no lock and starts no runtime.
- The shadow-table e2e on MySQL that confirms through the store directly stays as it was. That run is
  in-process, and the new suite covers the command.
