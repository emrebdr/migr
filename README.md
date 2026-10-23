# migr

**Polyglot, database-agnostic schema migrations.**

[![CI](https://github.com/emrebdr/migr/actions/workflows/ci.yml/badge.svg)](https://github.com/emrebdr/migr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Documentation: [emrebdr.github.io/migr](https://emrebdr.github.io/migr/)** — getting started, the
Go, TypeScript and SQL guides, each driver's capabilities and lock costs, the zero-downtime guide,
and the IR and protocol specifications.

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

## Installing

On macOS or Linux, with [Homebrew](https://brew.sh):

```console
$ brew install emrebdr/tap/migr
```

That installs the binary and its bash, zsh and fish completions, and `brew upgrade` keeps it current.
Naming the cask in full is what trusts it under Homebrew's
[tap trust](https://docs.brew.sh/Tap-Trust); a short `brew install migr` needs
`brew trust --cask emrebdr/tap/migr` first.

In a JavaScript project, from npm, on any of the six platforms below:

```console
$ npx @migr/cli version                 # run it once
$ npm install --save-dev @migr/cli      # or pin it beside @migr/sdk, then `npx migr`
```

npm downloads only the binary for your platform, released at the same version as `@migr/cli`. The
package is scoped because the unscoped name `migr` on npm belongs to an unrelated project; its
command is still `migr`.

In a pipeline, from the release images on `ghcr.io` — the minimal one for `.sql` projects, `-node`
for TypeScript ones ([what each holds](docker/README.md)):

```console
$ docker run --rm -v "$PWD:/work" -e DATABASE_URL ghcr.io/emrebdr/migr:0.1.0 up
$ docker run --rm -v "$PWD:/work" -e DATABASE_URL ghcr.io/emrebdr/migr:0.1.0-node validate --require-safe
```

The zero-downtime guide has [a complete GitHub Actions job](docs/zero-downtime.md#a-github-actions-job)
with a `--require-safe` gate. Each image is signed like the archives:
`cosign verify ghcr.io/emrebdr/migr:0.1.0 --certificate-identity-regexp '^https://github.com/emrebdr/migr/\.github/workflows/release\.yml@refs/tags/v' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.

Every release on [GitHub Releases](https://github.com/emrebdr/migr/releases) carries one archive per
platform — `linux`, `darwin` and `windows`, each on `amd64` and `arm64` — named
`migr_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows), plus a `checksums.txt`. The binary is static,
needing no C library and no installer: unpack it and put `migr` on your `PATH`. Shell completions
are in its `completions/` directory.

**Verify what you downloaded.** `checksums.txt` is signed keyless with
[cosign](https://docs.sigstore.dev/cosign/), by a certificate issued to this repository's release
workflow, so there is no public key to fetch:

```console
$ cosign verify-blob \
    --certificate checksums.txt.pem --signature checksums.txt.sig \
    --certificate-identity-regexp '^https://github.com/emrebdr/migr/\.github/workflows/release\.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    checksums.txt
$ sha256sum --check --ignore-missing checksums.txt   # shasum -a 256 -c on macOS
```

Each archive also carries a build-provenance attestation, which the GitHub CLI checks alone:
`gh attestation verify migr_<version>_<os>_<arch>.tar.gz --repo emrebdr/migr`.

The macOS binaries are not notarized. One downloaded through a browser is quarantined, and
`xattr -d com.apple.quarantine ./migr` releases it; `curl` sets no quarantine, and the Homebrew cask
releases it as it installs.

---

## Examples

Four complete projects in [`examples/`](examples/), each with a README, tests and a GitHub Actions
workflow to copy, and each run end to end by this repository's CI:

| Project | Shows |
|---|---|
| [`go-postgres`](examples/go-postgres/) | Go migrations on PostgreSQL: derived, authored and irreversible downs, `CREATE INDEX CONCURRENTLY`, the SDK's test helpers |
| [`ts-mysql`](examples/ts-mysql/) | TypeScript migrations on MySQL: an enum, an authored down, a data migration run in chunks |
| [`mixed-sqlite`](examples/mixed-sqlite/) | SQL, Go and TypeScript in one directory on SQLite, a dependency across languages, an emulated table rebuild |
| [`zero-downtime`](examples/zero-downtime/) | a column replaced across five deploys with `--release`, while an app keeps serving with no failed query |

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
docs/                  the documentation site, built by VitePress (make docs)
  getting-started.md   install, a first migration, the commands
  guides/              writing migrations in Go, TypeScript and SQL
  drivers/             each driver: what it supports, what it costs, what it refuses
  zero-downtime.md     what each engine can change under live traffic, and how to run it
  ir.md                the operation IR wire format, specified
  protocol.md          the runtime protocol between the engine and an SDK, specified
examples/              four complete projects, each run end to end by CI (make test-examples)
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

Running migrations against a database serving traffic is [`docs/zero-downtime.md`](docs/zero-downtime.md):
each engine's lock cost per operation — tables generated from the drivers and held to them by a
test — what cannot be made zero-downtime, shadow tables and their cutover, expand/contract across
deploys, and gating CI on lock cost.

---

## Contributing

> The project's full engineering contract lives in a `CLAUDE.md` that is deliberately not
> committed, and comments throughout this repository cite it by section. Everything that binds
> a contributor is restated below, and the rules that
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
way a stale comment never is. Multi-line context belongs in the package `doc.go`, or in `docs/`.
Tooling directives (`//nolint:`, `//go:`, `// Deprecated:`) are exempt.

These two are enforced by [`internal/lint`](internal/lint/), a small `go/ast` check that
`make lint` runs before `golangci-lint`. It walks the filesystem rather than loading packages,
which is deliberate: a walk crosses into the nested `sdk/go` module on its own, and it sees
files behind `//go:build integration` that a default `golangci-lint` run skips entirely.
`nolintlint` guards the `//nolint:` exemption, so a directive that names no linter, carries no
reason, or suppresses nothing cannot be used to smuggle prose into a function body.

**Errors.** Wrap with `%w`. No `panic` in library code. Every user-facing error names the
migration and the offending operation.

**Commits.** Conventional commits, scoped where useful: `feat(driver/postgres): ...`.

---

## License

[MIT](LICENSE) © 2026 Edip Emre Bodur
