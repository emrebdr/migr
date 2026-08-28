# 0042. The TypeScript migration contract

## Status

Accepted

## Date

2026-08-28

## Context

[ADR 0041](0041-typescript-package-scaffold.md) left `@migr/sdk` a package that builds, ships types,
and reports what it speaks — and nothing a migration could be written against. This is the
TypeScript half of [ADR 0037](0037-go-migration-contract.md), whose Go answer
[CLAUDE.md §8](../../CLAUDE.md) names as the worked example to follow.

Two things about the language change the shape of that answer rather than its substance.

**The module system does two of Go's jobs.** Go needs `Register` and a package-level registry
because a migration is a type in a package with no identity of its own; `runtime.Caller` recovers
the filename, and ADR 0037's consequence — *"a migration cannot be registered from a helper"* — is
the price of recovering it. In TypeScript the module **is** the file: the loader imports a path, so
the version is known before the migration is.

**And what Go can only check at registration, TypeScript can check at compile time.** A migration
that authors a `Down` *and* declares itself irreversible is an error in both SDKs. In Go it is
discovered at `migration.discover`; here it does not compile.

The catch is that M7-T5 loads `.js` and `.mjs` as well as `.ts`. A plain-JavaScript migration is
checked by nothing at all — so the compile-time half of this contract is a convenience, never the
enforcement.

## Decision Drivers

- The done-when: *"`Up`, `Down`, `Validate`, and `Dependencies` are exposed with full type
  inference, and both `export default` and named-export styles are supported."*
- [§11](../protocol.md#11-what-an-sdk-owes), the checklist an SDK is finished against.
- Invariant #10: both directions, always — a down may declare itself irreversible with a reason,
  but it may not be silently absent.
- [ADR 0018](0018-declared-dependencies.md): `Dependencies()` returns a declaration, never a
  computation.
- [CLAUDE.md §8](../../CLAUDE.md): *"Idiomatic syntax for the language, identical semantics."*
- `erasableSyntaxOnly` (ADR 0041): no `enum`, no parameter properties, nothing Node cannot strip.

## Considered Options

### A. How a migration declares itself

- **A1: `defineMigration({ ... })`, an identity function that exists for inference** ✔:

  ```ts
  export default defineMigration({
    async up(s) { ... },      // s is inferred; nothing is annotated
    async down(s) { ... },
  });
  ```

  The parameter is contextually typed from the constraint, so an author writes no annotations and
  `strict` still catches a wrong signature. It is Vue's `defineConfig` pattern and it costs one
  exported symbol and zero runtime work.
- A2: a plain object with `satisfies Migration`. **Rejected on merit**: it is exactly as strict
  *when it is written*, and silently as loose as `any` when the clause is forgotten. A helper
  cannot be forgotten — the call site is where the type arrives. The type stays exported, so
  `satisfies` remains available for anyone who prefers it.
- A3: a class implementing an interface, closest to Go's shape. **Rejected**: TypeScript cannot
  express the down/irreversible exclusivity across a class — `implements` checks each member
  separately, not the object against a union — and the export site grows a `new`. A class
  *instance* still loads, because the loader duck-types; it is not the shape the docs teach.

### B. What `up` receives

- **B1: `up(s: Schema)`, one parameter, with the `AbortSignal` on the schema** ✔. §8 asks for
  idiomatic syntax per language and identical semantics only, and an `AbortSignal` in an object is
  what TypeScript does. The facts beside it — `migration`, `direction`, `mode`, `planning` — are on
  the `Schema` in Go too, so this moves one member rather than inventing a shape.
- B2: `up(ctx, s)`, mirroring the Go SDK exactly. **Rejected on §8's terms rather than on merit.**
  ADR 0037 C1's argument carries — a signature that hides cancellation hides the thing that decides
  whether a backfill can be interrupted — and it is answered rather than dismissed: **every callback
  on the `Schema` refuses once the signal is aborted**, so a migration that never reads `s.signal`
  still stops at its next call. What is lost is a migration computing between calls, which is what
  `s.signal` is there for.

  The cost is real: the two SDKs no longer read identically side by side. What
  [M7-T7](../../TASKS.md) compares is the IR, not the signature.

### C. How the optional parts are spelled

- **C1: behaviour is a method, a fact is a property** ✔ — `up`, `down`, and `validate` are
  functions; `dependencies` is a `readonly string[]` and `irreversible` is a `string`.

  ADR 0018 requires that a dependency be *read* from a migration and never *computed* by one, and
  Go can only say that in prose because a method is the only shape an interface has. A property
  says it in the type: `dependencies() { return [...] }` does not compile.
- C2: everything a method, member for member with Go. **Rejected**: it buys a checklist that reads
  the same in both languages and spends the one place TypeScript can state an invariant Go can only
  document.

### D. Where the rules are enforced

- **D1: in the types and again at load** ✔. Both, always, and the runtime half is the real one.
- D2: types only. **Rejected on two grounds, either sufficient.** A `.js` or `.mjs` migration is
  loaded by the same runtime and compiled by nothing, so a type is advice it never receives. And
  the named-export style is unconstrainable by *any* type — nothing in TypeScript relates one
  export of a module to another — so `export async function down` beside `export const irreversible`
  is a shape no compiler can refuse.
- D3: runtime only, leaving the types loose. **Rejected**: the done-when asks for inference, and an
  error at `migr up` is worse than the same error in an editor.

### E. Both export styles, and what a module offering both means

- **E1: a default-exported migration, or the parts as named exports, and a module uses one** ✔. A
  module offering both is `ambiguous_module`, naming which named exports it also carried.
- E2: prefer the default export and ignore the named ones. **Rejected**: it is the ambiguity this
  project refuses everywhere else — a file that could mean two things means neither, and the half
  that was ignored is the half the author edited last.

### F. How a migration is identified

- **F1: from the filename the loader imported, parsed by this SDK's own copy of the grammar** ✔.
  There is no `Register`, no module-level mutable state, and no `ErrNoCaller`: a module is a file.
- F2: a `register()` call for parity with Go. **Rejected**: it would be a second registration path
  whose only merit is looking like another language's, and the identity it recovers is one the
  loader already has.

### G. A default export that is a class constructor

- **G1: refused, with the export that would have worked** — `export default new AddAccounts()` ✔.
  One accepted shape per style, and no heuristic deciding whether a function wants `new`.
- G2: instantiate it. **Rejected on cost, not on principle**: telling a class from an ordinary
  function means inspecting a prototype, and a wrong guess constructs something the author never
  asked to construct. The inspection is still done — to write the better message.

### H. Where the sources' import specifiers point

- **H1: `.ts` everywhere, with the build rewriting the declarations** ✔.
- H2: `.js` in the sources and `.ts` in the tests, which is what ADR 0041 concluded.
  **Rejected because it does not work**, and the reason it appeared to is that no source file
  imported another one until this task. Node resolves `./error.ts` and refuses `./error.js`; tsc's
  `rewriteRelativeImportExtensions` fixes that in emitted JavaScript and **does not reach
  declaration output**, where a `.ts` specifier is one no consumer can resolve. So neither
  extension is correct everywhere, and the build finishes the rewrite the compiler started.

## Decision Outcome

`@migr/sdk` exports a contract of one required member and four optional parts, two of them data:

```ts
type Migration = { up: Up; validate?: Validate; dependencies?: readonly string[] } & (
  | { down: Down; irreversible?: never }
  | { irreversible: string; down?: never }
  | { down?: never; irreversible?: never }
);
```

That intersection is deliberate rather than one flat union: `up` sits outside the union, so its
contextual type is unambiguous and inference does not have to pick a member first.

`fromModule(file, namespace)` reads an already-imported module as the migration its filename names,
and `Registry` collects what was read — refusing a duplicate version and reporting every problem at
once, because `migration.discover` answers once. Both take a namespace rather than a path:
importing files is `migr-runtime-node`'s job, and keeping it out means all of this is tested with no
`import()`, no process, and no toolchain.

`Registry` is a value the caller constructs, not module state. Go's registry is a package-level
singleton because `init` has nowhere else to put a migration; here the loader owns what it loaded.

`migr create --lang ts` writes a template that carries **both** export styles and the choice between
an authored `down` and a declared irreversibility. It is checked twice: `internal/cli` asserts what
it says, and `sdk/typescript` extracts it from the Go constant and **typechecks it against the
published package**, resolving `@migr/sdk` by self-reference through the same exports map a consumer
resolves.

## Consequences

**A migration can be written and cannot yet be run.** The schema builder is M7-T3, the protocol
client M7-T4, and the process that executes any of it M7-T5. `Schema` is declared here with the
call's own facts and its signal, and says in one line where the rest arrives. `migr up` against a
TypeScript project refuses, naming M7-T5.

**The compile-time rules are enforced twice and stated once.** Every `@ts-expect-error` in
`migration.test.ts` has a twin in `load.test.ts` built from a plain object — which is what a `.js`
migration is. A rule added to one and not the other is a rule half the migrations do not have.

**One cast carries the JavaScript boundary.** `checked()` returns the module's own object rather
than a picked-apart copy, so a class instance keeps the `this` its methods were written on and a
named export keeps its identity. The cast is sound because it sits immediately below the checks
that make it so, and it is the only one in the package.

**The declaration rewrite is a build step covering a compiler limitation**, and it will stop being
needed if TypeScript ever applies `rewriteRelativeImportExtensions` to declaration output. It is
held by a test that reads every shipped `.d.ts` and refuses a relative `.ts` specifier — so the day
the rewrite is removed by accident, the package fails its own suite rather than a consumer's build.

**`migr create --lang ts` writes a file `migr up` will not run.** That is the same window the Go
template lived in between M6-T7 and M6-T10, and the package doc says so rather than leaving an
author to find out.
