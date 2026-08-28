# `@migr/sdk`

Write database migrations in TypeScript and run them against any engine
[migr](https://github.com/emrebdr/migr) supports.

> **Pre-alpha.** This package is a scaffold: it builds, ships types, and reports what it speaks.
> The migration contract, the schema builder, and the protocol client land in M7-T2 through M7-T5.
> Do not depend on it.

```console
npm install @migr/sdk@alpha
```

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

The code follows [CLAUDE.md §9](../../CLAUDE.md)'s comment rules — no comments inside function
bodies, one-line doc comments — by convention. Nothing enforces them here yet: `internal/lint`
reads Go with `go/ast` and TypeScript needs its own scanner.
