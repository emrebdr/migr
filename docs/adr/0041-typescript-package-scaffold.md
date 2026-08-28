# 0041. The TypeScript package and its version policy

## Status

Accepted

## Date

2026-08-28

## Context

[`sdk/typescript/`](../../sdk/typescript) held one empty `.gitkeep`. Everything M7 needs — the
migration contract, the schema builder, the protocol client, and the `migr-runtime-node` binary —
lands inside a package that did not exist.

The scaffold is load-bearing for the build in a way its done-when does not show. `ts_run` in the
[Makefile](../../Makefile) is gated on `sdk/typescript/package.json` existing, and printed
`skipped -- created in M7` four times. The moment that file lands, `make build`, `make test`,
`make lint`, and `make fmt` all invoke npm for real — and CI, which calls three of them, had no
Node step at all. So this is the point where the gate grows a second toolchain, and every part of
that had to work on the first commit rather than on the first release.

## Decision Drivers

- The done-when: *"`sdk/typescript` builds to both ESM and CJS, ships type declarations, and its
  version tracks the supported protocol version."*
- [`docs/protocol.md` §10.3](../protocol.md): the framework version, the `irVersion`, the
  `protocolVersion`, and the checksum scheme are four independent lines, and *"nothing couples
  them."*
- The repository's preference for a small dependency set, and for a mechanical check over a
  convention — the reason `goccy/go-yaml` was chosen and the reason every spec copy is held to a
  document rather than to the other copy.
- M7-T5 will ship esbuild as a **runtime** dependency, to transpile a user's own `.ts` migrations
  with no build step of their own.

## Considered Options

### A. What "its version tracks the supported protocol version" means

1. **Declare it**: export `PROTOCOL_VERSION`, hold it to the specification by a test, leave semver
   free.
2. **Encode it**: the package's major version *is* the protocol version.

Chosen: **2**. `1.x.y` speaks protocol 1, held by a test that reads the major and tolerates a
prerelease suffix.

This is worth being plain about, because §10.3 says nothing couples those lines and this couples
one of them to a release number. The reconciliation is that it is a **publishing convention, not a
protocol mechanism**: the engine still offers a set at `initialize` and the runtime still picks one,
and nothing anywhere reads the npm version to decide behaviour. What it costs is real and is
recorded in the package's own README rather than only here — a protocol bump forces a major release
of a package whose API did not change, and a breaking change to the API **cannot** take the major
without misreporting the protocol.

Option 1 honours §10.3 exactly and was rejected because it leaves "tracks" meaning only "is kept in
step by a test", which is a weaker reading of the done-when than the one a user gets from the
version they installed.

Two consequences are left standing deliberately:

- The first publish would be `1.0.0`, which signals a stability the README disclaims. The version is
  therefore **`1.0.0-alpha.0`**: the major matches the protocol, and the prerelease tag keeps npm
  from installing it by default.
- The specification's own examples show `@migr/sdk` at `0.1.0` speaking protocol 1. Those are
  byte-exact round-trip vectors that both copies of the wire types are held to. They are **not**
  re-derived here: `sdk.version` is an opaque string to the protocol, and editing checked vectors to
  reflect one package's release policy is the tail wagging the dog.

### B. What builds the two module formats

1. **tsup** — one config, dual output plus declarations.
2. **esbuild + `tsc --emitDeclarationOnly`**.
3. **`tsc` alone**, two configurations.

Chosen: **2**. esbuild is a dependency the package carries regardless, so the bundles cost nothing
new and every step stays legible. Option 1 adds a wrapper's dependency tree on top of that same
esbuild; option 3 has the fewest dependencies of all but duplicates the transpiler M7-T5 needs, and
still needs a second configuration and manifest shims to emit both formats.

### C. How the two formats are laid out

1. **`index.mjs` / `index.cjs`** in one directory, with `.d.mts` / `.d.cts` copied beside them.
2. **`dist/esm/` and `dist/cjs/`**, each holding a `package.json` naming its own `type`.

Chosen: **2**, and this was settled empirically rather than by preference. Option 1 was built first
and its declarations do not resolve: `tsc` emits one file per source, so `index.d.ts` re-exports
`./version.js`, and a `.d.cts` copy of it looks for a `version.d.cts` that no build produces. The
alternative — one `.d.ts` serving both conditions — is the masquerading case, where a `require()`
consumer is handed ESM-flavoured types under a package whose `type` is `module`.

A directory that states what it holds has neither problem, needs no declaration bundler, and needs
only one `tsc` run. A scratch consumer typechecks against it under `moduleResolution: nodenext` from
both an ESM and a CJS file, and loads both artifacts at runtime.

### D. Test runner and lint

1. **`node --test`**, with lint as `tsc --noEmit` plus prettier.
2. `node --test` plus eslint.
3. vitest plus eslint.

Chosen: **1**. Node 24 strips TypeScript types natively, so the test runner costs nothing — verified
before it was relied on, along with the fact that Node resolves `./dep.ts` and refuses `./dep.js`.
That asymmetry is why the sources import with `.js` specifiers, which `tsc` and esbuild resolve,
while the test files import with `.ts`, which Node resolves.

`erasableSyntaxOnly` is what keeps the zero-dependency runner available rather than merely working
today: Node can strip a type annotation but cannot execute an `enum`, so the compiler refuses the
syntax that would break the runner.

The whole dependency set is four packages. Options 2 and 3 buy static analysis and a richer runner
for a substantially larger tree, and vitest additionally brings a second transform pipeline beside
the esbuild already shipping.

### E. §9's comment rules on TypeScript

1. **Convention now**, a checker later.
2. Extend `internal/lint` to `.ts`.
3. An eslint rule.

Chosen: **1**, and recorded as unenforced rather than assumed. `internal/lint` reads Go with
`go/ast` and filters to `.go` at one line; TypeScript needs its own scanner, which is a parser to
write and test rather than a filter to widen. Option 3 is only coherent if eslint is the lint
choice, which D rejected.

## Consequences

- `make lint`, `make test`, `make build`, and `make fmt` now cross a second toolchain, and CI sets
  up Node on **both** OS legs before any of them run.
- `make tools` installs the npm dependencies as well as golangci-lint, and `ts_run` fails with a
  message naming that target when `node_modules` is absent — npm's own "Cannot find module" names a
  dependency rather than the step that installs it.
- CI runs `npm ci` explicitly rather than relying on `make tools`, whose step is skipped on a tools
  cache hit; the dependencies would then silently not be there.
- `package-lock.json` is committed and `npm ci` is what installs, so no build resolves a version the
  lockfile did not pin.
- The comment rules are unenforced on TypeScript. That is a real gap, it is written in the package's
  README and in TASKS.md, and it wants its own task.
- Nothing yet checks the published package's type resolution across every module mode.
  `@arethetypeswrong/cli` is the tool for it; the scratch consumer typecheck this task did by hand
  is what stands in until something needs the dependency.
