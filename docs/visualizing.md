# Visualizing migrations

`migr graph` draws a project's migrations in two ways:

- **the dependency DAG**: every migration in the order a run uses, with the dependencies they declare
  and the deploy steps of a change across deploys;
- **the schema's evolution** (`--schema`): what each migration changes in the schema, folded forward
  from nothing, and the schema they leave.

```bash
migr graph                                 # the DAG, as text
migr graph --format mermaid                # ... as a Mermaid flowchart
migr graph --format dot                    # ... as a Graphviz digraph
migr graph --schema                        # what each migration changes, as text
migr graph --schema --to VER --format dot  # the schema at VER, as an ER diagram
migr --json graph --schema                 # either view as a JSON document
```

`migr graph` opens no database and takes no lock. It reads the migrations the way `migr validate`
does: it starts each language's runtime and asks every migration what it does, with no database
behind the answers.

## The dependency DAG

Each migration is one node, listed in run order. A solid edge is a dependency the later migration
declares. A dashed edge labeled `next deploy` joins two steps of one change: the later step runs
only under a later release than the step before it. A node also shows the step it declares, and
`shadow_table` if it runs through a shadow table.

This is the [zero-downtime example](../examples/zero-downtime/README.md), which replaces a column in
four steps:

```console
$ migr graph --format mermaid
flowchart TD
  m20261101T120000["20261101T120000_create_users.go"]
  m20261101T120100["20261101T120100_expand_user_email.go<br/>step users.email: expand"]
  m20261101T120200["20261101T120200_backfill_user_email.go<br/>step users.email: backfill"]
  m20261101T120300["20261101T120300_swap_user_email.go<br/>step users.email: swap"]
  m20261101T120400["20261101T120400_contract_user_email.go<br/>step users.email: contract"]
  m20261101T120000 ~~~ m20261101T120100
  m20261101T120100 ~~~ m20261101T120200
  m20261101T120200 ~~~ m20261101T120300
  m20261101T120300 ~~~ m20261101T120400
  m20261101T120100 -.->|next deploy| m20261101T120200
  m20261101T120200 -.->|next deploy| m20261101T120300
  m20261101T120300 -.->|next deploy| m20261101T120400
```

To draw it, paste the output into a fenced `mermaid` block in a GitHub issue, pull request or
markdown file, or render it with mermaid-cli. For DOT output, use Graphviz: `migr graph --format
dot | dot -Tsvg > migrations.svg`. Both diagram formats link each migration invisibly to the next
one in run order. That keeps a migration with no dependencies in its place in the sequence.

## The schema's evolution

With `--schema`, each migration is listed with its operations, followed by the schema lines it
adds (`+`) and removes (`-`). This is the [Go on PostgreSQL example](../examples/go-postgres/README.md),
stopped at its second migration:

```console
$ migr graph --schema --to 20261101T100100
1  20261101T100000_create_accounts.go
   create_table      accounts
   + table       accounts
   + column      accounts.created_at timestamptz not null default current_timestamp
   + column      accounts.email string(255) not null
   + column      accounts.id int64 not null
   + primary_key accounts.<unnamed> (id)

2  20261101T100100_add_accounts_nickname.go
   add_column        accounts.nickname
   add_index         idx_accounts_nickname
   + column      accounts.nickname string(64) null
   + index       accounts idx_accounts_nickname (nickname)

The schema after 20261101T100100_add_accounts_nickname.go:
  table       accounts
  column      accounts.created_at timestamptz not null default current_timestamp
  column      accounts.email string(255) not null
  column      accounts.id int64 not null
  column      accounts.nickname string(64) null
  index       accounts idx_accounts_nickname (nickname)
  primary_key accounts.<unnamed> (id)
```

With `--format mermaid` or `--format dot`, it draws the schema at the end, or at `--to`, as an
entity-relationship diagram. The diagram shows tables, columns with their logical types, the
primary key, unique columns, and one edge per foreign key:

```console
$ migr graph --schema --to 20261101T100100 --format mermaid
erDiagram
  accounts {
    timestamptz created_at
    string(255) email
    int64 id PK
    string(64) nickname "null"
  }
```

Columns are listed by name, because a schema snapshot doesn't record column order. Mermaid's ER
syntax can't write some types, such as `decimal(10,2)` and `enum(a,b)`. For those columns the
diagram shows the type's kind, and the attribute's comment carries the full type.

## Where the schema stops being known

The evolution never guesses. It stops at the first migration whose effect on the schema migr can't
read:

- **A raw operation** is engine SQL. migr can't read what it does to the schema, so a `.sql` project
  stops at its first migration.
- **A migration that reaches the database** can't be read, because `migr graph` opens no database.
  A Go or TypeScript migration that queries the server before it decides what to do stops the
  schema here. In the Go example above that is `add_accounts_tier`, which is why the example stops
  at `--to 20261101T100100`.
- **An operation the schema can't carry**, such as adding a column to a table no migration creates.

The migration that ends the projection says why. Every later migration still lists its operations,
marked `not projected`. A diagram (`--format mermaid` or `dot`) of a moment past that point exits 1
and names the migration. Pass `--to` a version before it.

A migration that runs SQL itself through the escape hatch (`host.db.exec`) is marked with `!`. The
schema doesn't include what those statements do.

## Exit codes

| Code | When |
|---|---|
| 0 | The graph or the evolution was drawn |
| 1 | The migrations directory is missing, a runtime won't start, or a diagram was asked for past where the schema is known |
| 2 | An unknown `--format`; `--json` together with `--format`; `--to` without `--schema`, or naming no migration; dependencies that form a cycle or name no migration |
