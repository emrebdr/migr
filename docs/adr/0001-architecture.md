# 0001. Go core with an operation IR in the middle

## Status

Accepted

## Date

2026-07-27

## Context

`migr` is a database migration framework with two independence claims. Migrations are written
in the developer's own language — Go, TypeScript, and later Python, Rust, and others — and they
run against any supported database engine: PostgreSQL, MySQL, SQLite, and later MongoDB,
DynamoDB, ClickHouse. A migration says *what* changes (`CreateTable("users")`), never *how* a
particular engine performs it.

Two structural questions had to be answered before any code existed, because everything else
depends on them:

1. What language implements the engine and CLI?
2. What sits between N language SDKs and M database drivers, so that adding a language does not
   touch any driver and adding a driver does not touch any SDK?

Answering these late would be expensive. The second question in particular determines whether
the project has an architecture or an accumulating pile of special cases.

## Decision Drivers

- **Distribution.** The CLI must ship as a binary a user can download and run, on Linux, macOS,
  and Windows, without installing a runtime.
- **Database driver breadth and quality.** The project targets relational engines now and
  document and wide-column stores later. Driver maturity varies enormously by ecosystem,
  especially outside PostgreSQL and MySQL.
- **Subprocess orchestration.** Executing migrations written in other languages means spawning
  and speaking to child processes; this is a core operation, not an edge case.
- **N×M containment.** Adding a database must not require changing the core or any SDK, and
  vice versa. Without this the project does not scale past its first few targets.
- **Contributor accessibility.** The intended contributors are backend and platform engineers.

## Considered Options

### Option 1: Go core, with an operation IR as the neutral middle

The engine and CLI are written in Go. Language SDKs emit a versioned, database-neutral
**operation IR**; database drivers consume that IR and compile it into engine-specific
statements. Nothing else crosses that boundary.

```
   Go SDK      TypeScript SDK      (Python, Rust, … later)
      └──────────────┴──────────── emit IR ───┘
                     │
              ┌──────▼───────┐
              │ Operation IR │   ← the only contract
              └──────┬───────┘
        ┌────────────┼────────────┐
   ┌────▼───┐   ┌────▼───┐   ┌────▼───┐
   │Postgres│   │ MySQL  │   │ SQLite │   ← compile IR → statements
   └────────┘   └────────┘   └────────┘
```

**Pros**

- Single static binary; cross-compilation is a first-class, well-trodden path.
- Strongest breadth of maintained database drivers across both SQL and NoSQL engines.
- `os/exec` and the standard library make subprocess orchestration straightforward.
- The IR turns an N×M problem into N+M: each SDK targets one format, each driver consumes one
  format.
- Widely readable by the intended contributor base.

**Cons**

- Less expressive type system than Rust for modelling the operation set.
- Garbage collection, though irrelevant at the scale of a migration tool.

### Option 2: Rust core

**Pros**

- Strong type system, well suited to an operation enum and exhaustive matching.
- No garbage collector; excellent performance.
- `sqlx` and `tokio-postgres` are solid for relational engines.

**Cons**

- Weaker SDK coverage for the non-relational engines on the roadmap, notably DynamoDB and
  MongoDB, where the official clients target Go and JavaScript first.
- Heavier cross-compilation and CI story for a tool distributed as prebuilt binaries.
- Slower to build, on a project whose difficulty is breadth of engine support rather than
  execution speed.

**Rejected because:** the hard part of this project is covering many database engines
correctly, and that is exactly where Rust's ecosystem is thinnest. Performance, where Rust would
win, is not a constraint — a migration's cost is dominated by the database, not the tool.

### Option 3: TypeScript/Node core

**Pros**

- Fastest path to authoring SDKs, and the best developer experience for the JavaScript ecosystem
  that is a large part of the target audience.
- One language for the core and for what is likely the most-used SDK.

**Cons**

- The CLI requires an installed Node runtime, or bundling via `pkg`/`bun`, which weakens the
  "download a binary" distribution story.
- Driver quality is uneven for enterprise engines — Oracle, SQL Server, Cassandra.

**Rejected because:** the distribution and enterprise-driver weaknesses land squarely on the
project's stated audience of platform teams and enterprises running heterogeneous databases.

## Decision Outcome

**Option 1.** Go for the engine and CLI, with a versioned operation IR as the sole contract
between language SDKs and database drivers.

Structural rules that follow from the IR being the only contract:

- Core packages (IR, planner, engine, validation, capability model) never import a concrete
  driver or runtime. Wiring happens through a registry, with blank imports confined to the CLI
  entrypoint.
- Drivers never import one another; shared logic moves up into the driver contract package.
- No raw SQL exists outside a driver package — the migration history table itself is created
  through driver-compiled IR operations.

These are enforced mechanically by a `depguard` rule in `.golangci.yml` rather than by
convention, so a violating import fails the build.

## Consequences

### Positive

- Adding a database requires a new package implementing the driver contract and zero changes
  elsewhere. Adding a language requires a new SDK and a runtime adapter, and no driver changes.
- A single conformance suite can assert that every driver handles every operation, and a second
  can assert that every SDK emits **byte-identical IR** for the same logical migration. That
  second suite is the mechanical proof of the language-independence claim; without it the claim
  is unfalsifiable.

### Negative

- **The repository has two Go modules**, root and `sdk/go`. The SDK is separate so that
  `go get github.com/emrebdr/migr/sdk/go` does not drag the CLI's dependency tree — the
  PostgreSQL, MySQL, and SQLite drivers, the CLI framework, the YAML parser — into a user's
  application. The cost is real: a root `go build ./...` **silently skips** nested modules, so
  every build, test, lint, and CI invocation must iterate both explicitly. A tool that forgets
  reports success while the SDK is broken.
- The IR is a translation layer, so an engine-specific feature is only reachable through an
  escape hatch or by extending the IR, which is a five-part change touching every driver.
- Go's type system cannot express the operation set as precisely as Rust's, so some
  exhaustiveness is enforced by linting rather than by the compiler.

### Neutral

- **v0.1 targets PostgreSQL, SQLite, and MySQL, with Go and TypeScript SDKs.** The combination
  is chosen to stress the abstraction rather than to maximise coverage: PostgreSQL supports
  transactional DDL and most operations natively; SQLite cannot drop or alter columns directly
  and so forces the capability model's emulation path; MySQL has no transactional DDL and so
  forces the engine's partial-failure and resume model. Alternatives considered and rejected:
  PostgreSQL alone, which ships fastest but leaves the abstraction entirely unvalidated, and
  PostgreSQL with MongoDB, which would prove the boldest claim first but front-loads the most
  risk before the engine is stable.

## Notes

Revisit if the driver ecosystem shifts materially — for example if the primary clients for the
non-relational engines on the roadmap move away from Go, which was the deciding factor against
Rust.
