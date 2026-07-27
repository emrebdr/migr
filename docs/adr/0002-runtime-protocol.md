# 0002. Bidirectional JSON-RPC over stdio for language runtimes

## Status

Accepted

## Date

2026-07-27

## Context

[ADR 0001](0001-architecture.md) established that migrations are written in the developer's own
language and that a database-neutral operation IR is the contract between language SDKs and
database drivers. It did not say how a migration written in TypeScript actually *runs*.

The engine is a Go binary. A migration is a program in another language. Something has to
execute that program and collect the schema operations it describes.

The choice is constrained by what migrations must be able to express. The framework's goals
include data transformations and online migrations of the gh-ost class — backfilling a column
in throttled chunks, branching on how many rows already have a value, adapting to replica lag.
These are v0.1 goals, not stretch goals. A migration that cannot ask the database a question
mid-flight cannot do any of them.

## Decision Drivers

- **Expressiveness.** Migrations must support loops, conditionals, and decisions based on live
  database state, not only static schema declarations.
- **Identical semantics across languages.** The same logical migration must behave the same in
  every SDK; syntax may differ, meaning may not.
- **Dry-run without a parallel implementation.** A preview mode that is a separate code path
  drifts from the real one, and a preview that lies is worse than none.
- **Low cost to add a language.** The value of the project scales with the language matrix.
- **No privileged language.** If Go took a shortcut the others cannot, the abstraction would
  quietly be shaped around Go.

## Considered Options

### Option 1: Bidirectional line-delimited JSON-RPC 2.0 over stdin/stdout

The engine spawns the language runtime as a child process. They exchange JSON-RPC messages over
the child's stdin and stdout, one per line; stderr is passed through as log output.

The engine calls `initialize`, `migration.discover`, `migration.execute`, `migration.validate`,
and `shutdown`. The runtime calls back into the host with `host.schema.apply`, `host.db.query`
(read-only, enforced at the host rather than by convention), `host.db.exec`,
`host.capability.get`, `host.info`, and `host.log`.

**Pros**

- Migrations can query live state and branch on the result, which is what data transformations
  and online migrations require.
- **Dry-run falls out of the same code path.** In plan mode the host records mutating callbacks
  instead of executing them, while read queries still run for real. Preview, diff, and golden
  tests exercise the identical code path as a real apply — there is no second implementation to
  drift.
- Any language with JSON and standard streams can implement it, which is every language on the
  roadmap.
- Process isolation: a crashing migration cannot take the engine down.

**Cons**

- A bidirectional protocol is substantially more to specify than a one-shot output format, and
  the specification becomes a compatibility surface across every SDK.
- Subprocess spawn and IPC overhead per migration run.
- Because migrations may branch on live data, plan mode can take a different branch than the
  later apply.

### Option 2: Declarative IR emission only

Each SDK runs standalone, builds an operation list, prints it as JSON, and exits. The engine
reads that document, plans, and executes.

**Pros**

- Dramatically simpler: a one-way format with no session, no callbacks, no lifecycle.
- Perfectly deterministic and trivially cacheable — the same source always yields the same
  document.
- The easiest possible bar for adding a language: print JSON.
- No possibility of plan and apply diverging.

**Cons**

- A migration cannot see the database. Every decision must be made from static information.
- Data transformations, conditional backfills, and online migrations become impossible to
  express in the migration itself.

**Rejected because:** it forecloses capabilities that are v0.1 commitments rather than future
ambitions. Backfilling a column in chunks while watching replica lag cannot be expressed as a
static operation list. Choosing this would mean either abandoning those goals or bolting a
second, different mechanism onto the side later — at which point the simplicity that motivated
the choice is gone.

### Option 3: WebAssembly plugins

Migrations compile to WebAssembly and run sandboxed inside the engine process.

**Pros**

- Strong isolation with no subprocess overhead.
- Deterministic execution.
- A single distribution artefact per migration.

**Cons**

- The toolchains for the target languages are immature or painful. Python, JavaScript, C#, and
  Java to WASM range from awkward to impractical, and those are most of the roadmap.
- A host-function ABI is still required for database access, so the protocol design work is not
  avoided — only moved and made harder to evolve.
- The entire toolchain cost lands before the first migration can run.

**Rejected because:** the cost arrives before any value, and it is concentrated precisely in
the languages the project exists to support. Rejected on present maturity rather than on
principle — if WASM toolchains for those languages mature, this is worth revisiting as an
additional runtime alongside the subprocess one, not as a replacement.

## Decision Outcome

**Option 1.** Bidirectional line-delimited JSON-RPC 2.0 over the child process's standard
streams, with host callbacks for schema operations and read-only queries.

Two commitments follow:

**Plan mode is a host behaviour, not a separate path.** With `mode: plan`, mutating callbacks
are recorded and return synthetic success; read queries execute live. Everything that previews
migrations — dry-run, diff generation, golden tests — runs the real migration code.

**Go is not privileged.** Go migrations cannot be dynamically loaded into an already-built
binary, so the Go runtime adapter generates a small runner `main` in the user's module, builds
it with `go build`, caches the result by content hash, and executes it as a child process
speaking the same protocol. There is deliberately no in-process fast path for Go. One code path
keeps every language honest, and prevents the protocol from quietly acquiring assumptions that
only hold for the implementation language.

## Consequences

### Positive

- Data transformations, conditional logic, and online migration strategies are expressible in
  the migration itself rather than requiring a separate mechanism.
- Preview and apply cannot drift, because they are the same code.
- Adding a language requires a protocol client and a runtime adapter — no engine changes.
- A misbehaving migration is contained in its own process.

### Negative

- **A migration that branches on live data can plan differently than it applies.** If a
  migration reads a row count and takes one of two paths, the plan reflects the state at plan
  time and the apply reflects the state at apply time. This is inherent to the expressiveness
  the option was chosen for. It is documented in user-facing material rather than hidden;
  pretending otherwise would make plan output untrustworthy in a way users could not detect.
- The protocol is a versioned compatibility surface. A change requires updating the
  specification and the conformance run for every SDK.
- Per-run process spawn cost, and lifecycle handling — timeouts, cancellation, orphan
  prevention — that a one-shot format would not need.

### Neutral

- `host.db.query` is enforced read-only by the host. A migration that needs to write does so
  through recorded operations, which keeps plan mode meaningful.
- Every SDK must pass a conformance suite proving it emits byte-identical IR for the same
  fixture migrations. This is what makes "identical semantics across languages" a testable
  claim rather than an aspiration.

## Notes

Worth revisiting if WebAssembly toolchains for Python, JavaScript, C#, and Java mature to the
point where compiling a migration is routine — most likely as an additional runtime rather than
a replacement, since the protocol would be reused for host functions either way.
