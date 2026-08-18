# migr

**Polyglot, database-agnostic schema migrations.**

[![CI](https://github.com/emrebdr/migr/actions/workflows/ci.yml/badge.svg)](https://github.com/emrebdr/migr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Describe a schema change once, in the language you already write, and run it against any
supported engine. A migration says *what* changes — `CreateTable("users")` — never *how* a
particular database performs it. `migr up` behaves the same whether the target is PostgreSQL or
MongoDB, and whether the migration was written in Go or TypeScript.

That is two-dimensional independence: **database independence**, because a migration never
names an engine or its SQL, and **language independence**, because every SDK implements the
same contract. A third claim sits alongside them — migrations are **safe to run against a
live, loaded production database**. Zero-downtime is a property the framework verifies, not
operator discipline.

---

## Status

> **Pre-alpha.** There is no migration engine yet. The repository builds, lints, and tests
> clean, and the foundations are in place — but nothing here will migrate a database today.
> Do not depend on it.

What the binary actually does right now:

```console
$ migr version
migr dev go1.26.3 darwin/arm64

$ migr version --json
{
  "version": "dev",
  "go": "go1.26.3",
  "platform": "darwin/arm64"
}
```

```console
$ DATABASE_URL='postgres://app:s3cr3t@db.internal:5432/app' migr config
version: 1
migrations:
  dir: ./migrations
  language: typescript
databases:
  default:
    driver: postgres
    dsn: "postgres://app:***@db.internal:5432/app"
    schema: public
history:
  table: migr_migrations
lock:
  strategy: advisory
  timeout: 30s
safety:
  lock_timeout: 3s
  lock_retries: 5
  statement_timeout: 30s
  max_lock_impact: brief_exclusive
  auto_rewrite: true
  replica_lag_threshold: 5s
validation:
  allow_destructive: false
```

That is the complete list. `version` and `config` are the only subcommands registered in
[root.go](internal/cli/root.go); everything else in the planned surface below is unimplemented.
Credentials in a DSN are redacted on every output path, so leaking one takes a deliberate call.

### Now, next, later

**Done — M0, M1, M2.** The foundations: a two-module layout, a Makefile that cannot skip a
module, a 24-linter gate, a mechanically enforced import boundary, a custom AST check for the
comment rules, and CI on Linux and macOS. Then the neutral middle: a logical type system, the
twenty-operation IR with a versioned envelope and a checksum scheme. Then the contracts every
database plugs into: the driver interface and registry, the two-axis capability model, a
golden-file harness, a conformance suite that proves a driver's compiler agrees with what it
declared, and the migration history store — thirteen architecture decision records so far.

**Now — M3, the PostgreSQL driver**, and it is the first code that touches a database. The
engine core follows in M4. **M5 is the first version a person can actually use** — against
PostgreSQL, with SQL-file migrations. The Go SDK and the runtime protocol land in M6, and
TypeScript in M7, which is where the polyglot claim stops being a claim.

**Later.** The SQLite and MySQL drivers (M8, M9), validation and safety (M10), introspection
and schema diffing (M11), seeds and data transformations (M12), and the zero-downtime milestone
(M13) — where "safe against a live production database" becomes something a test suite proves
rather than something the README asserts.

---

## The idea

### Database independence

A migration never names a native type or writes engine SQL. It emits a versioned, neutral
**operation IR**; each driver compiles that IR into its own statements. Raw SQL outside a
driver package is an invariant violation rather than a style preference — the migration history
table itself is created through compiled operations.

Where an engine cannot do something directly, the driver says so honestly: `Native`,
`Emulated` with a named strategy, or `Unsupported` with a reason. A capability is never
silently degraded, and an emulation that rebuilds a table shows up in `migr plan` before it
happens.

### Language independence

Every SDK implements the same contract — `Up`, `Down`, `Validate`, `Dependencies` — and must
emit **byte-identical IR** for the same logical migration. A shared conformance suite runs the
same fixture migrations through every SDK and compares the output byte for byte. If Go and
TypeScript disagree, the central claim of the project is false, and the suite fails.

### Safe against a live database

Support answers *can this operation run*. A second, independent axis answers *what does it cost
a live database*: `LockFree`, `BriefExclusive`, `LongExclusive`, or `TableRewrite`.

That verdict is **resolved at runtime from the connected server's version**, never hard-coded.
PostgreSQL 11 made `ADD COLUMN ... DEFAULT` instant where 10 rewrote the table; MySQL 8's
choice between `INSTANT`, `INPLACE`, and `COPY` depends on the column type and the specific
alteration. A driver that reports the wrong lock impact is worse than one that reports
`Unsupported` — it turns a "safe" migration into an outage.

No DDL statement ever waits unbounded for a lock. On PostgreSQL the lock queue is FIFO, so an
`ALTER TABLE` stuck behind a long-running query blocks *every subsequent query on that table*
behind it. Three milliseconds of work becomes a table outage. Every statement therefore runs
under a lock timeout with bounded retry and backoff.

For the changes no lock timeout can make cheap, migr copies the table into a shadow, keeps the
shadow current by **reading the engine's replication log** — the binlog on MySQL, logical
decoding on PostgreSQL — and swaps the two by rename. Reading the log rather than installing
triggers is the point: production writes do no extra work for the hours a large copy takes.

The rename is the only irreversible step, so it **waits for you**. When the copy finishes the
shadow is held in sync indefinitely, its lag measured and shown, until you confirm — and the
original table is kept under a derived name afterwards, so a rollback is a second rename rather
than a restore.

---

## The idea, in code

> **Not implemented yet.** The Go SDK lands in M6 and the TypeScript SDK in M7. This is shown
> so the shape of the contract is legible, not because you can run it.

```go
package migrations

import migr "github.com/emrebdr/migr/sdk/go"

func (AddUsers) Up(s *migr.Schema) {
    s.CreateTable("users", func(t *migr.Table) {
        t.UUID("id").PrimaryKey()
        t.String("email", 255).Unique()
        t.Timestamptz("created_at")
    })
}

func (AddUsers) Down(s *migr.Schema) {
    s.DropTable("users")
}
```

```typescript
import { Schema, Table } from "@migr/sdk";

export function up(s: Schema) {
  s.createTable("users", (t: Table) => {
    t.uuid("id").primaryKey();
    t.string("email", 255).unique();
    t.timestamptz("created_at");
  });
}

export function down(s: Schema) {
  s.dropTable("users");
}
```

Different syntax, identical meaning. Both emit the same IR, byte for byte, and the conformance
suite is what holds that true as both SDKs evolve.

### Planned CLI surface

| Command | | Status |
|---|---|---|
| `migr version` | version, commit, and platform | **works today** |
| `migr config` | resolved config, defaults applied, secrets redacted | **works today** |
| `migr init` | scaffold `migr.yaml` and a migrations directory | **works today** |
| `migr create` | new migration from a template | **works today** |
| `migr up` / `migr down` | apply / roll back, by step or to a version | **works today** |
| `migr status` | applied, pending, missing, drifted, dirty | **works today** |
| `migr history` | the execution log, rollbacks and failures included | **works today** |
| `migr plan` | compiled statements per migration, either direction | **works today** |
| `migr plan --safety` | the same, annotated with lock impact | planned |
| `migr validate` | read, compile, and reverse every migration, connecting to nothing | **works today** |
| `migr diff` | introspect, diff against the desired schema, emit a migration | planned |
| `migr seed` | data seeds, kept separate from schema migrations | planned |

Global flags `--config`, `--database`, `--json`, and `-v/--verbose` are registered now.
`--json` switches **both** streams — JSON results on stdout, JSON logs on stderr — so
`migr status --json | jq` never finds a log line inside the document it is parsing. A failure
goes to stderr too, as one object on one line, so a run that committed some migrations and then
failed leaves exactly one document on each stream.

Exit codes are stable and meant for scripting: `0` success, `1` runtime error, `2` validation
failure, `3` lock contention, `4` checksum drift, `5` lock-impact threshold exceeded. **A report
exits 0 having found a problem** — `migr status` over a drifted database exits 0 while `migr up`
against that same one exits 4, and `migr validate` exits 2. They answer different questions:
`status` reports state, `validate` judges the repository, and `up` is the gate.

---

## Building from source

Requires **Go 1.26**. Nothing else, yet — Node 24 becomes a prerequisite when the TypeScript
SDK arrives in M7, and Docker when the integration suites arrive in M3.

```console
$ git clone https://github.com/emrebdr/migr.git && cd migr
$ make build          # -> bin/migr
$ ./bin/migr version

$ make test           # unit + golden tests, no Docker required
$ make check          # lint + test -- the gate a change must pass
$ make help           # every target
```

`make tools` installs the pinned `golangci-lint` into `./bin`. If you already have one on
`PATH`, `make lint` uses it and warns that the version is unpinned.

> This repository contains **two Go modules**: the root module and a nested `sdk/go`, kept
> separate so `go get .../sdk/go` does not drag the database drivers, the CLI framework, and
> the YAML parser into an application that only wants to write migrations. A bare
> `go build ./...` **silently skips** the nested module, which is why every Makefile target
> loops over both. CI calls these same targets rather than reimplementing them in YAML, so the
> local gate and the remote gate are the same gate.

---

## Repository layout

```
cmd/migr/              CLI entrypoint -- the ONLY place drivers and runtimes are wired in
internal/
  ir/                  versioned operation IR: op types, JSON codec, validation
  schema/              logical type system and schema snapshots
  capability/          per-operation feature matrix and lock-impact classification
  plan/ engine/        IR -> ordered plan; apply, rollback, transactions, locking
  history/ checksum/   migration history store and drift detection
  validate/ diff/      destructive-op and capability validation; snapshot diffing
  migration/           the migration filename grammar, discovery, and dependency order
  driver/              Driver contract + registry; drivertest/ postgres/ mysql/ sqlite/
  runtime/             Runtime contract + registry; protocol/ golang/ node/
  source/sql/          the SQL migration file format: extension, markers, template
  cli/ config/ version/
sdk/
  go/                  separate module: github.com/emrebdr/migr/sdk/go
  typescript/          npm package @migr/sdk
test/
  golden/              per-driver, per-operation expected SQL
  conformance/         shared driver, runtime, and zero-downtime suites
  integration/         Docker-backed end-to-end tests
docs/
  ir.md                the operation IR wire format, specified
  protocol.md          the runtime protocol between the engine and an SDK, specified
  adr/                 architecture decision records
```

Most of these are currently a `doc.go` recording the package's purpose and the milestone that
fills it in.

---

## Architecture and decisions

Three contracts carry the whole design: the **operation IR** between SDKs and drivers, the
**driver contract** with its two capability axes, and the **runtime protocol** between the
engine and a language SDK.

The IR's wire format is written down in [`docs/ir.md`](docs/ir.md) and the runtime protocol in
[`docs/protocol.md`](docs/protocol.md) — the two documents a third-party SDK implements against.
Both are checked by the test suite against the code they specify, so neither can drift from it.

The reasoning behind each contract — including the options that were rejected and why — lives in
[`docs/adr/`](docs/adr/README.md):

| # | Decision |
|---|---|
| [0001](docs/adr/0001-architecture.md) | Go core with an operation IR in the middle |
| [0002](docs/adr/0002-runtime-protocol.md) | Bidirectional JSON-RPC over stdio for language runtimes |
| [0003](docs/adr/0003-ir-versioning.md) | Version the operation IR explicitly |
| [0004](docs/adr/0004-safety-model.md) | Lock impact as a capability axis; zero-downtime as a v0.1 property |
| [0005](docs/adr/0005-logical-type-encoding.md) | Encode logical types as canonical strings in the IR |
| [0006](docs/adr/0006-ir-operation-shape.md) | Flat discriminated operations, one representation per schema fact |
| [0007](docs/adr/0007-ir-document-envelope.md) | A versioned document envelope, outside the checksum boundary |
| [0008](docs/adr/0008-reverse-operations.md) | Every operation declares an inverse or a reason it has none |
| [0009](docs/adr/0009-migration-checksum.md) | A migration checksum is a scheme-versioned pair |
| [0010](docs/adr/0010-recorded-absence-of-a-default.md) | Recorded prior state distinguishes "no default" from "nothing recorded" |
| [0011](docs/adr/0011-driver-contract.md) | The driver contract: a session-scoped connection and an explicitly wired registry |
| [0012](docs/adr/0012-capability-resolution.md) | Capability resolution: one rule per operation, resolved against one server |
| [0013](docs/adr/0013-migration-history.md) | Migration history is an append-only log, keyed by the run rather than by the row |
| [0014](docs/adr/0014-online-change-capture.md) | Online change capture reads the write-ahead log, and cutover waits for a human |
| [0015](docs/adr/0015-payload-dependent-refusals.md) | A driver refuses payloads, not operation kinds |
| [0016](docs/adr/0016-lock-impact-is-a-duration.md) | Lock impact is a duration, not a lock level |
| [0017](docs/adr/0017-transaction-boundaries.md) | A transaction is a closure the driver brackets, not a handle the caller holds |
| [0018](docs/adr/0018-declared-dependencies.md) | A dependency is declared by a migration, never computed by one |
| [0019](docs/adr/0019-history-reconciliation.md) | Reconciliation replays the whole log, and a half-applied migration is a fifth state |
| [0020](docs/adr/0020-lock-contention.md) | Lock contention: one budget, no retry, and a holder read after the fact |
| [0021](docs/adr/0021-apply-executor.md) | The apply queue reconciles under the lock, and a batch belongs to one database |
| [0022](docs/adr/0022-rollback-executor.md) | A rollback inverts the whole batch before it runs any of it |
| [0023](docs/adr/0023-batch-narrowing.md) | `--to` names the state to end in, and narrowing is a prefix of the run order |
| [0024](docs/adr/0024-dirty-runs.md) | A run is dirty when its statements outlived the failure |
| [0025](docs/adr/0025-sql-source-language.md) | SQL is a source language, and `migr create` writes what `Parse` reads |
| [0026](docs/adr/0026-dry-run-and-sql-sources.md) | A dry run reads what an apply ensures |
| [0027](docs/adr/0027-read-only-reports.md) | A read-only report takes no lock |
| [0028](docs/adr/0028-validation-report.md) | A validator reports in phases |
| [0029](docs/adr/0029-failure-presentation.md) | A failure is a document too |
| [0030](docs/adr/0030-runtime-protocol-specification.md) | The runtime protocol, specified |
| [0031](docs/adr/0031-protocol-codec-and-framing.md) | The codec, and what ends a session |
| [0032](docs/adr/0032-host-callback-server.md) | The host callback server, and the SQL a migration wrote |
| [0033](docs/adr/0033-where-the-mode-lives.md) | The mode belongs to the call, and the engine owns it |
| [0034](docs/adr/0034-runtime-process-lifecycle.md) | The runtime process, and what outlives what |
| [0035](docs/adr/0035-generated-go-runner.md) | Running a Go migration |
| [0036](docs/adr/0036-escape-hatch-statements-in-a-preview.md) | An escape-hatch statement in a preview |

Worth reading before proposing a change to any of the three contracts. The rejected options are
usually the part that answers "why is it like this?".

---

## Contributing

> The project's full engineering contract lives in a `CLAUDE.md` that is deliberately not
> committed, and comments throughout this repository cite it by section. Everything that binds
> a contributor is restated below or in [`docs/adr/`](docs/adr/README.md), and the rules that
> can be mechanically enforced are — `make lint` fails the build on them rather than leaving
> them to a reviewer's memory.

**The gate.** `make check` must be clean: the comment-rule check and `golangci-lint` across both
modules, then unit and golden tests. CI runs the identical targets on Linux and macOS.

**Rules that are not local decisions.**

- **Core never imports a concrete implementation.** `ir`, `schema`, `plan`, `engine`,
  `validate`, `capability`, `history`, and `checksum` must not import `internal/driver/<name>`
  or `internal/runtime/<name>`, and drivers must not import one another. Wiring happens through
  a registry that `cmd/migr` builds explicitly — a driver exports a constructor rather than
  registering itself from an `init`, so a duplicate name is an error someone can act on.
  Enforced by `depguard`.
- **No raw SQL outside a driver package.** Not in the engine, not in tests above the driver.
- **Adding an IR operation is a five-part change**, all in one PR: the op type plus a JSON
  round-trip test, a `Compile` in *every* driver or an explicit `Unsupported` with a reason, a
  golden file per driver, a defined reverse operation or an `Irreversible` marker, and an entry
  in [`docs/ir.md`](docs/ir.md). The last one is enforced: the test suite reads the
  specification and fails if an operation is missing from it.
- **Planning paths are deterministic.** No `time.Now`, no randomness, no reliance on map
  iteration order in anything producing IR, plans, or SQL. Inject a clock; sort before
  iterating. Enforced by `forbidigo`; golden tests depend on it.
- **Applied migrations are immutable.** A checksum mismatch is a hard error, never an
  auto-repair.
- **Every migration has both directions.** `Down` may declare itself irreversible with a
  reason; it may not be silently absent.
- **Every lock is released**, on every path, including the crashed one.

**Comments.** Two hard rules, and they are unusual enough to state plainly: **no comments
inside a function body**, and **a doc comment is one line** — for functions, types, and struct
fields alike. If a block needs a comment to be understood, extract it into a named function —
the name is visible at every call site, and a name that stops fitting the code is obvious in a
way a stale comment never is. Multi-line context belongs in the package `doc.go`, in `docs/`,
or in an ADR. Tooling directives (`//nolint:`, `//go:`, `// Deprecated:`) are exempt.

These two are enforced by [`internal/lint`](internal/lint/), a small `go/ast` check that
`make lint` runs before `golangci-lint`. It walks the filesystem rather than loading packages,
which is deliberate: a walk crosses into the nested `sdk/go` module on its own, and it sees
files behind `//go:build integration` that a default `golangci-lint` run skips entirely.
`nolintlint` guards the `//nolint:` exemption, so a directive that names no linter, carries no
reason, or suppresses nothing cannot be used to smuggle prose into a function body.

**Errors.** Wrap with `%w`. No `panic` in library code. Every user-facing error names the
migration and the offending operation.

**When to write an ADR.** Any change to the IR, the runtime protocol, the driver contract, or
the capability model. See [`docs/adr/README.md`](docs/adr/README.md) for the template and the
practice — the Considered Options section is not optional.

**Commits.** Conventional commits, scoped where useful: `feat(driver/postgres): ...`.

---

## License

[MIT](LICENSE) © 2026 Edip Emre Bodur
