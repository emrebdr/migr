# 0037. The Go migration contract

## Status

Accepted

## Date

2026-08-20

## Context

`sdk/go` was a `go.mod` and a `doc.go` saying *"Status: scaffolding."* Everything on the engine
side was finished and waiting: [ADR 0030](0030-runtime-protocol-specification.md) specified the
wire, [0031](0031-protocol-codec-and-framing.md) built the codec,
[0032](0032-host-callback-server.md) the six callbacks, [0034](0034-runtime-process-lifecycle.md)
the session, and [0035](0035-generated-go-runner.md) the adapter — which generates a runner naming
`github.com/emrebdr/migr/sdk/go/runner` and `runner.Main()`, **a package that did not exist**.

This is the other end of that import, and the first thing in this repository written to be compiled
into somebody else's program. That changes what is allowed.

`sdk/go` cannot import `github.com/emrebdr/migr/internal/...`. Go's internal rule would permit it —
`sdk/go` sits under `github.com/emrebdr/migr` — but the import would make this module require the
CLI module, dragging pgx, cobra, and a YAML parser into every program that writes a migration. Not
doing that is the reason the SDK is a separate module at all.

## Decision Drivers

- The done-when: *"`sdk/go` is its own module exposing `Up`, `Down`, `Validate`, `Dependencies`,
  and metadata, with a protocol client underneath."*
- [§11](../protocol.md#11-what-an-sdk-owes), which is a checklist written to be ticked off and is
  this task's real acceptance criteria.
- Invariant #10: both directions, always — a `Down` may declare itself irreversible with a reason,
  but it may not be silently absent.
- [ADR 0018](0018-declared-dependencies.md): `Dependencies()` returns a declaration, never a
  computation.
- [ADR 0036](0036-escape-hatch-statements-in-a-preview.md), D: a derived down is refused outright
  for a migration that reached the database itself.
- `forbidigo` forbids `panic` outside `cmd/` and `internal/cli/`, which includes all of `sdk/go`.

## Considered Options

### A. The shape of the contract

- **A1: one required method, and each optional part its own interface** ✔:

  ```go
  type Migration interface{ Up(ctx context.Context, s *Schema) error }

  type Downer interface{ Down(ctx context.Context, s *Schema) error }
  type Irreversible interface{ Irreversible() string }
  type Validator interface{ Validate() error }
  type Dependent interface{ Dependencies() []string }
  ```

  This makes §7.4 structural rather than remembered. A migration implementing neither `Downer` nor
  `Irreversible` **is** `no_down_authored` — there is no field to leave nil and no convention to
  forget — and one implementing both is a registration error, because an authored undo and a
  declaration that none exists cannot both be true.
- A2: a struct of function fields, `migr.Register(migr.Migration{Up: ..., Down: ...})`.
  **Rejected**: "authors no down" becomes a nil check rather than a type fact, and "declares itself
  irreversible" needs a second nilable field whose interaction with the first is a runtime rule
  nothing enforces.
- A3: all four methods required. **Rejected**: every migration would write a `Validate` returning
  nil and a `Dependencies` returning nil, and boilerplate that is usually empty is boilerplate
  nobody reads — including on the one migration where it is not empty.

### B. How a migration is identified

- **B1: `Register(m)` reads its caller's file with `runtime.Caller` and parses the filename with
  the migration grammar** ✔. The version is the one the file already carries, so it cannot disagree
  with what the engine discovered, and there is nothing for an author to mistype.
- B2: `Register(version, m)`. **Rejected**: a version that disagrees with its filename surfaces as
  *the engine discovered a version this runtime did not load* — a message about reconciliation,
  three layers from the typo that caused it.

  The SDK therefore carries its own copy of the filename grammar. That is duplication, and it is
  the cheaper kind: the grammar is already frozen by every migration anybody has written.

### C. Whether `Up` takes a context

- **C1: `Up(ctx context.Context, s *Schema) error`** ✔. §8 says a canceled request's receiver
  *"stops as soon as it can"*, and without a context a migration cannot. The callbacks do I/O, which
  is the case Go's own convention exists for.
- C2: `Up(s *Schema) error`, with the context on the `Schema`. **Rejected**: it is against the
  standard library's own guidance, and it hides the one parameter that decides whether a backfill
  can be interrupted.
- C3: no context anywhere, with cancellation surfacing only as an error from the next callback.
  **Rejected as the whole answer, kept as the backstop**: every `Schema` call does refuse once the
  context is canceled, so a chunked loop stops at its next call even if its author never checks.
  A migration computing between calls still needs the ctx.

### D. Where the session lives

- **D1: in package `migr`, exported as `Run() int`, with `runner.Main()` calling it** ✔. Only that
  package can construct a `*Schema`, and `runner` exists to be the stable symbol ADR 0035 generates
  a call to. It also means a project that would rather write its own `main` can call `migr.Run()`.
- D2: the session in `sdk/go/internal/session`. **Rejected**: it would need to construct a
  `*Schema`, which needs an unexported constructor in `migr`, which Go has no way to share.

### E. Where the print target is redirected

- **E1: in `Run`, before the first message** ✔.
- E2: in an `init` of package `migr`, which would also cover a print from a migration's own `init`.
  **Rejected**: that package is imported by the user's own tests and tools, which would find their
  stdout silently pointed at stderr. Reaching into an importer's process is worse than the hole.

The hole is real and stated: a `fmt.Println` in a migration's `init` runs before `main` and lands
on the protocol stream. It fails loudly — the engine reports a line it could not frame, naming it
([ADR 0031](0031-protocol-codec-and-framing.md)) — rather than corrupting a run silently.

### F. How the duplicated wire types are held honest

- **F1: a spec test in `sdk/go/internal/wire` reading `docs/protocol.md`** ✔, exactly as
  `internal/runtime/protocol/spec_test.go` reads it from the other side. Both halves answer to the
  **document**; neither is held to the other. Every checked JSON block in the specification — 43 of
  them — is decoded and re-encoded **byte for byte** by each codec independently.
- F2: a cross-module drift test, with the root module requiring `sdk/go`. **Rejected**: it would
  make whichever half was written first the definition of correct, and it is the *document* that
  M7-T4 will write the TypeScript client from.
- F3: generate one side from the other. **Rejected**: generation drift is the same problem wearing
  a hat, and the generated side would still need a test to prove it was regenerated.

### G. Where `migr create --lang go`'s template lives

- **G1: `internal/source/golang`, holding `Ext` and `Template` and nothing else** ✔.
- G2: beside the adapter, in `internal/runtime/golang`. **Rejected mechanically**: `internal/cli`
  must import the template, and depguard forbids `internal/cli` from importing anything under
  `internal/runtime/` (invariant #1).
- G3: inline in `internal/cli/create.go`. **Rejected**: [ADR 0025](0025-sql-source-language.md), B
  put the `.sql` template beside its format, and one format's template inline while another's is
  not is a rule with an exception nobody can state.

The template says what the escape hatch costs, because ADR 0036 D now refuses a derived down over
a migration that used it, and the place an author meets `Exec` is the place to say so.

## Decision Outcome

`sdk/go` is a module with three packages: `migr` (the contract, the registry, the `Schema`, and the
session), `migr/internal/wire` (framing, envelope, methods, codes, payloads), and `migr/runner`
(`Main`, and nothing else).

The session mirrors `internal/runtime.Session` from the other side, and the two §2.5 rules apply
identically: **one read loop**, and a pending table for **this side's own** callbacks only.
`migration.execute` and `migration.validate` run on a goroutine, because `$/cancel` has to be
readable while they work — a session that served them inline would leave the notification sitting
behind the very work it was sent to stop.

`internal/runtime/golang`'s test fixture now replaces onto this module rather than onto the stub
ADR 0035 shipped. Its assertions changed with it: the adapter's tests now generate a runner, build
it, start it, and complete a real handshake, which is a materially stronger claim than the stub
could support.

## Consequences

**A migration can only emit `raw` until M6-T8.** That names an engine and trades away database
independence, which is the whole point of the IR. It is the honest shape of a half-built SDK, and
the builder is the next task.

**The wire is written twice, and will drift if the document stops being the contract.** The spec
test is what makes that a build failure rather than a bug report from an SDK user. Anything added
to one codec without a documented vector passes on that side and fails nothing on the other.

**`Register` depends on `runtime.Caller` reporting a usable filename.** Under `-trimpath` the path
is module-relative and `filepath.Base` still gives the right answer; a build that reports no file
at all is refused rather than guessed at.

**A migration cannot be registered from a helper.** `Register` reads the frame directly above it,
so wrapping it in a project's own helper reports that helper's filename. That is a real constraint
and it is not enforced — the failure is a version the engine did not discover.

**`migr validate` still cannot check a Go project**, unchanged from ADR 0035: a Go migration may
call `host.db.query`, which needs an open `driver.Conn`, and [ADR 0028](0028-validation-report.md)
says validate opens no database. The SDK's own `Validate` is served over the protocol and reaches
no database, so the shape is ready; what is missing is a `migr validate` that will start a runtime.
