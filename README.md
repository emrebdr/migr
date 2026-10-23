# migr

**Schema migrations you write once, in the language you already use, and run safely against any
supported database — including a live, loaded one.**

[![CI](https://github.com/emrebdr/migr/actions/workflows/ci.yml/badge.svg)](https://github.com/emrebdr/migr/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[Documentation](https://emrebdr.github.io/migr/) ·
[Getting started](docs/getting-started.md) ·
[Examples](examples/) ·
[Zero-downtime guide](docs/zero-downtime.md)

---

A migration says **what** changes, never **how** a particular engine does it:

```go
func (AddUsers) Up(ctx context.Context, s *migr.Schema) error {
    return s.CreateTable("users").
        Column("id", migr.UUID()).
        Column("email", migr.String(255)).
        PrimaryKey("id").
        Apply(ctx)
}
```

migr compiles that into PostgreSQL, MySQL, MariaDB or SQLite statements, works out what each one
will cost a database serving traffic, and runs it under bounded lock waits. The same migration
written in TypeScript produces the same result, byte for byte.

## Why migr

- **Database-independent.** Migrations describe changes in a neutral operation format with logical
  types (`string(255)`, `timestamptz`, `uuid`, …). Each driver compiles it for its own engine. Where
  an engine can't do something directly, the driver says so up front: native, emulated (shown in
  `migr plan`), or refused with a reason. It never quietly does less.
- **Language-independent.** Write migrations in **Go**, **TypeScript** or plain **SQL**, and mix them
  in one directory. A shared conformance suite holds every SDK to byte-identical output.
- **Safe against production.** Every operation's lock cost is read from the connected server's
  version, not assumed. Every DDL statement runs under a lock timeout with bounded retry, so a
  3 ms `ALTER` can't queue behind a long query and take a table offline. CI can refuse a migration
  that costs more than you allow.
- **Online changes that wait for you.** For changes no lock timeout can make cheap, migr copies the
  table into a shadow and keeps it current from the replication log (MySQL/MariaDB binlog,
  PostgreSQL logical decoding), without triggers. It then swaps the tables only when you confirm.
- **Rollback you can trust.** Every migration has both directions. A missing `Down` is derived from
  `Up`, a one-way migration says so with a reason, and editing an applied migration is a hard error.
  migr never repairs a migration on its own.

## Status

> **Pre-1.0, and not yet released.** migr runs end to end today against PostgreSQL 16 and 17,
> MySQL 8.0, MariaDB 11.4 and SQLite, in all three languages, including the zero-downtime features.
> The first release (v0.1) is being prepared. Until it ships, [build from source](#building-from-source),
> and expect the interfaces to change.

| | Supported |
|---|---|
| **Databases** | PostgreSQL 16, 17 · MySQL 8.0 · MariaDB 11.4 · SQLite |
| **Languages** | Go · TypeScript / JavaScript (Node 24) · SQL |
| **Platforms** | Linux, macOS, Windows, each on amd64 and arm64 |

## Quick start

This runs against a SQLite file, so you need no database server.

```console
$ mkdir app && cd app
$ migr init                      # writes migr.yaml and migrations/
$ migr create add_users          # writes migrations/<timestamp>_add_users.sql
```

Point `migr.yaml` at SQLite, under `databases.default`:

```yaml
databases:
  default:
    driver: sqlite
    dsn: ./app.db
```

Write the migration:

```sql
-- migr:up
CREATE TABLE users (
  id    INTEGER PRIMARY KEY,
  email TEXT NOT NULL UNIQUE
);

-- migr:down
DROP TABLE users;
```

Then preview it, run it, and roll it back:

```console
$ migr plan       # what would be sent, sending nothing
$ migr up         # apply what is pending
$ migr status     # applied / pending / missing / drifted / dirty / waiting
$ migr down       # roll back the newest batch
```

The [getting started guide](docs/getting-started.md) walks through this with full output, then
switches to PostgreSQL or MySQL.

## Writing migrations

### Go

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

### TypeScript

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

Both emit the same operations. A few rules apply in every language:

- A column is `NOT NULL` unless you say otherwise.
- Nothing is emitted until `Apply` / `apply()`. A chain that is never applied fails the migration,
  rather than reporting a change that never happened.
- `Down` is optional. Leave it out and migr derives the inverse from what `Up` emitted.
- A migration can declare dependencies on other migrations, across languages, and can read the live
  database before deciding what to do.

`.sql` migrations are also supported. They trade database independence for familiarity: the SQL is
passed through as written, so they need an authored `-- migr:down`, or a reason under
`-- migr:irreversible:`. See the [Go](docs/guides/go.md), [TypeScript](docs/guides/typescript.md)
and [SQL](docs/guides/sql.md) guides.

## Commands

| Command | What it does |
|---|---|
| `migr init` | Scaffold `migr.yaml` and a migrations directory |
| `migr create <name>` | Write a new migration from a template (`--lang go\|typescript\|sql`) |
| `migr plan` | Print the statements a run would send and, with `--safety`, each one's lock cost |
| `migr up` / `migr down` | Apply or roll back, all at once, by `--step N`, or `--to VER` |
| `migr status` | Each migration's state: applied, pending, missing, drifted, dirty or waiting |
| `migr history` | The execution log, rollbacks and failures included |
| `migr validate` | Read, compile and reverse every migration; `--connect` also asks the target server |
| `migr diff [name]` | Compare the live schema with the migrations and write the migration that closes the gap |
| `migr graph` | Draw the dependency DAG, or with `--schema` the schema each migration builds, as text, Mermaid or Graphviz |
| `migr resolve` | Settle a migration a stopped run left between states: `--resume`, `--applied` or `--rolled-back` |
| `migr cutover` | List shadow-table migrations waiting to swap; `--confirm VER` lets one proceed |
| `migr seed` | Apply data seeds for this database's environment; also `seed status`, `history`, `down`, `resolve` |
| `migr config` | Print the resolved configuration, defaults included, with secrets redacted |
| `migr version` | Version, commit and platform |

Global flags: `--config`, `--database`, `--json`, `-v`/`-vv`, `--lock-timeout`.

`--json` makes stdout a JSON document and stderr JSON logs, so `migr status --json | jq` always
parses. Every failure says what failed, why, and what to do next:

```console
$ migr down
migr: 20261023T171330_add_users.sql: this migration declares that it cannot be undone: the rows are gone
  next: roll back only what is newer with migr down --to <its version>; only a backup taken before it ran reaches further
```

### Exit codes

Stable, and meant for scripts and CI:

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Runtime error: the database, the files or the environment isn't ready |
| `2` | Validation failure: the command line, config or a migration is wrong |
| `3` | Lock contention: the migration lock, or a table lock after every retry |
| `4` | Checksum drift: an applied migration was edited |
| `5` | Lock cost over `safety.max_lock_impact` (`plan` / `validate --require-safe`) |
| `6` | A shadow-table migration is waiting for `migr cutover --confirm` |

Reports such as `migr status` exit `0` even when they find a problem. `migr up` is the gate, and
`migr validate` is the check built for CI.

## Zero-downtime in CI

```console
$ migr validate --require-safe    # exits 5 if any migration costs more than you allow
```

`safety.max_lock_impact` in `migr.yaml` sets that ceiling: `lock_free`, `brief_exclusive` (the
default), `long_exclusive` or `table_rewrite`. The [zero-downtime guide](docs/zero-downtime.md)
covers:

- what each operation costs on each engine;
- what can't be made zero-downtime;
- shadow tables and their cutover;
- changing a column across deploys;
- a complete [GitHub Actions job](docs/zero-downtime.md#a-github-actions-job).

## Installing

> These channels go live with the first release. Until then, [build from source](#building-from-source).

```console
$ brew install emrebdr/tap/migr                 # macOS or Linux, with shell completions
$ npx @migr/cli version                         # npm: run it once
$ npm install --save-dev @migr/cli              #      or pin it beside @migr/sdk, then `npx migr`
$ docker run --rm -v "$PWD:/work" -e DATABASE_URL ghcr.io/emrebdr/migr:0.1.0 up
```

- **Docker:** there are two images. `<version>` is a minimal image for `.sql` projects;
  `<version>-node` adds Node for TypeScript projects. See [`docker/`](docker/README.md).
- **Archives:** every [GitHub release](https://github.com/emrebdr/migr/releases) carries
  `migr_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) for each platform. Each holds a single
  static binary and the bash, zsh and fish completions.
- **Homebrew trust:** name the cask in full, as above, to trust it under Homebrew's
  [tap trust](https://docs.brew.sh/Tap-Trust).
- **npm name:** the npm package is scoped because the unscoped `migr` belongs to an unrelated
  project. Its command is still `migr`.

<details>
<summary><strong>Verifying a download</strong></summary>

`checksums.txt` is signed keyless with [cosign](https://docs.sigstore.dev/cosign/), by a certificate
issued to this repository's release workflow:

```console
$ cosign verify-blob \
    --certificate checksums.txt.pem --signature checksums.txt.sig \
    --certificate-identity-regexp '^https://github.com/emrebdr/migr/\.github/workflows/release\.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    checksums.txt
$ sha256sum --check --ignore-missing checksums.txt    # shasum -a 256 -c on macOS
```

Each archive also has a build-provenance attestation:

```console
$ gh attestation verify migr_<version>_<os>_<arch>.tar.gz --repo emrebdr/migr
```

The images are signed the same way. Verify one with `cosign verify ghcr.io/emrebdr/migr:<version>`
and the same two certificate flags.

The macOS binaries aren't notarized. If you download one in a browser, release it with
`xattr -d com.apple.quarantine ./migr`. Homebrew releases it for you.

</details>

## Examples

Four complete projects, each with a README, tests and a CI workflow to copy, and each run end to end
by this repository's CI:

| Project | Shows |
|---|---|
| [`go-postgres`](examples/go-postgres/) | Go on PostgreSQL: derived, authored and irreversible downs; a concurrent index; the SDK's test helpers |
| [`ts-mysql`](examples/ts-mysql/) | TypeScript on MySQL: an enum, an authored down, a data migration run in chunks |
| [`mixed-sqlite`](examples/mixed-sqlite/) | SQL, Go and TypeScript in one directory, with a dependency across languages and an emulated table rebuild |
| [`zero-downtime`](examples/zero-downtime/) | A column replaced across five deploys while an app keeps serving with no failed query |

## Documentation

The full documentation is at **[emrebdr.github.io/migr](https://emrebdr.github.io/migr/)**:

- [Getting started](docs/getting-started.md)
- Writing migrations in [Go](docs/guides/go.md), [TypeScript](docs/guides/typescript.md) and [SQL](docs/guides/sql.md)
- Drivers: [PostgreSQL](docs/drivers/postgres.md), [MySQL and MariaDB](docs/drivers/mysql.md),
  [SQLite](docs/drivers/sqlite.md). Each page covers what that driver supports, emulates and
  refuses, and what each operation costs.
- [The zero-downtime guide](docs/zero-downtime.md)
- [Visualizing migrations](docs/visualizing.md)
- Specifications for SDK authors: the [operation IR](docs/ir.md) and the
  [runtime protocol](docs/protocol.md)

## How it works

```
   Go SDK      TypeScript SDK      (more languages later)
      │              │                      │
      └──────────────┴──── JSON-RPC ────────┘
                     │
              ┌──────▼───────┐
              │ Operation IR │   versioned, neutral JSON
              └──────┬───────┘
        ┌────────────┼────────────┐
   ┌────▼───┐   ┌────▼───┐   ┌────▼───┐
   │Postgres│   │ MySQL  │   │ SQLite │   each compiles the IR to its own SQL
   └────────┘   └────────┘   └────────┘
```

Three contracts carry the design:

- **The operation IR** between SDKs and drivers. It has 23 operations and 18 logical types, and it
  is versioned.
- **The driver contract.** Each driver answers two questions per operation: can it run, and what
  does it cost a live database.
- **The runtime protocol** between the engine and a language SDK. It is line-delimited JSON-RPC
  over a child process's stdin and stdout.

A Go migration is not special-cased. migr generates a small runner in your module, builds it, and
talks to it over the same protocol a TypeScript migration uses. Both specifications are checked by
the test suite against the code they describe.

## Building from source

You need **Go 1.26** and **Node 24**. Docker is only needed for the integration tests.

```console
$ git clone https://github.com/emrebdr/migr.git && cd migr
$ make tools          # pinned golangci-lint into ./bin, and the SDK's npm dependencies
$ make build          # -> bin/migr
$ ./bin/migr version
```

| Target | Runs |
|---|---|
| `make check` | Lint and unit and golden tests: the gate every change must pass |
| `make test-integration` | End-to-end against PostgreSQL, MySQL and MariaDB in Docker |
| `make test-conformance` | Every SDK against the same fixtures |
| `make test-zerodowntime` | Every operation under concurrent load, held to its declared lock cost |
| `make test-examples` | Every project in `examples/`, as its README says |
| `make docs` | Builds the documentation site; a dead link fails it |
| `make help` | Every target |

The repository holds two Go modules: the root, and `sdk/go`, which is kept separate so an
application that only writes migrations doesn't pull in database drivers. A bare `go build ./...`
skips the nested module, so use the Makefile targets, which cover both. CI runs the same targets on
Linux and macOS.

## Contributing

Contributions are welcome. Before opening a pull request:

1. Run `make check` and make sure it's clean.
2. Use [conventional commits](https://www.conventionalcommits.org/), scoped where useful:
   `feat(driver/postgres): …`.

The rules that keep the design intact, most of them enforced by `make lint`:

- **Core never imports a concrete driver or runtime**, and drivers never import one another. Only
  `cmd/migr` wires them in.
- **No raw SQL outside a driver package**, tests included.
- **A new IR operation is a five-part change** in one PR:
  - the operation type and a JSON round-trip test;
  - a compiler in every driver, or an explicit refusal with a reason;
  - a golden file per driver;
  - a reverse operation, or an irreversible marker;
  - an entry in [`docs/ir.md`](docs/ir.md).
- **Planning is deterministic.** No clock, no randomness and no map-order dependence in anything
  that produces IR, plans or SQL.
- **Applied migrations are immutable**, every migration has both directions, and every lock is
  released, even on a crash.
- **Comments:** none inside function bodies, and a doc comment is one line. Longer context belongs
  in the package's `doc.go` or in `docs/`.
- **Errors** are wrapped with `%w`. Library code never panics. A user-facing error names what
  failed, why, and what to do next.

## License

[MIT](LICENSE) © 2026 Edip Emre Bodur
