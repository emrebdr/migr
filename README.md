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

> **Pre-alpha.** migr applies and rolls back migrations against PostgreSQL, SQLite, MySQL and
> MariaDB today, written as `.sql` files, in Go, or in TypeScript. Validation and safety checks,
> schema diffing, and the zero-downtime guarantees are not built yet. Do not depend on it.

Two of the eleven subcommands, since they need no database:

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
  allow_destructive: [raw]
```

Eleven subcommands are registered in [root.go](internal/cli/root.go); the table further down says
which, and which are still planned. Credentials in a DSN are redacted on every output path, so
leaking one takes a deliberate call.

### Now, next, later

**Done — M0 through M5.** The foundations: a two-module layout, a Makefile that cannot skip a
module, a 24-linter gate, a mechanically enforced import boundary, a custom AST check for the
comment rules, and CI on Linux and macOS. Then the neutral middle: a logical type system, the
twenty-operation IR with a versioned envelope and a checksum scheme. Then the contracts every
database plugs into: the driver interface and registry, the two-axis capability model, a
golden-file harness, and a conformance suite that proves a driver's compiler agrees with what it
declared. Then the PostgreSQL driver, the engine — apply, rollback, locking, history
reconciliation, drift — and a CLI, which made `migr up` against a real PostgreSQL with `.sql`
migrations the first thing a person could actually use.

**Done — M6, the runtime protocol and the Go SDK**, which is where the polyglot claim stopped being
a claim. The wire is specified and implemented on both sides, a Go migration is executed by a runner
migr generates and builds inside the user's own module, the SDK covers the whole twenty-operation
IR, and a conformance suite reads what a migration emits off the wire and holds it, byte for byte,
to a set of fixtures that belong to no language. `migr up` then applies a Go migration against
PostgreSQL and `migr down` reverts it, compared by a schema digest taken before and after — a Go
migration authoring no `Down` included, which is the first time the framework's derived inverse has
run against a real database.

**Done — M7, TypeScript**, where "identical IR from two languages" stopped being a claim. The same
twelve fixtures, asked for both directions in both modes, are answered byte for byte by the Go SDK
and by the TypeScript one; neither was compared to the other, and the file they are both compared to
belongs to no language. Both languages then apply and roll back against a real PostgreSQL — and a
TypeScript migration is the first in any language to read the live database and branch on the
answer, and the first to send a statement of its own outside the transaction its operations run in.

**Done — M8, SQLite**, which is where the capability model started earning its keep: seven
operations this engine has no statement for are achieved by rebuilding the table, declared
`Emulated`, compiled against the database rather than purely, and labeled as such in `migr plan`.
Being in-process and pure Go, its live tests need no container — so a real database, an executable
golden file, and an end-to-end `migr up`/`migr down` round trip all sit inside `make check` on both
CI legs.

**Done — M9, MySQL: the hardest rollback case, proven.** This engine has no transactional DDL, so a
migration that fails partway leaves the schema between states — and MySQL 8.0 and MariaDB 11.4 are
the first servers to produce that by failing rather than from a fixture. The run is recorded dirty at
the statement it reached, the next `up` is refused with the row that run wrote, and `migr resolve`
is the way out: resume from the recorded index, or record what you did by hand. The whole operation
set then applies and **reverses** against both servers, compared by the server's own printer, which
makes this the first engine whose emulated inverse has run at all.

**Now — M12, seeds and data transformations.** Validation and safety (M10) and introspection and
schema diffing (M11) are done; then the zero-downtime milestone (M13) — where "safe against a live production
database" becomes something a test suite proves rather than something the README asserts.

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

> **Both halves are real.** The Go SDK is the contract, the protocol client, and the whole
> twenty-operation schema builder (M6-T7, M6-T8); the TypeScript SDK is all of that plus the process
> that runs it and the adapter that starts one — the same builder emitting the same IR, the
> `migr-runtime-node` executable that loads `.ts`, `.js`, and `.mjs` migrations with no build step,
> and an engine that finds Node and this project's `@migr/sdk` for itself (M7-T2 through M7-T6).
> Both are held to the **same** fixtures and emit the same operations for every one of them (M7-T7),
> and both now run against a real PostgreSQL: `migr up` applies a TypeScript migration and `migr
> down` reverts it, compared by a schema digest either side (M7-T8). One migrations directory may
> hold **both**: each file is routed by its own extension, the two interleave in timestamp order
> under a single `migr up`, and each history row records the language of its own migration (M7-T10).
> A migration's declared `Dependencies()` now overlay those timestamps in the run itself, across
> languages, so a merged branch whose files carry the wrong order applies in the right one — and a
> report, which starts no runtime, prints version order and says which order it has (M7-T11).

```go
package migrations

import (
    "context"

    migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddUsers{}) }

type AddUsers struct{}

func (AddUsers) Up(ctx context.Context, s *migr.Schema) error {
    return s.CreateTable("users").
        Column("id", migr.UUID()).
        Column("email", migr.String(255)).
        Column("created_at", migr.TimestampTZ()).Default(migr.Call(migr.FuncCurrentTimestamp)).
        PrimaryKey("id").
        Apply(ctx)
}

func (AddUsers) Down(ctx context.Context, s *migr.Schema) error {
    return s.Table("users").Drop().Apply(ctx)
}
```

Nothing is emitted until `Apply`, and a chain built without it emits nothing at all — so migr
fails such a migration rather than reporting a change it never made. A column is `NOT NULL`
unless the chain says `Null`.

`Down` is optional: leave it out and migr derives the inverse from what `Up` emitted. So are
`Validate` and `Dependencies`, each one method.

```typescript
import { call, defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s
      .createTable("users")
      .column("id", t.uuid())
      .column("email", t.string(255))
      .column("created_at", t.timestamptz()).default(call("current_timestamp"))
      .primaryKey("id")
      .apply();
  },

  async down(s) {
    await s.table("users").drop().apply();
  },
});
```

Nothing is annotated: `s` is inferred, and writing both a `down` and an `irreversible` reason does
not compile. The parts can also be exported one by one — `export async function up(s)` — which is
the style a plain `.js` migration usually takes; a module uses one or the other.

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
| `migr plan --safety` | the same, annotated with each operation's lock cost and what exceeds `safety.max_lock_impact` | **works today** |
| `migr plan --require-safe` | exit `5` where an operation exceeds `safety.max_lock_impact`, for a CI job to block a merge on; `migr validate --require-safe` judges every migration's up half the same way | **works today** |
| `migr validate` | read, compile, and reverse every migration, connecting to nothing | **works today** |
| `migr validate --connect` | the same, plus what the target server declares it cannot perform | **works today** |
| `migr resolve` | settle the migration a stopped run left between states | **works today** |
| `migr diff` | introspect, diff against the desired schema, emit a migration | **works today** |
| `migr seed` | apply data seeds, kept in a history and a directory of their own, scoped per environment | **works today** — list and roll back are planned |

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

Requires **Go 1.26** and **Node 24** — the TypeScript SDK arrived in M7, and `make lint`, `make
test`, and `make build` all cross it. Docker is needed only for `make test-integration`.

`make tools` installs what both toolchains need: golangci-lint into `./bin`, and the SDK's npm
dependencies with `npm ci`.

```console
$ git clone https://github.com/emrebdr/migr.git && cd migr
$ make build          # -> bin/migr
$ ./bin/migr version

$ make test           # unit + golden tests, no Docker required
$ make check          # lint + test -- the gate a change must pass
$ make test-conformance  # every SDK against the same fixtures (needs Node)
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
  source/              per-language migration file formats: sql/ golang/ typescript/
  cli/ config/ version/
sdk/
  go/                  separate module: github.com/emrebdr/migr/sdk/go
  typescript/          npm package @migr/sdk: the migration contract, ESM and CJS
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
| [0037](docs/adr/0037-go-migration-contract.md) | The Go migration contract |
| [0038](docs/adr/0038-go-schema-builder.md) | The Go schema builder |
| [0039](docs/adr/0039-runtime-conformance-fixtures.md) | The runtime conformance fixtures |
| [0040](docs/adr/0040-running-a-go-migration.md) | Running a Go migration from the CLI |
| [0041](docs/adr/0041-typescript-package-scaffold.md) | The TypeScript package and its version policy |
| [0042](docs/adr/0042-typescript-migration-contract.md) | The TypeScript migration contract |
| [0043](docs/adr/0043-typescript-schema-builder.md) | The TypeScript schema builder |
| [0044](docs/adr/0044-typescript-protocol-client.md) | The TypeScript protocol client |
| [0045](docs/adr/0045-node-runtime-binary.md) | The Node runtime binary |
| [0046](docs/adr/0046-node-runtime-adapter.md) | The Node runtime adapter |
| [0047](docs/adr/0047-line-separator-escaping.md) | U+2028 and U+2029 are emitted as UTF-8, and a scanner undoes Go's escape |
| [0048](docs/adr/0048-multi-language-projects.md) | One directory, several languages: route each migration by its own extension |
| [0049](docs/adr/0049-reading-a-declared-dependency.md) | A declared dependency is read once the runtimes are up, and a report names the order it has |
| [0050](docs/adr/0050-sqlite-session-and-pragmas.md) | The SQLite session: one pinned connection, three pragmas, and what an in-process engine changes |
| [0051](docs/adr/0051-sqlite-type-map-and-the-rebuild-boundary.md) | SQLite's type map, and where the table rebuild begins |
| [0052](docs/adr/0052-sqlite-lock-verdicts.md) | SQLite's lock verdicts, and why an emulation cannot be declared before it can be compiled |
| [0053](docs/adr/0053-expanding-an-operation-against-a-session.md) | An operation a driver compiles against the database, rather than against nothing |
| [0054](docs/adr/0054-the-sqlite-table-rebuild.md) | The SQLite table rebuild, as the measurements made it |
| [0055](docs/adr/0055-the-sqlite-file-lock.md) | The SQLite file lock: flock decides, the record reports, and nothing wedges |
| [0056](docs/adr/0056-the-sqlite-history-store.md) | The SQLite history store: asking the catalog where PostgreSQL reads a code |
| [0057](docs/adr/0057-the-mysql-session.md) | The MySQL session: what a read-only transaction does not refuse |
| [0058](docs/adr/0058-compiling-mysql.md) | Compiling MySQL |
| [0059](docs/adr/0059-mysql-lock-verdicts.md) | MySQL's lock verdicts, and the two things a server will say about its own ALTER |
| [0060](docs/adr/0060-the-mysql-migration-lock.md) | The MySQL migration lock, and a holder duration that says what it measures |
| [0061](docs/adr/0061-refusing-a-dirty-database.md) | Refusing a dirty database, and what the row may be asked to say |
| [0062](docs/adr/0062-resolving-a-dirty-migration.md) | Resolving a dirty migration is an append, and a resume is an index into one list |
| [0063](docs/adr/0063-destructive-operations.md) | Destructive operations: who refuses, and what a project may permit |
| [0064](docs/adr/0064-capability-preflight.md) | Capability preflight: who reads the declaration, and when |
| [0065](docs/adr/0065-the-schema-snapshot.md) | The schema snapshot: what a driver-neutral schema can honestly claim |
| [0066](docs/adr/0066-lint-rules.md) | Lint rules: what a severity may decide, and what it may not |
| [0067](docs/adr/0067-the-shipped-lint-rules.md) | The shipped lint rules: what each reports, and what each declines |
| [0068](docs/adr/0068-sdk-test-helpers.md) | SDK test helpers: capture the IR, read the undo answer, and let the engine judge the rest |
| [0069](docs/adr/0069-per-driver-introspection.md) | Per-driver introspection, and what a live schema is allowed to refuse |
| [0070](docs/adr/0070-the-snapshot-differ.md) | The snapshot differ, and what it declines to propose |
| [0071](docs/adr/0071-expanding-as-the-run-reaches-it.md) | Expanding an emulated operation as the run reaches it |
| [0072](docs/adr/0072-rename-detection.md) | Detecting a rename, and asking before acting on one |
| [0073](docs/adr/0073-the-schema-projection.md) | Folding operations onto a schema, writing them back out, and `migr diff` |
| [0074](docs/adr/0074-the-neutral-round-trip.md) | Comparing two moments of a schema through the snapshot rather than the catalog |
| [0075](docs/adr/0075-reference-integrity.md) | Judging the references migrations make against two projections rather than one |
| [0076](docs/adr/0076-seed-migrations.md) | Seeds: a second history on the same session, scoped by directory, run after every migration |
| [0077](docs/adr/0077-data-operations.md) | Data operations, and irVersion 2 as a superset of 1 |
| [0078](docs/adr/0078-chunked-data-operations.md) | Running a data-only migration in chunks, and carrying it on after an interruption |
| [0079](docs/adr/0079-expand-contract-helper.md) | Replacing a column through four migrations, each a phase of one declared change |
| [0080](docs/adr/0080-seed-listing-and-rollback.md) | Listing, rolling back and settling seeds, as subcommands over the seed history |
| [0081](docs/adr/0081-lock-verdicts-resolved-against-the-table.md) | Lock verdicts are pessimistic where they depend on the table, and a session may lower them |
| [0082](docs/adr/0082-statement-lock-retry.md) | A statement that gave up on a lock is tried again, after a jittered backoff, a bounded number of times |
| [0083](docs/adr/0083-server-side-cancellation.md) | A statement whose context ends is stopped on the server, and two bounds end it |
| [0084](docs/adr/0084-transaction-opt-out.md) | A statement may declare it runs outside a transaction, and the migration runs in parts around it |
| [0085](docs/adr/0085-rewriting-unsafe-operations.md) | A session performs an operation more safely than its plain compile, and the plan says so |
| [0086](docs/adr/0086-lock-impact-gate.md) | A plan shows each operation's lock cost, and `--require-safe` fails one above the ceiling |

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
