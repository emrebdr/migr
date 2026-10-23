# `@migr/sdk`

Write database migrations in TypeScript and run them against any engine
[migr](https://github.com/emrebdr/migr) supports.

> **Pre-alpha.** A migration can be written and run by `migr-runtime-node`, and `migr` cannot yet
> start one: the adapter that spawns this runtime lands in M7-T6. Do not depend on it.

```console
npm install @migr/sdk@alpha
```

## Writing a migration

A migration has one required member and four optional ones. `up` performs the change; everything
else is there when you need it:

```ts
import { defineMigration } from "@migr/sdk";

export default defineMigration({
  async up(s) { ... },                       // required
  async down(s) { ... },                     // or `irreversible`, or neither -- see below
  validate() { ... },                        // this migration's own check
  dependencies: ["20260727T153000"],         // versions this one must follow
});
```

`defineMigration` does nothing at runtime — it returns what it was given. It exists so the members
are contextually typed: `s` is inferred, no annotation is written, and a wrong signature is a
compile error rather than a surprise at `migr up`.

The parts can also be exported one by one, which is the style a `.js` or `.mjs` migration usually
takes. **A module uses one style or the other**; exporting a default migration _and_ named parts is
refused rather than resolved in someone's favour:

```ts
export async function up(s) { ... }
export async function down(s) { ... }
export const dependencies = ["20260727T153000"];
```

### How a migration is undone

Every migration has both directions, and there are exactly three honest answers:

| What you write             | What migr does                             |
| -------------------------- | ------------------------------------------ |
| `down`                     | runs it                                    |
| `irreversible: "<reason>"` | halts a rollback with **your** reason      |
| neither                    | derives the inverse from what `up` emitted |

Writing `down` _and_ `irreversible` does not compile, because both cannot be true. A migration that
sends the database a statement itself has no inverse to derive, so it owes one of the first two.

A `down` that emits nothing says there is nothing to undo, and the rollback is recorded with no
statement run. An `up` that emits nothing is still refused.

### `dependencies` is data, not a method

It is a `readonly string[]` rather than a function, and that is the type saying what the framework
requires: a dependency is **declared** by a migration and never **computed** by one. A graph that
could branch on live data would let `migr plan` and `migr up` order the same migrations differently.
`dependencies() { ... }` is a compile error for that reason.

Everything above is enforced twice — in the types, and again when the module is read, since a `.js`
migration is compiled by nothing and no type can constrain a module's exports against each other.

## Describing a change

Four chains, one per thing a change acts on, each ending in `apply()`:

```ts
import { call, defineMigration, t } from "@migr/sdk";

export default defineMigration({
  async up(s) {
    await s
      .createTable("users")
      .column("id", t.uuid())
      .column("email", t.string(255))
      .column("created_at", t.timestamptz())
      .default(call("current_timestamp"))
      .primaryKey("id")
      .apply();

    await s
      .table("posts")
      .addForeignKey("fk_posts_author", ["author_id"], "users", ["id"])
      .onDelete("cascade")
      .apply();
  },
});
```

`s.table(name)` changes a table that already exists, `s.view(name)` a view, and `s.raw(driver, sql)`
is the escape hatch for what the IR cannot express — it names one engine, so no other will run it.
`.noTransaction()` after a statement declares that it cannot run inside a transaction, as
`CREATE INDEX CONCURRENTLY` cannot: migr runs it on its own, the statements around it in
transactions of their own, and records how far it got after each. It needs an engine reading
`irVersion` 3, and against an older one the migration is refused before anything is sent.

**Nothing is emitted until `apply`**, which sends the whole chain in one call and empties it, so
applying twice does not emit twice. A chain built without it emits nothing at all, and a migration
that reports success having changed nothing is the worst failure this format has — so migr fails
such a migration, naming the object and counting what was never applied. An `apply()` that is sent
and never `await`ed is caught the same way: its failure is surfaced when the call ends rather than
disappearing into a promise nobody read.

Two rules are structural rather than documented, because two SDKs disagreeing about either would
emit different bytes for the same schema change:

- **a column is `NOT NULL`** unless the chain says `.null()`;
- **a new table states its key inline** (`createTable(...).primaryKey(...)`) where an existing table
  states it as an operation (`table(...).addPrimaryKey(...)`). Different calls, so the wrong choice
  is not spellable.

The operations are exported too, and `s.apply(...ops)` takes them — a chain and a hand-written
literal emit the same bytes, which is asserted for all twenty.

`JSON.stringify` happens to escape exactly what
[`docs/ir.md` §3.3](../../docs/ir.md) asks for, including U+2028 and U+2029, which Go's encoder
escapes whatever it is told — so this SDK is the conforming one there and the Go copies undo their
own encoder. A test holds the line
`host.schema.apply` writes to that, because the bytes on the wire come from the envelope's
`stringify` rather than from the operation codec the spec test exercises.

### Replacing a column

`replaceColumn` declares one column changed through four migrations. Each file exports one phase as
its default and restates the whole declaration, so an applied file cannot change what it emits
underneath its checksum:

```ts
import { replaceColumn, t } from "@migr/sdk";

export default replaceColumn("users", "email", t.string(255)).with(t.string(320)).expand();
```

| Phase      | Up                                                                                         | Unsafe?                         |
| ---------- | ------------------------------------------------------------------------------------------ | ------------------------------- |
| `expand`   | adds `email_next`, nullable whatever it ends as                                            | its down, once an app writes it |
| `backfill` | fills it from `email` through `through(...)`, `copy` by default, in chunks                 | no                              |
| `swap`     | relaxes `email`, tightens `email_next`, and renames them past each other                   | yes, both ways                  |
| `contract` | drops `email_old`; irreversible after `lower`, `upper` or `trim`, a copy-back after `copy` | yes                             |

`renameTo(name)` ends the column under a new name instead, so swap renames nothing and contract
drops the original; `via` and `retire` override the two derived names. Each unsafe step sends a
warning naming what it breaks for an app version still serving, in plan mode too. Each phase also
declares itself a **step** of the change, waiting on the one before it, and migr runs a step only
under a release after the one that applied its prior step: a deploy runs `migr up --release <build>`
(or sets `MIGR_RELEASE`), and the four land a deploy apart, the run holding at the next step rather
than failing. A hand-written migration declares the same with a `phase` property —
`{ change, name, after }`. The backfill's `data_transform` and the contract's `drop_column` are
destructive by kind, so a project permits both in `validation.allow_destructive`.

### Running through a shadow table

A migration changing one large table declares `online: SHADOW_TABLE`, and migr then performs it
through a copy of the table rather than as a plain `ALTER`:

1. it copies the rows in chunks while reading the table's writes from the server's replication log;
2. it holds the copy in sync until someone confirms the cutover;
3. it swaps the copy in, keeping the original under a derived name.

The wait is indefinite by design. A server that cannot read its own log is refused before anything
is created, naming what it lacks: MySQL and MariaDB read their binary log, PostgreSQL decodes its WAL
through a replication slot, and SQLite has no log to read. Any other value of `online` is refused
when the file is loaded.

```ts
import { columns, defineMigration, SHADOW_TABLE } from "@migr/sdk";

export default defineMigration({
  online: SHADOW_TABLE,
  async up(s) {
    await s.table("people").addIndex("idx_people_region", "region").apply();
  },
  async down(s) {
    await s.table("people").dropIndex("idx_people_region").was(columns("region")).apply();
  },
});
```

### Logical types

`t` holds the eighteen types [`docs/ir.md` §5.2](../../docs/ir.md) defines, and a type **is** its
canonical spelling: `t.string(255)` is `"string(255)"`, branded so a bare string cannot be passed
where a type is wanted.

```ts
t.uuid(); // "uuid"
t.decimal(10, 2); // "decimal(10,2)"
t.enum("draft", "live"); // "enum(draft,live)"  -- order is part of the type
t.array(t.string(255)); // "array(string(255))"
```

What the type system can read from a literal, it refuses before anything runs — a length of `0`, a
fractional precision, an empty `enum`, a label listed twice. What it cannot read, it does not
pretend to: an argument computed at runtime is a plain `number`, so **every rule is checked twice**
and a bad type throws where it was written.

## Asking the database

A migration is a program, not a document: it can ask the database a question and branch on the
answer. Five callbacks beyond `apply`, all of them awaited:

```ts
export default defineMigration({
  async up(s) {
    const { rows } = await s.query("SELECT count(*) AS n FROM users WHERE email IS NULL");

    if (rows[0]?.[0] !== "0") {
      s.log("warn", "backfilling first", { table: "users" });

      await s.exec("UPDATE users SET email = '' WHERE email IS NULL");
    }

    await s.table("users").alterColumn("email").notNull().wasNullable(true).apply();
  },
});
```

**Every value a read returns is a string or `null`.** Not a number, not a boolean — a 64-bit integer
does not survive a round trip through a JSON number in JavaScript, and a migration backfilling a
table keyed on `bigint` is exactly the case that breaks, silently, at row 2^53. Convert from the
string, where you know the column's type and JSON does not.

`args` are bound and positional, and the placeholder style is the **target engine's** — `$1` on
PostgreSQL, `?` on MySQL and SQLite. `(await s.info()).driver` is how a migration knows which. That
is a real leak of the engine into a migration, and it is the reason the operation IR exists: a
migration that only emits operations is portable, and one that queries is portable only as far as its
SQL is.

### What the escape hatch costs

`s.exec(...)` sends a statement the IR cannot express, and it costs three things:

- it is **recorded rather than run** under `migr plan`, and the result says so with
  `executed: false` — so a migration branching on `rowsAffected` can tell it is being previewed
  rather than being told a lie;
- it runs **outside the transaction the operations run in**, committed on its own, so a migration
  that fails afterwards does not undo it;
- the engine cannot read the statement, so it cannot invert it, cannot classify its lock impact, and
  cannot tell another engine what it means.

Reach for it last. A statement whose undo matters belongs in a chain, where the engine can invert it.

### Asking before emitting

`s.capability(op)` reports what the target engine makes of one operation, on both axes at once —
whether it is `native`, `emulated`, or `unsupported`, and what it costs a live database. A migration
is not obliged to ask; this exists so one can take a different route when an engine would rewrite a
table.

### Two channels for two kinds of thing to say

`s.log(level, message, fields)` reaches `migr --json`'s log stream as an object a script can filter.
Anything the language itself prints goes to **stderr**, which the engine passes through verbatim and
never parses — `console.log` in a migration is redirected there before the first protocol message,
because stdout carries protocol and nothing else.

## How a migration is loaded

Installing this package installs an executable, **`migr-runtime-node`**. It is what the engine
spawns, it speaks the protocol over its stdin and stdout, and it is the only thing that reads your
migration files:

```console
$ ls migrations
20260727T153000_add_users.ts  20260727T153100_add_teams.js  20260727T153200_add_plans.mjs
```

All three load, and each may use either module system — a `.js` file is an ES module where your
`package.json` says `"type": "module"` and CommonJS (`module.exports = { up }`) where it does not,
which is Node's own rule rather than one of ours. `.mjs` and `.mts` are always modules, `.cjs` and
`.cts` always CommonJS.

**TypeScript needs no build step.** Every `.ts` file is compiled by esbuild as it is imported, which
is why esbuild is a real dependency of this package and not a development one. Two things follow, and
both are worth knowing before you hit them:

- **It transpiles and does not typecheck.** A type error does not stop a migration — esbuild removes
  the types and never checks them. `tsc --noEmit` stays your own gate, in your editor and in CI.
- **`tsconfig.json` is not read.** No `paths` aliases, no `extends`, no `compilerOptions`. An import
  that relies on an alias will not resolve.

What _does_ resolve is your project's own TypeScript, spelled the only way `tsc` permits:

```ts
import { tenants } from "../db/schema.js"; // resolves ../db/schema.ts
```

`tsc` refuses a `./x.ts` specifier unless `allowImportingTsExtensions` is on, so `.js` is what a
TypeScript project must write — and it is the one Node cannot find. The runtime retries the
TypeScript sibling when there is no such JavaScript file. Stack traces point at your `.ts` line,
because source maps are on.

### The migrations directory holds migrations

It is flat, and everything in it is a migration. A subdirectory, a `README.md`, a `helpers.ts`, a
filename the grammar refuses — each one fails, and **every problem is reported at once** rather than
the first. Files beginning with `.` are the one exemption.

That is deliberately strict. The alternative is skipping what does not parse, and the cost of that is
a migration someone wrote, committed, and had reviewed going quietly unrun. Shared helpers live
outside the directory, where the import above reaches them.

There is a second exemption and it is narrow: a file whose name is a **well-formed migration** in an
extension this runtime does not load — `20260727T153000_add_users.go` — belongs to another runtime and
is skipped, because one directory may hold more than one language. A `README.md` is not that, and is
still refused. Nothing is skipped by everything in silence: migr routed every file by its extension
and holds what each runtime reports here against what it sent.

## The version means something

**The major version is the protocol version this SDK speaks.** `1.x.y` speaks
[`protocolVersion` 1](../../docs/protocol.md); a `2.x.y` would speak protocol 2. That is a
publishing convention rather than a protocol mechanism — the engine and the runtime still negotiate
at `initialize`, where the engine offers every version it speaks and the runtime picks one, so
nothing anywhere reads this package's version to decide behaviour.

It has a cost worth knowing before you read a changelog: a protocol bump forces a major release of
a package whose API may not have changed, and a breaking change to _this API_ cannot take the major
without misreporting the protocol.

Three tests hold the claim up rather than leaving it to a release checklist: `VERSION` matches
`package.json`, its major matches `PROTOCOL_VERSION`, and `PROTOCOL_VERSION` matches the version
`docs/protocol.md` itself states.

## Testing a migration

`@migr/sdk/testing` is a subpath of its own, so the library a migration imports carries no test
helpers. There are three, and what separates them is who can actually answer the question.

**What IR did this half emit?** The SDK can answer that alone, in this process, reaching nothing:

```ts
import { capture } from "@migr/sdk/testing";

const c = await capture(addUsers);

c.is({
  op: "create_table",
  table: "users",
  columns: [{ name: "id", type: t.uuid(), nullable: false }],
  primary_key: ["id"],
});
```

`is` renders what you expected through the SDK's own encoder and compares the documents, so member
order counts; `c.kinds()` is the looser assertion, and `c.operations`, `c.reads`, `c.statements` and
`c.logs` are there for anything else.

A migration may read the database and branch on the answer, so a stage says what its questions are
answered with. Three of them are **refused rather than guessed** when the stage is silent — a read, a
capability question, and `info()` — because an empty value answered in place of a missing one sends
the migration down a branch your test never meant to ask about:

```ts
await capture(addTier, {
  mode: "plan",
  reads: { "SELECT count(*) FROM users": answer(["count"], ["0"]) },
});
```

Everything else is a copy of the engine's own host. `exec` is recorded and **never performed** —
there is no database here — and answered the way the engine answers it: held under `plan`, reported
performed under `apply` with whatever `affected` says it changed.

**How is this migration undone?** Also the SDK's, because it is a fact about the migration's shape:

```ts
const r = await reversal(addUsers);

r.undo; // "authored" | "declared" | "derived"
r.reason; // the author's reason, where one is declared
r.down.is(...); // empty exactly when the engine derives the inverse
```

It does not invert anything. Every inverse lives in the engine, so that two SDKs cannot reach two
answers for one migration — whether an
operation can be inverted at all is reported by `migr validate` below. The one rollback this can see
is impossible it throws over: an up that sends statements of its own and authors no down, since a
statement has no inverse to derive.

**Does it run, and does the rollback leave anything behind?** That needs the engine, because nothing
in this SDK can compile an operation into SQL:

```ts
const p = project({ dir: "./migrations" });

t.after(() => p.remove());

p.up();
p.validate().problems; // the reverse check among them
p.roundTrip(); // up, down, up, down
p.dsn; // inspect the database with your own driver
```

The database is a throwaway SQLite file (`driver` and `dsn` together point it somewhere real), and
the `migr.yaml` goes under `<project>/.migr/` rather than in a temp directory — the directory holding
it is the runtime's working directory, and this runtime resolves `@migr/sdk` from there. The binary
is `MIGR_BIN`, or `migr` on `PATH`; when there is neither this **fails rather than skipping**, and
`found()` answers the same question without starting anything if you would rather skip deliberately.

`roundTrip()` catches residue: a down that left a table behind makes the second up fail, and the
server is what says so. It is not a comparison of two schema snapshots — that needs introspection the
engine does not expose yet.

**The test file does not go in the migrations directory.** Discovery is strict and dotfiles are its
only exemption, so a `migrations.test.ts` sitting beside the migrations is refused by name and every
command fails before a migration is loaded. `capture` and `reversal` do not care — they take a
migration as a value — and `project` points at the directory from outside it:

```
migrations/20260301T090000_add_users.ts
test/migrations.test.ts                  // project({ dir: "../migrations" })
```

A command that fails throws with what migr wrote on stderr. One case needs more than the exit code:
`migr validate` exits 2 both for _the checks found problems_, whose report is on stdout, and for _this
command was given something it refuses_, whose message is on stderr. `validate()` tells them apart by
whether a document was printed, so a refused filename arrives as an error naming the file rather than
as an empty report.

## Both module formats

The package ships ESM and CommonJS side by side, each in a directory that declares its own flavour,
so a plain `.d.ts` beside each bundle is unambiguous:

```
dist/esm/  index.js  index.d.ts  package.json {"type":"module"}     migr-runtime-node.js
dist/cjs/  index.js  index.d.ts  package.json {"type":"commonjs"}
dist/<fmt>/testing/index.js        # @migr/sdk/testing, which imports ../index.js
```

The test helpers are a second entry point rather than a second bundle: their one non-builtin import
is the library beside them, left external, so the package grows by the helpers and not by a copy of
itself. Node's own package self-reference cannot do that job here — the `package.json` each format
directory carries to declare its `type` shadows the real manifest, so `@migr/sdk` resolves to nothing
from inside `dist/` — which is why the specifier is relative and the build finishes the `.ts` → `.js`
rewrite for it.

```ts
import { PROTOCOL_VERSION } from "@migr/sdk"; // ESM
const { PROTOCOL_VERSION } = require("@migr/sdk"); // CJS
```

`npm test` builds first and then loads both artifacts, so "builds to both" is measured on every run
rather than asserted by a config file. The executable is a shebang and three lines, generated beside
the ESM bundle it loads — one test spawns it by path, so the shebang and the executable bit are
driven rather than assumed.

## Working on it

```console
npm ci        # or `make tools` from the repository root
npm run build # esbuild for the bundles, tsc for the declarations
npm test      # builds, then runs node --test
npm run lint  # tsc --noEmit, then prettier --check
npm run fmt   # prettier --write
```

Node 24 or newer. The tests run TypeScript through Node's own type stripping, with no transpiler in
the loop — `erasableSyntaxOnly` in `tsconfig.json` is what keeps that possible, by making
TypeScript-only runtime syntax a compile error rather than a thing to remember.

**Every import here ends in `.ts`, including in the shipped sources**, because that is the only
specifier Node resolves when it runs them. `rewriteRelativeImportExtensions` turns those into `.js`
in emitted JavaScript and does not reach declaration output, so `build.mjs` finishes the rewrite —
and `dist.test.ts` reads every shipped `.d.ts` and refuses a relative `.ts` specifier, since one
there is a specifier no consumer can resolve.

The code follows [CLAUDE.md §9](../../CLAUDE.md)'s comment rules — no comments inside function
bodies, one-line doc comments — by convention. Nothing enforces them here yet: `internal/lint`
reads Go with `go/ast` and TypeScript needs its own scanner.
