# 0035. Running a Go migration

## Status

Accepted

## Date

2026-08-18

## Context

[ADR 0034](0034-runtime-process-lifecycle.md) built `internal/runtime.Session` — spawn, handshake,
one read loop, the close ladder, the process group — and deliberately shipped no adapter, because
there was nothing to register. So `cmd/migr` built no runtime registry either: dead wiring was
written and deleted rather than shipped.

This is the first adapter, and the question it has to answer is the one CLAUDE.md §4.3 already
states the shape of:

> **Go is not privileged.** Go migrations are executed by generating a small runner `main` in the
> user's module, building it (cached by content hash), and speaking the same protocol over a pipe.
> There is no in-process fast path — one code path keeps every language honest.

Why there is no in-process fast path is worth restating, because it looks like an obvious
optimisation. migr is a single static binary built at release time; a user's migration is Go source
in *their* module, against *their* dependency versions. Nothing links the two. Even if something
could, taking it would give Go a code path no other language has, and the first bug that appeared in
one and not the other would be unfindable.

`sdk/go` is a module with a `doc.go` and nothing else until **M6-T7**. So this adapter names a
package that does not exist yet. That is stated rather than hidden: the generated file is the
contract M6-T7 owes, and the tests here reach it through a stub in `testdata` that a local `replace`
points at.

## Decision Drivers

- The done-when: *"a runner `main` is generated in the user's module, built with `go build`, cached
  by content hash, and executed; a rebuild is skipped when nothing changed."*
- [ADR 0034](0034-runtime-process-lifecycle.md), B: an adapter returns a `Command` description, and
  the pipes, the group, and the kill are the session's.
- [§9](../protocol.md#9-the-process): the working directory is the project root, no secret is
  passed, and a runtime must not depend on argv.
- Invariant #8: nothing that produces a plan may read a clock or a random source, and
  `Runtime.Command` runs inside `migr plan`.
- Invariant #1: only `cmd/migr` may name `internal/runtime/golang`, which depguard enforces.

## Considered Options

### A. How a Go migration is executed at all

- **A1: generate a runner `main` in the user's module, build it, cache it, exec it** ✔.
- A2: the `plugin` package. **Rejected**: linux and darwin only, no Windows at all, and it requires
  the host binary and the plugin to have been built by an identical toolchain with identical
  versions of every common dependency — a constraint no real project holds across a released `migr`
  and a user's own `go.mod`.
- A3: `go run ./migrations` per invocation. **Rejected** for two independent reasons. It rebuilds
  every time, so there is no cache to skip. And the process the engine talks to is `go run` itself,
  with the migration as its *child* — so the exit status is proxied, `go run` prints its own
  `exit status 1` onto the stderr the engine is tailing, and the process the group kill reaches is
  the wrapper. Every guarantee ADR 0034 established would become indirect.
- A4: an embedded Go interpreter. **Rejected**: it is not the toolchain the user's code is compiled
  and tested with, so a migration could pass in migr and fail in their build, or the reverse.

### B. What the generated file names

- **B1: a blank import of the migrations package, plus one SDK entrypoint** ✔:

  ```go
  import (
      _ "example.com/app/migrations"

      "github.com/emrebdr/migr/sdk/go/runner"
  )

  func main() { runner.Main() }
  ```

  Each migration registers itself from an `init`, so the generated file never names an identifier
  the user chose and adding a migration is a one-file change.
- B2: import the package and call an exported `All()` the user writes. **Rejected**: it makes the
  generated file depend on code migr does not own, makes every new migration a two-file change, and
  fails at compile time in generated code when somebody forgets the second file.

  This is registration through `init`, which [ADR 0011](0011-driver-contract.md) rejected for
  *drivers*. The reason it rejected it does not apply here: a driver registry is built in `main`
  before a flag is parsed, so an `init` has nowhere to report a duplicate. A migration registry is
  read when the engine asks `migration.discover`, which is a request with an error response — so a
  duplicate version is reportable, and reporting it is M6-T7's.

### C. Where the runner and the binary live

- **C1: `<module root>/.migr/go`, with a `.gitignore` of `*` written beside it** ✔. The generated
  file must be inside the module for `go build` to resolve the migrations import at all. A
  dot-directory is invisible to `./...`, so it cannot break the user's own build, vet, or test. The
  ignore file is written only when absent, so migr never edits one somebody owns.
- C2: the source in the module, the binary under `os.UserCacheDir()`. **Rejected**: two locations
  to reason about and two to clean, for a binary that is a function of this project's own source.
- C3: `go build -overlay`, writing nothing into the user's tree. **Rejected**: it works, but a
  package that exists only in an overlay makes every compile error name a path that is not on disk —
  and the errors this reports are precisely the ones a user needs to open a file to fix.

### D. One build directory, or one per stamp

- **D1: one directory, with the stamp in a file beside the binary** ✔. `main.go` therefore has a
  stable path, which is what a `BuildError` names.
- D2: `.migr/go/<stamp>/`, keyed by content. **Rejected**: it grows without bound — a binary per
  migration edit, tens of megabytes a week in normal development — and pruning needs either a
  time-based sweep, which invariant #8 forbids on a planning path, or deleting siblings, which races
  a concurrent run that has already passed its cache check and is about to `exec` one.

  The concurrency D2 was meant to protect is handled instead by building to a reserved name and
  renaming onto `runner`: a process already executing the old inode keeps it.

### E. What the content hash covers

- **E1: the generated runner, every file in the migrations directory, `go.mod`, `go.sum`, the
  toolchain's identity (`GOVERSION`, `GOOS`, `GOARCH`), and migr's own version** ✔, length-framed so
  two different inputs cannot concatenate into one digest, and read in name order per invariant #8.
- E2: additionally re-hash the module-local files `go list -deps` reported at the last build.
  **Rejected for now**: exact in practice, but it costs a `go list` on every build and dozens of
  file reads on every hit, to close a hole whose remedy is deleting one directory.
- E3: no stamp — always `go build`, and let Go's own build cache make it cheap. **Rejected**: it is
  correct by construction, but the done-when asks migr to skip the rebuild, and a warm no-op build
  measured ~190 ms on this repository — paid by `migr status` as much as by `migr up`.

**E1 has a hole and it is stated rather than omitted:** a package the migrations import from
elsewhere in the module is not in the digest, so editing it does not force a rebuild. It is written
in `internal/runtime/golang/doc.go`, it is written here, and it is asserted by a test — so widening
the stamp fails that test rather than quietly making both documents wrong.

### F. What consumes the registry today

- **F1: `cmd/migr` builds it, `cli.Execute` takes it, and `sourceFor` reads it** ✔ — so the refusal
  an operator sees distinguishes *this build has no adapter for that language* from *this build has
  a Go adapter and the SDK it runs arrives in M6-T7*.
- F2: build it in `cmd/migr` and pass it nowhere. **Rejected**: that is the dead wiring M6-T5
  refused, and a registry with no reader is a value nothing can be wrong about.

This is thin, and worth saying plainly: the registry's only reader is a message. It lands here
because `cmd/migr` is the sole package that may name `internal/runtime/golang`, so the registration
has nowhere else to go — and because M6-T10, which wires a `Session` into `sourceFor`, is blocked on
M6-T7 rather than on this task.

## Decision Outcome

`internal/runtime/golang` implements `Name`, `Discover`, and `Command`. `Discover` is
`exec.LookPath("go")` and nothing more, which is what lets the interface keep its promise to open no
process. `Command` runs `go env` for the toolchain's identity and `go list -m` for the module, then
either hands back the cached binary or writes `main.go`, builds it, and records the stamp.

The stamp is dropped *before* a build begins and written *after* it succeeds, so an interrupted
build is never read as a hit and a stamp is never newer than the binary it describes.

`cmd/migr` gains `runtimes.go`, mirroring `drivers.go`, and `cli.Execute` takes both registries.

## Consequences

**A user's repository gains a `.migr/` directory.** It ignores itself from the inside, so nothing of
migr's reaches a commit, but it is there and `migr` never removes it.

**A stale binary is possible and the remedy is manual.** Editing a helper package the migrations
import, without touching the migrations themselves, leaves the previously built runner in place.
Deleting `.migr/go` is the way out. E2 is the upgrade path if this turns out to bite.

**This adapter cannot be used end to end yet.** `github.com/emrebdr/migr/sdk/go/runner` does not
exist, so a real project would fail inside `go build` with a missing package. Everything except that
one import is exercised by the tests here, against a stub the fixture module reaches by `replace`.

**One symbol is guessed on M6-T7's behalf.** If the SDK exports something other than
`runner.Main()`, this adapter's template changes with it — one constant and one test.

**The build directory is not portable between machines.** `GOOS`, `GOARCH`, and `GOVERSION` are in
the stamp, so a `.migr/` copied or mounted across machines rebuilds rather than running the wrong
binary. That is the intended outcome, not a limitation.

**`migr validate` still cannot check a Go project**, and this task does not change that. A Go
migration may call `host.db.query`, which needs an open `driver.Conn`, and ADR 0028 says validate
opens no database at all. That tension is real and belongs to whichever task first makes
`migrations.language: go` validate.
