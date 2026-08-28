# `@migr/sdk`

Write database migrations in TypeScript and run them against any engine
[migr](https://github.com/emrebdr/migr) supports.

> **Pre-alpha.** A migration can be written against this contract and cannot yet be run: the schema
> builder, the protocol client, and the `migr-runtime-node` process that executes any of it land in
> M7-T3 through M7-T5. Do not depend on it.

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

### `dependencies` is data, not a method

It is a `readonly string[]` rather than a function, and that is the type saying what the framework
requires: a dependency is **declared** by a migration and never **computed** by one. A graph that
could branch on live data would let `migr plan` and `migr up` order the same migrations differently.
`dependencies() { ... }` is a compile error for that reason.

Everything above is enforced twice — in the types, and again when the module is read, since a `.js`
migration is compiled by nothing and no type can constrain a module's exports against each other.

## The version means something

**The major version is the protocol version this SDK speaks.** `1.x.y` speaks
[`protocolVersion` 1](../../docs/protocol.md); a `2.x.y` would speak protocol 2. That is a
publishing convention rather than a protocol mechanism — the engine and the runtime still negotiate
at `initialize`, where the engine offers every version it speaks and the runtime picks one, so
nothing anywhere reads this package's version to decide behaviour.

It has a cost worth knowing before you read a changelog: a protocol bump forces a major release of
a package whose API may not have changed, and a breaking change to _this API_ cannot take the major
without misreporting the protocol. See
[ADR 0041](../../docs/adr/0041-typescript-package-scaffold.md).

Three tests hold the claim up rather than leaving it to a release checklist: `VERSION` matches
`package.json`, its major matches `PROTOCOL_VERSION`, and `PROTOCOL_VERSION` matches the version
`docs/protocol.md` itself states.

## Both module formats

The package ships ESM and CommonJS side by side, each in a directory that declares its own flavour,
so a plain `.d.ts` beside each bundle is unambiguous:

```
dist/esm/  index.js  index.d.ts  package.json {"type":"module"}
dist/cjs/  index.js  index.d.ts  package.json {"type":"commonjs"}
```

```ts
import { PROTOCOL_VERSION } from "@migr/sdk"; // ESM
const { PROTOCOL_VERSION } = require("@migr/sdk"); // CJS
```

`npm test` builds first and then loads both artifacts, so "builds to both" is measured on every run
rather than asserted by a config file.

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
