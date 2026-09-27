# 0045. The Node runtime binary

## Status

Accepted

## Date

2026-08-29

## Context

[ADR 0044](0044-typescript-protocol-client.md) left the TypeScript SDK able to hold a whole
conversation with an engine and unable to find a single migration. `run({ load })` takes its loader
as a parameter and nothing supplied one; `load.ts` says so at the top — *"importing files is
migr-runtime-node's job (M7-T5)"* — and every test to that point handed `Registry.add` a namespace
object written by hand.

This is the process: the executable `@migr/sdk` installs, the directory walk behind it, and the two
module hooks that make a `.ts` migration something Node can run with no build step of the author's
own.

## Decision Drivers

- The done-when: *"`.ts`, `.js`, and `.mjs` migrations all load; TypeScript is transpiled via
  esbuild/tsx without requiring a user-side build step."*
- [§9](../protocol.md#9-the-process): no command-line contract, the working directory is the project
  root, and `migrations.dir` arrives absolute in `initialize`.
- [§4](../protocol.md#migrationdiscover): `migration.discover` is *"not the file discovery"* — the
  engine has already read the directory and parsed every filename — but a version the engine
  discovered and the runtime did not load is a failure naming the file.
- [CLAUDE.md §9](../../CLAUDE.md): discovery is strict and flat, with dotfiles the one exemption.
- [ADR 0037](0037-go-migration-contract.md) E: a process-wide change belongs in the entry point, not
  at import.
- [ADR 0041](0041-typescript-package-scaffold.md): this task was already named as the one that ships
  esbuild as a **runtime** dependency.

Every mechanism below was probed against real files before it was chosen, on Node 24.16 with esbuild
0.25.12. Four of those probes decided a question outright, and they are quoted where they did.

## Considered Options

### A. What turns a `.ts` migration into something Node can run

- **A1: `module.registerHooks` with an `esbuild.transformSync` load hook** ✔. The hook is
  **synchronous**, in-thread, and covers `require` as well as `import`. A transform of this
  package's own 542-line `session.ts` costs ~4 ms, so a 200-migration project pays under a second.
- A2: `tsx`. **Rejected**: it brings a second copy of esbuild into the install and answers with its
  diagnostics rather than ours, for machinery that is ~60 lines here.
- A3: Node's own type stripping, with no dependency at all. **Rejected mechanically.**
  `node --input-type=module-typescript -e "enum E { A = 1 }"` is
  `SyntaxError [ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX]: TypeScript enum is not supported in strip-only
  mode` — and the same for parameter properties, `namespace`, and decorators. A migration is
  ordinary application code; refusing a construct its authors' own project uses elsewhere is not a
  transpiler.

`module.register` — the asynchronous, off-thread hook API — was not in the running: a load hook that
must call `transformSync` has to be synchronous, which is what `registerHooks` is.

**esbuild is imported dynamically, inside `install()`**, and marked `external` in both bundles. A
program that merely writes migrations, or imports `@migr/sdk` in its own test, must not pay for a
10 MB transpiler; and a library bundle is not the place to inline one.

### B. How the module system for one file is decided

A `.ts` file has to be emitted as CommonJS or as ESM, and a namespace has to be read as one or the
other. Two probes decided where that answer comes from:

- `context.format` in the load hook is **`null`** for every `.ts` file outside a `"type": "module"`
  package, and for a CommonJS `.js` too. It is populated (`module-typescript`) only inside an ESM
  package.
- Calling `nextLoad` to find out **throws `ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX`** on exactly the
  syntax esbuild is there to handle: Node strips inside the default load for the CommonJS path.

- **B1: read Node's own rule in one place — `src/system.ts`** ✔. `.mjs`/`.mts` are modules and
  `.cjs`/`.cts` are CommonJS whatever the package says; anything else takes the nearest
  `package.json`'s `type`, defaulting to CommonJS. The hook reads it to know what esbuild should
  emit, and the loader reads it to know how to read what came back. One rule, one place, its own
  test file.
- B2: transform every `.ts` to ESM and rely on esbuild's CommonJS detection, which wraps a
  `module.exports` file and re-exports it as a default. **Rejected**: it works, and it silently
  breaks a `.ts` file that calls `require`, whose free variable does not exist in an ES module.
  The walk was needed for `.js` regardless, so doing `.ts` properly costs no extra machinery.
- B3: take the format from a pass-through `nextLoad` for JavaScript and the walk for TypeScript.
  **Rejected**: two sources for one fact, which is how they come to disagree.

**The CommonJS unwrap is not a nicety.** Probed:

```js
const ns = await import(".../dist/cjs/index.js");
Object.keys(ns);                                   // 35 named keys ... and "default"
ns.default.defineMigration === ns.defineMigration; // true
```

Node builds a namespace carrying both the cjs-module-lexer's named exports *and* `default`. Handed
to `fromModule` that is `ambiguous_module` — the SDK correctly refusing a module that appears to use
both export styles at once. So the loader passes `namespace.default` for a CommonJS file, and the
rule stays exactly one line because B1 already knows which files those are.

### C. What the executable is

- **C1: a shebang and three lines, written by `build.mjs` into `dist/esm/`** ✔, the way each
  format's `package.json` is already written there. `dist/` is the answer to what ships.
- C2: a committed `bin/migr-runtime-node.mjs`. **Rejected**: the repository's `.gitignore` ignores
  `bin/`, and it would be the one shipped file no linter reads.
- C3: a second esbuild entry point. **Rejected**: it puts a whole second copy of the library in the
  package for a file that names one export.

`process.test.ts` spawns that file **by path rather than through `node`**, so the shebang and the
executable bit are driven by a test rather than assumed at install time.

### D. What the loader does with a file that is not a migration

- **D1: refuse it, recording the reason** ✔. A subdirectory, a `README.md`, a `helpers.ts`, a
  `.tsx` — each is a refusal, and a dotfile is the one exemption. This is the engine's own rule
  stated a second time, which is what the SDK does with every rule `migration.ts` states and
  `load.ts` restates.
- D2: skip anything that does not parse as a migration. **Rejected**: it permits a `helpers.ts` to
  sit beside the migrations, and it pays for that by letting a misnamed migration — written,
  committed, reviewed — vanish silently. A README failing loudly is by a wide margin the cheaper
  mistake.

**Every refusal joins the registry rather than ending the walk.** `Registry` gains `refuse`, since
it already claims to be *"every migration one loader read, and everything wrong with the set"*, and
`MigrationErrorCode` gains `not_loadable` for a module that threw before it could be read. One
`migration.discover` then names all of them.

### E. How far the loader goes to make a migration's own imports resolve

- **E1: a resolve hook that retries a relative `.js`/`.mjs`/`.cjs` as its TypeScript sibling** ✔,
  and only on `ERR_MODULE_NOT_FOUND`. This is not a convenience: `tsc` forbids writing `./x.ts`
  unless `allowImportingTsExtensions` is on, so `./x.js` is the **only** spelling a TypeScript
  project can use for its own helper — and it is the one Node cannot resolve. Without the retry,
  "no build step" holds only for a migration that imports nothing.
- E2: nothing extra. **Rejected** for that reason.
- E3: also read `tsconfig.json` — `paths` aliases and `compilerOptions` as `tsconfigRaw`.
  **Deferred**: `extends` chains, `baseUrl`, and wildcard matching are a resolver of their own, and
  nothing else here needs one. Stated in the README rather than left to be discovered.

### F. Which error code a directory that cannot be read answers with

- **F1: `-32603 internal_error`, which is what `sdk/go`'s `discover` answers a registration problem
  with** ✔. The engine reads one code for one class of problem whichever language produced it.
- F2: `-32011 migration_failed`, which is what this SDK's dispatch would have reached by default.
  **Rejected**: §7.3 defines it as *"the migration's own code raised"*, which a `README.md` in the
  directory is not — and two SDKs answering differently for one fixture is a defect M7-T7 would not
  catch, because it compares IR.

Neither code is a good fit. A `discovery_failed` in the application band would be, and adding one is
a protocol change with a version line of its own; it is noted here and not taken.

## Decision Outcome

`src/system.ts` holds Node's rule, `src/hook.ts` the two hooks and the one installation,
`src/directory.ts` the walk, and `src/main.ts` the three lines the binary is. `build.mjs` writes
`dist/esm/migr-runtime-node.js`, which `package.json` installs as `migr-runtime-node`.

Source maps are enabled in `main` before anything is loaded, because every `.ts` file now reaches
Node through esbuild and a stack trace into transpiled output names a line the author never wrote.

Two things this deliberately does not do, both stated in the package's README rather than left to be
found: it **does not typecheck**, since esbuild transpiles per file and `tsc --noEmit` stays the
author's own gate; and it does not read `tsconfig.json`, so `paths` aliases do not resolve.

## Consequences

**`migr up` still refuses a TypeScript project, and now names M7-T6.** The SDK exists, and so does
the process it spawns; what is missing is the adapter that starts one. `internal/cli`'s refusal was
rewritten to say that, and its test asserts the new task.

**One test file grew a process and lost a fiction.** `process.test.ts` used to spawn a runtime
written as a string literal, with a registry built in memory; it now spawns the real executable in a
real project — `package.json`, `node_modules/@migr/sdk`, files on disk — and asserts the emitted IR
byte for byte. Every fixture's TypeScript carries an `enum`, so a fixture that only used erasable
syntax could not pass with the transpiler switched off.

**A leak in that file's harness had cost four hours before it was found.** A spawned child that
never exits keeps `node --test`'s per-file process alive indefinitely, because its stdio handles
hold the event loop open. Under mutation that is routine, so M7-T4's sweep wedged on its fourth case
and ran for four hours without finishing — and a commit taken in the meantime captured a mutated
`message.ts` and a stray `.sweepbak`. The harness now kills its child in `finally` whatever the test
made of it, and the sweep bounds every run with a timeout. The mutation testing found this, but not
by catching a mutation: by hanging on one.
