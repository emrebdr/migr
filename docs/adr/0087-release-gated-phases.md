# 0087. A step of a change across deploys runs under a release after its prior step's

## Status

Accepted

## Date

2026-10-20

## Context

M13-T7 asks for two things:
- a migration can declare a required prior phase and a release gate;
- a contract-phase migration **refuses to run** until its expand phase is recorded as applied and the
  gate is satisfied.

A contract applied while the old app version is still serving is an outage, and the framework
should make that unrepresentable rather than documented.

What existed:
- M12-T4 (ADR 0079) built `ReplaceColumn` / `replaceColumn` in both SDKs. Expand, backfill, swap and
  contract are each a migration, and each already knew which phase it was. Nothing read it, so all
  four could run in one `migr up`, and the only guard was a warning.
- A batch is one invocation, and nothing recorded which **deploy** an invocation belonged to.
- `migration.discover` carries what a migration declares, and adding an optional result member is not
  a protocol break (protocol §10.2).
- The history table has no schema evolution. The checkpoint table (ADR 0078) is the precedent for
  state that lives beside it.

**migr cannot observe the app.** The fact that matters, that the old version is no longer serving, is
outside it. Something the operator supplies has to stand for it, and the question is what.

## Decision Drivers

- The steps of one change must not be able to land in one deploy, rather than being merely unlikely
  to.
- A change partway through its deploys is a **normal** state, so it must not fail a deploy pipeline.
- Language independence: both SDKs declare the same steps, and the engine decides.
- No IR change and no protocol version bump.

## Considered Options

### A. What the gate is
- **A1: a release named per run** ✔. `migr up --release <id>` (or `MIGR_RELEASE`) records which
  deploy each batch belongs to. A step runs only where its prior step belongs to a **different**
  release. CI already has a build id, and the deploy that applies an expand then rolls out the app
  that writes both columns, so the next deploy's run is the first that may backfill.
- A2: an explicit confirmation (`migr confirm <change> <step>`). **Rejected**: every step waits for a
  person or a scripted call, and in a pipeline that call is the same build id under another name.
- A3: an earlier batch. **Rejected**: two `migr up` runs in one deploy pass it, so it narrows the
  outage rather than making it unrepresentable.
- A4: a minimum age. **Rejected**: time says nothing about whether the app rolled out.

### B. What declares a step
- **B1: a `phase` result member of `migration.discover`, read from the migration** ✔. It holds
  `change`, `name` and `after`, the last omitted where the step waits on nothing.
  - Go has an optional `Phased` interface and TypeScript an optional `phase` property, each refused at
    load if malformed.
  - `ReplaceColumn`'s phases declare themselves under `<table>.<column>`, each `after` the one before
    it.
  - The declaration is in the file, so editing it moves the source digest and invariant #6 holds with
    nothing added.
- B2: a protocol version bump. **Rejected**: §10.2 makes an optional member no break. An engine that
  does not know it ignores it, and an SDK that does not send it declares nothing.
- B3: a `.sql` marker. **Deferred**: no `.sql` migration can be a `ReplaceColumn` phase, and the
  hand-written case is served by the two SDKs first.

### C. The chain
- **Each step waits on the one before it** (by agreement): backfill after expand, swap after backfill,
  contract after swap. A backfill in the deploy that expanded misses every row the old app writes
  afterwards, and a contract in the deploy that swapped drops a column the old app still reads.

### D. What a waiting step does to the run
- **D1: it holds, and the run succeeds** ✔ (by agreement). The run applies everything before the
  waiting step, stops there, reports `⏸ held` with the reason, and exits 0. Everything after it waits
  too, since order is kept.
  - `--json` carries `held`.
  - `migr plan` and `--dry-run` stop at the same step.
  - `-v` logs it.
- D2: refuse with exit 1. **Rejected**: every deploy partway through a change would fail its pipeline,
  which is how operators learn to ignore exit 1.

### E. Recording the release
- **E1: `<history.table>_releases`, `(batch, release, recorded_at)`, keyed by batch** ✔. It is created
  from IR in `Ensure`, excluded from `Introspect`, and implemented by all three drivers'
  `history.Releaser`, exactly as the checkpoint table is.
  - The release is written **in the transaction of the batch's first applied row**, so a batch that
    applies nothing records nothing.
  - A history with no table reads as no releases.
- E2: a column on the history table. **Rejected**: there is no schema evolution, and every existing
  history would need one.

### F. The rules at the edges
- **A run whose queue holds any step must name a release**, or it is refused before anything runs:
  `engine.ErrNoRelease`, exit **2**, naming the remedy. That includes an expand, so every step is
  recorded under one.
- **A prior step whose batch has no recorded release counts as an earlier release.** Only a run from
  before releases were recorded can leave one, since every run holding a step names one. The other
  reading would strand every change begun before this build, with no way for any deploy to end the
  wait.
- **A rollback is never held**, being the operator's instruction (invariant #7's reasoning). **A resume
  is never held**, since it finishes what a permitted run started (ADR 0063).
- **Declarations are checked at discovery**, exit **2**, naming every file:
  - an `after` no migration declares;
  - one step declared twice;
  - a step waiting on one that runs after it;
  - a blank change or name;
  - a step waiting on itself.
- **Steps run in one invocation are judged by what the invocation already applied.** After expand
  runs under `r1`, the backfill holds with "belongs to release r1, which is this one" rather than "not
  applied yet".
- **On an engine whose DDL commits as it goes**, a hold at a swap, or at the step after one, adds that
  the swap is not one moment: between its renames the column an app reads does not exist. That reads
  `capability.Set.TransactionalDDL()`, not a driver name.

## Decision Outcome

A1, B1, C, D1, E1, and F.

Measured:
- **SQLite, untagged in `cmd/migr`, over 200 rows:** a release per phase advances one phase per
  deploy, and a repeat with the same release holds. `migr down` restores the snapshot, and four more
  deploys carry the sequence through again.
- **PostgreSQL 17:** the same sequence through the whole stack.
- **TypeScript:** a backfill held after its expand in one deploy and run in the next.
- **MySQL 8.0 and MariaDB 11.4:** a contract held after a swap states the two-commit swap, and the
  release row is recorded once.
- **Both SDKs** send byte-identical declarations for fixtures 120014–120019, which their manifests
  pin.

## Consequences

### Positive
- The four phases of a replacement cannot run in one deploy. "Order the phases across deploys" stops
  being the operator's job, the gap ADR 0079 recorded.
- A pipeline mid-change succeeds and says what it is waiting for.

### Negative
- **Deploys must pass `--release`** once a project uses phases, and a run without it is refused.
- **The release stands for the app; it does not observe it.** Two deploys under different releases
  with no app rollout between them pass the gate. That is stated, not guarded.
- `migr status` starts no runtime, so it cannot show a hold: only Go and TypeScript migrations declare
  steps. `migr plan` and `--dry-run` show it.
- `migr resolve` takes no release, so a step it settles is recorded under none, which F reads as an
  earlier release.

### Neutral
- Seeds declare no phases. Their history gets a release table beside it, which stays empty.
- `.sql` has no marker yet.
