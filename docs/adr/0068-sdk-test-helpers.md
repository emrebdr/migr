# 0068. SDK test helpers: capture the IR, read the undo answer, and let the engine judge the rest

## Status

Accepted

## Date

2026-10-04

## Context

Both SDKs could already be driven in a test — that is how they are tested here. `sdk/go`'s
`fixtures_test.go` stands up a fake engine over two pipes, and `sdk/typescript`'s `schema.ts` says in
its own header that *"the window takes a `Host` rather than a stream, so the whole builder and every
callback is exercised against a recording host and no process."*

**None of that was reachable by a user.** TypeScript exported `newSchema` and `Host` and nothing that
used them; Go kept `newSchema`, `host` and `seal` unexported, so someone writing a test against their
own migration had no seam at all and no way to see the IR it emits. M10-T6 turns the machinery both
SDKs already have into API, and adds the one thing neither had: a way to run a migration against a
database that is thrown away afterwards.

The task's three clauses are three different questions, and the design follows from noticing that
they have three different authorities:

| clause | who can answer it | needs |
|---|---|---|
| assert generated operations | the SDK's own window | nothing |
| assert rollback correctness | *split* — see Option A | — |
| run against a throwaway database | the **engine** | the binary, a database |

### What was measured first

Four probes, before anything was written.

**Node's self-reference cannot reach the library from a subpath bundle.** The plan for
`@migr/sdk/testing` was a second esbuild entry whose one import is the package by name, left external
so no second copy of the library ships. `tsc --noEmit` resolves that happily with a `paths` entry —
and at runtime Node throws `ERR_MODULE_NOT_FOUND`, because the build writes a `package.json` into
each format directory to declare its `type`, and **that manifest shadows the real one**, so the
package self-reference resolves to a package with no `exports` and no name. A relative `../index.ts`
marked external does work in both formats, needs no `tsconfig` change, and finishing the `.ts` → `.js`
rewrite for it is the rewrite `build.mjs` already performs on declarations. Measured: 13.6 KB of
helpers beside a 71.6 KB library, importing it rather than copying it.

**A config in a temporary directory does not work at all.** `docs/protocol.md` §9 makes the directory
holding `migr.yaml` the runtime's working directory, and `runtime.Project.Root` is that directory —
which both adapters probe *from*: `internal/runtime/golang` runs `go list -m` there, and
`internal/runtime/node` asks Node to resolve `@migr/sdk` from there. Measured, with the config outside
the module:

```
migr: starting the go runtime: preparing the go runtime: this project is not inside a Go module:
go list -m reported "command-line-arguments"
```

So the throwaway config has to live **inside the project**, under `<project>/.migr/` — the directory
the Go adapter already writes its generated runner into, which carries a `.gitignore` holding `*` and
so ignores itself from the inside (ADR 0035). Measured again from there: `up`, `down`, `up`, `down`
all green, and the warm path 32 ms.

**The documents `--json` prints are what to parse.** `migr up --json` emits `runs` (not `ran`),
`skipped`, and `batch`; `validate --json` emits `problems`, `warnings`, `unchecked`, `connected` and
`migrations`; and a failing command writes **one parseable line on stderr** — `{"code":1,"error":…}`
— so a helper gets the exit code *and* a reason without scraping a table.

**`host.db.exec` under `mode: plan` never reaches the connection.** Read at
`internal/runtime/host/handler.go`: the plan branch answers `{rowsAffected:"0", executed:false}` and
holds the statement before `opts.Conn` is touched, while `query` has no mode branch at all. That is
what the recording hosts copy.

## Decision Drivers

- A helper must not become a second implementation of anything the engine owns. The framework's
  central claim is that one neutral middle is translated in one place.
- A helper that silently skips itself is worse than no helper: it reports a pass it did not earn
  (ADR 0028 B2, ADR 0040).
- A migration may branch on what it reads (ADR 0009), so a capture has to be able to stage that —
  and must not answer a question the test never staged.
- Test-only API should not be what a migration imports, nor what `migr-runtime-node` loads.

## Considered Options

### Option A: what *"assert rollback correctness"* actually asserts

**A1. Capture both halves, read the undo answer, and let the engine judge the rest — chosen.**
In-process the helper reports which of `docs/protocol.md` §7.4's three answers a migration gives, and
captures every half it has. Whether every operation *can* be inverted is answered by `migr validate`'s
`reverse` check through the project helper; whether a rollback actually leaves the database where it
found it is `RoundTrip`. One refusal is kept in-process because only the capture can see it: an up
that sends statements of its own and authors no down, which the engine refuses to derive a down over
(ADR 0036).

**A2. Port `ir.Reverse` into both SDKs — rejected on merit.** It is the strongest offline check:
`down == reverse(up)` with no database. It is also a second and a third implementation of inversion,
and [ADR 0022](0022-rollback-executor.md) put derivation in `internal/engine` for exactly this reason:
*"three SDKs must not reach three different inverses for one migration."* Twenty cases per language,
held to `docs/ir.md` §8 — and the day a copy drifted, the helper would pass a down the engine refuses.
A test helper that disagrees with the engine is worse than one that declines to answer.

**A3. A per-kind reversibility bit — rejected on merit, and it is A2 one bit at a time.** It looks
cheaper: a table of which kinds need a `from` to be invertible. But §8.4 makes that per *payload*, not
per kind (`create_table` inverts to a `drop_table` that is itself irreversible), so the table would
encode the same knowledge in a weaker form, with the same drift and none of A2's strength. The engine
already reports it, offline, through a command built for CI.

### Option B: where the helpers live

**B1. Its own surface per language — chosen.** `sdk/go/migrtest`, as `drivertest` and `pgtest` are,
and `@migr/sdk/testing`. The Go package takes `*testing.T`, which means it imports `testing`; keeping
that out of the package a user's *application* imports is the point. The TypeScript subpath keeps the
helpers out of `index.js`, which is the file `migr-runtime-node` loads on every migration run.

**B2. The SDK's own surface — rejected.** No packaging work, and the library a migration imports would
carry `os/exec` and `node:child_process` and a `*testing.T`-shaped API.

Go needed a seam either way, and it is the four names TypeScript already had: `Host`, `Window`,
`NewWindow`, and `Window.Seal`. Two details are worth recording. **`Call` was not available as a
name** — `column.go` already exports `Call(f Function) *Default` for a default expression — so Go
takes the three facts as parameters where TypeScript takes a `Call`; a public API does not get to
rename one of its own exports to make a second one read nicely. And `Seal` lives on a wrapper that
*holds* the `Schema` rather than embedding it, so a migration handed a `*Schema` still cannot close
the window it is acting through. A test asserts that `*Schema` has no `Seal` method.

`session.go` now opens its window through `NewWindow` too, so *"the helper stands where the session
stands"* is structural rather than claimed.

### Option C: how a captured operation comes back

**C1. The canonical document, compared as bytes — chosen.** The capture holds each operation as the
document the engine would have received, and `Is`/`is` renders what the test expected through the
SDK's *own* encoder before comparing. That is the runtime conformance suite's method, and it has a
second benefit: an expected operation that is not a valid one fails with the SDK's own error.

**C2. Typed operations — rejected on cost.** Natural in TypeScript, which already exports `decode`.
The Go SDK has no decoder at all — only `ParseType` — so this would mean writing a third IR *decoder*
and keeping it in step with `docs/ir.md`. `Kinds`/`kinds` covers the looser assertion for free.

### Option D: the binary, when there is none

**D1. Resolve, then fail — chosen.** `MIGR_BIN`, then `migr` on PATH, then a refusal naming both.
`Found`/`found` answers the same question without starting anything, for a suite that would rather
skip deliberately. **A `MIGR_BIN` that names something unrunnable is an error and not a fall-through
to PATH**: whoever set it meant it, and silently running a different binary is how a suite starts
measuring something nobody chose.

**D2. Skip when absent — rejected on merit**, for the reason `migr validate --connect` fails on an
unreachable database (ADR 0064): a gate that drops a check reports a pass it did not earn.

`MIGR_BIN` is the first environment variable migr itself names. `internal/config` reads the
environment exactly once, as an injected `os.LookupEnv` parameter, and both ladders here follow it —
which is also what lets a test stage the ladder without touching the process environment.

### Option E: which database is the throwaway

A SQLite file in a temporary directory, with `Driver` and `DSN` together pointing at a real
PostgreSQL or MySQL where a user wants one. That engine needs no daemon (ADR 0050), so the default
costs a `TempDir` and a few milliseconds — and the lock file the driver puts beside the database goes
with it.

The history table is derived **per project, not per test**, and that was measured rather than
reasoned about: the first version derived it from the test's name alone, and a case staging two
projects over one database had the second read the first one's runs as its own. M7-T8 recorded the
general form of this — *the database is the thing that remembers*.

## Decision Outcome

| helper | Go | TypeScript |
|---|---|---|
| capture a half | `Up` · `Down` · `Stage{…}.Up` | `capture` · `captureDown` |
| assert the IR | `Capture.Is` · `Capture.Kinds` · `Documents` | `.is` · `.kinds` · `documents` |
| the undo answer | `Reversal` → `Reversed{Undo, Reason, Up, Down}` | `reversal` → `Reversed` |
| the throwaway | `New(t, Project{…})` → `Running` | `project({…})` → `Project` |
| the binary | `Found` · `ErrNoBinary` · `MIGR_BIN` | `found` · `NoBinaryError` · `MIGR_BIN` |

Four rules the recording hosts keep, each a copy of the engine's host rather than a convenience:

- an unstaged **read**, **capability**, or **`host.info`** is refused, naming what to stage. Answering
  an empty value in its place sends a branching migration down a branch the test never meant to ask
  about, which is the failure a capture exists to catch;
- the refusal is remembered, so a migration that **swallows** it still fails the capture — the test is
  that the migration *reached* for an answer, not what it made of the refusal (ADR 0040's rule, from
  the other side);
- **`exec` is recorded and never performed**, and answered the way the engine answers it: held under
  plan, reported performed under apply with whatever the stage says it changed;
- the window is **sealed**, so a chain built and never applied fails rather than quietly emitting less.

Where the two SDKs diverge, the language decides, as ADR 0042 established. Go reports the ADR 0036
refusal through `t.Errorf` *and* carries it on `Reversed.Problem`, because Go has non-fatal reporting;
TypeScript throws, because it does not. Go cleans up through `t.Cleanup`; TypeScript returns a
`remove()` a test calls in an `after` hook or a `finally` (ADR 0045).

## Consequences

### Positive

- A user can assert the IR their migration emits, in both modes, with a staged read — against the
  real window, the real builder, and the real encoder.
- `migr validate` and `RoundTrip` reach a user's test suite, so the two things only the engine knows
  are available without anyone shelling out by hand.
- The Go seam is four names TypeScript already had, and `session.go` uses it, so the two SDKs' windows
  are now opened the same way.

### Negative

- The project helper needs the `migr` binary. There is no way around it — an SDK contains no driver —
  and a suite that has none fails rather than skipping.
- `RoundTrip` is a residue check (up, down, up, down), not a comparison of two schema snapshots. That
  needs `Conn.Introspect`, which is **M11-T2**, and an SDK composing catalog SQL is what invariant #3
  forbids. `cmd/migr`'s own suites measure the same property the same way.
- A Go project helper leaves `<module>/.migr/go` behind, since the adapter builds its runner there.
  It ignores itself from the inside, and what the helper writes is removed with the test.
- **A test file cannot live in the migrations directory**, which is the least convenient consequence
  and was found by hand-driving rather than by reasoning: §9's discovery is strict with dotfiles as
  its only exemption, so a `migrations_test.go` beside the migrations is refused by name and every
  command fails before a migration is loaded. The first two helpers do not care — they take a
  migration as a value — and the third points at the directory from outside it, which also means a
  test imports the migrations package rather than being inside it. Both SDKs' docs say so.
- The same probe found that **`migr validate`'s exit 2 is two different things**: the checks found
  problems, whose report is on stdout, and the command was given something it refuses, whose message
  is on stderr. The helper tolerated 2 and then choked on an empty document. A blank answer is now
  never a report, in both SDKs, and a refused filename arrives as a failure naming the file.
- The repository's own suites build `migr` to test the third helper (`make test` runs before
  `make build` in CI), which costs a few seconds per package run.

### Neutral

- The TypeScript helpers are synchronous where they drive a process (`spawnSync`) and asynchronous
  where the SDK's own API is. Nothing is left running for a test to clean up after.
- `@migr/sdk/testing` is a second entry point rather than a second bundle, so the package grows by
  the helpers and not by a copy of itself.

## Notes

M10-T6 completes M10 bar M10-T3, which waits on M11-T2. The one thing a third SDK owes beyond these
three helpers is **nothing further**: it owes no inverse, and that is the point of A2.
