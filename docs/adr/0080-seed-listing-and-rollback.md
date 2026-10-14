# 0080. Listing, rolling back and settling seeds, as subcommands over the seed history

## Status

Accepted

## Date

2026-10-14

## Context

M12-T5's done-when is that *"seeds run, list, and roll back independently of schema migrations"*.
M12-T1 (ADR 0076) shipped the run: `migr seed` hands the migration engine a session whose
`History()` is the seed store, so applying, recording, dirty, drift and resume are a migration's
own. Every other command read the migration history alone, which left three gaps:

- **nothing listed a seed** — `migr status` and `migr history` deliberately say nothing about one;
- **nothing undid a seed**, so PostgreSQL's integration suite dropped its seed table through
  `pgtest.Drop` to leave the server as it found it;
- **nothing could settle a seed left dirty.** `migr resolve` reads the migration history, finds
  nothing between states and exits 0, so one failed seed refused every later `migr seed` for good.

## Decision

### A. Four subcommands of `migr seed`

| Command | Is | Lock |
|---|---|---|
| `migr seed status` | `migr status` over the seed history and this environment's seeds | none (ADR 0027) |
| `migr seed history` | `migr history` over the seed history | none |
| `migr seed down [--step N] [--to VER] [--dry-run]` | `migr down` over the seeds; the last seed batch by default | the migration lock |
| `migr seed resolve --resume\|--applied\|--rolled-back [--reason] [--dry-run]` | `migr resolve` over the seed history | the migration lock |

Plain `migr seed` keeps applying, with its flags unchanged.

### B. The same machinery, given the seed history

Each subcommand runs its migration twin's pipeline, with the session's connection replaced by the
`seeding` wrapper ADR 0076 built, so `History()` is the seed store. Every session helper — `run`,
`preview`, `reconcile`, `log` — reaches the store through that connection, so nothing in
`internal/engine`, `internal/history` or a driver changed, and the migration commands did not
either. **The lock stays keyed by the history table**, exactly as for `migr seed`.

The renderers are the migration ones, told what they are about: a report carries an unexported
`subject` (noun, table, the command that creates it) whose zero value means migrations, so every
migration report keeps its exact bytes. A seed report names its environment, in its heading and as
`"environment"` in `--json`.

**The engine's prose is reworded, not rewritten.** A dirty seed's refusal is
`engine.DirtyStateError`, whose fixed templates name `migr resolve`, `migr up` and `migr status`.
The engine cannot know it is running seeds, so `internal/cli` wraps a seed run's failure and
replaces those fixed phrases with their `migr seed` forms; `errors.As` still reaches the error, so
the `--json` failure document is built from the same fields. Sending an operator to `migr resolve`
over a dirty seed was the defect: it reports nothing to settle.

### C. What a seed rollback waits for

`migr seed down` and `migr seed resolve --resume` refuse, exit 1, unless every migration is applied —
the `checkMigrated` gate `migr seed` already has — because a seed's down is written against the schema
the migrations leave. **The asserted repairs are not gated**: `--applied` and `--rolled-back` run
nothing and must work exactly when things are broken.

A seed's down passes through the same `dataOnly` source as its up, so a down emitting a schema change
is refused, exit 2. A derived down of a raw or data seed is irreversible, as it is for a migration,
and a seed declared irreversible halts the rollback with its author's reason, exit 1 — both as
`migr down` answers them.

### D. Measured

- An untagged SQLite end-to-end walk: seed, `seed status` showing both applied, `seed down`, both
  pending, and seeding again **succeeding** — each seed inserts under a fixed primary key, so the
  second run is what proves the rows the first wrote are gone, without this suite composing a
  query. The migration history holds no down row throughout, and `migr status`/`migr history`
  still name no seed.
- PostgreSQL's integration suite leaves the server as it found it with `migr seed down` then
  `migr down`, and no longer touches `pgtest.Drop` for seeds.

## Consequences

- `migr down` does not consult the seed history. Rolling a migration back below the schema a seed
  ran against leaves that seed recorded as applied, and its own down may then fail against a schema
  it was not written for. Coupling the two would make a migration rollback depend on seeds, which is
  the mixing ADR 0076 exists to prevent; the gate in C is the half that can be kept.
- The exit-code completeness check now walks **every** command, subcommands included — it read
  only the top level, so `migr seed down` would have been a command nothing held to a code.
- `pgtest.Drop` keeps its one other purpose: a table a migration's escape hatch leaves behind.

## Rejected

- **Flags on `migr seed`** (`--list`, `--down`). Each would change what every other flag means.
- **A `--seeds` flag on `migr status`, `migr history` and `migr resolve`.** Three migration commands
  learning about seeds, where ADR 0076 holds that nothing else sees one.
- **No gate on `migr seed down`.** A seed's down then runs against whatever schema the database
  happens to hold.
- **Rewriting the engine's messages per caller.** The engine cannot know which history it was given,
  and a parameter for it would put seed knowledge into `internal/engine`.
