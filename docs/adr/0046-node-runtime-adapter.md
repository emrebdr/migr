# 0046. The Node runtime adapter

## Status

Accepted

## Date

2026-08-30

## Context

[ADR 0045](0045-node-runtime-binary.md) shipped `migr-runtime-node` and nothing in the engine could
start it: `internal/runtime/node` was a `doc.go`, so `runtimes.New("typescript")` failed at the
registry before `internal/cli`'s refusal was even reached.

This is the adapter, and it is three methods. The pipes, the process group, §8.2's timers and the
kill ladder belong to `internal/runtime.Session` and are already written
([ADR 0034](0034-runtime-process-lifecycle.md)); the build, the stamp and `.migr/` belong to Go
([ADR 0035](0035-go-runner-generation.md)) and have no counterpart here, because Node loads its own
source and the SDK transpiles a `.ts` migration as it imports it.

What is left is two facts — which `node`, and where this project's `@migr/sdk` is — and the decision
of how to get them.

## Decision Drivers

- The done-when: *"Node is discovered, `node_modules` resolution works from the project root, and a
  missing/too-old Node version produces an actionable error."*
- [ADR 0035](0035-go-runner-generation.md): `Discover` **opens no process**, because it answers *can
  this machine run the language*, which a report may ask without paying for a toolchain probe.
- [§9](../protocol.md#9-the-process): no command-line contract, the working directory is the project
  root, and the environment is the engine's own plus whatever the adapter adds.
- The Go adapter's own habit: it asks `go env` and `go list -m` rather than reading `go.mod` and
  reimplementing module resolution.

Every mechanism was probed against real trees before it was chosen, on Node 24.16.

## Considered Options

### A. How `@migr/sdk` is found from the project root

- **A1: ask Node, in one `node -e` probe with the project root as its working directory** ✔.
  Resolution is Node's own, and the probe returns `process.versions.node` beside it.
- A2: climb from the project root looking for `node_modules/@migr/sdk/package.json`.
  **Rejected**: it is a reimplementation of someone else's algorithm, and it is wrong for the layout
  every package manager actually produces. Probed — a project at `mono/packages/app` resolves
  `mono/node_modules/@migr/sdk`, because npm, pnpm and yarn all hoist a workspace's dependencies to
  its root; and Yarn PnP has no `node_modules` at all. A1 gets both right for free, which is the
  same reason `internal/runtime/golang` asks `go list -m` rather than parsing `go.mod`.

The probe is written so that **it cannot throw**, which is what lets one process answer both
questions:

```js
const at = (() => {
  try { return require.resolve("@migr/sdk/package.json"); } catch { return ""; }
})();

process.stdout.write(["migr-node", process.versions.node, at].join("\t") + "\n");
```

`@migr/sdk` exports exactly two paths, and `./package.json` is one of them — which is what makes it
resolvable by name at all.

### B. Which failure is reported when both are true

- **B1: the version first** ✔. A node old enough to matter cannot resolve an `exports`-only package
  either, so it comes back with an empty path as well. Reporting *that* would send an operator to
  `npm install` for a problem npm cannot fix.
- B2: whichever the probe reported first. **Rejected** for that reason. A test asserts the order.

### C. Reading the probe's own line

- **C1: mark it, and scan for the marked line** ✔. Stdout is not guaranteed to be empty: an ambient
  `NODE_OPTIONS=--require ./x` that prints would otherwise be read as a version, and the failure
  ("node reported version `some startup banner`") would name the wrong thing entirely.
- C2: read the first line. **Rejected**: it is one environment variable away from being wrong, and
  the diagnostic it produces sends the reader to the wrong file.

### D. Where the executable's path comes from

- **D1: `@migr/sdk`'s own `bin` field** ✔, joined to the resolved package's directory and stat'd.
  The location of the built file is stated once, in the package that builds it, and this adapter
  reads it. Two tests hold that up: one asserts the package declares it, and one asserts what the
  adapter does with what it finds.
- D2: hard-code `dist/esm/migr-runtime-node.js`. **Rejected**: the same path in two repositories'
  worth of places, where one of them is the one that could change it.

It is run **through `node`** rather than as `node_modules/.bin/migr-runtime-node`, which is a shell
script on POSIX and a `.cmd` on Windows — the file both of those point at is the one `bin` names.

**The resulting `Command` carries an argument, where the Go adapter's carries none.** That is not in
tension with §9: what §9 forbids is a *runtime* depending on argv, and `migr-runtime-node` reads
none. Naming a script to `node` is how the program is spelled.

### E. Where the version floor lives

- **E1: `MinimumNode = 24`, with a test holding it to `@migr/sdk`'s `engines.node`** ✔. Two
  statements of one requirement, and neither can move without the other failing a build.
- E2: parse `engines.node` at runtime. **Rejected**: `>=24`, `^24.1.0`, `24.x` and `>=22 <25` are
  all things npm writes, and a migration tool that has to answer *what do I do with a range I cannot
  read* has taken on a semver implementation to avoid one constant.

### F. Whether `NODE_OPTIONS` is cleared to defend the protocol stream

- **F1: leave the environment alone** ✔, which is §9's rule.
- F2: clear or sanitise `NODE_OPTIONS`. **Rejected**: it would break a legitimate
  `--max-old-space-size` on a large backfill, and the hazard it guards against fails loudly anyway —
  a print from a preloaded module is a line the engine cannot frame, which is the hole
  [ADR 0044](0044-typescript-protocol-client.md) B already documents for module scope. What is
  guarded instead is the *probe*, by C1.

### G. Whether `migr up` accepts a TypeScript project after this

- **G1: no — the refusal stays, now naming M7-T8** ✔.
- G2: lift it, since nothing is missing. **Rejected on precedent, which turned out to be
  unambiguous**: `cmd/migr/runtimes.go` was *created in M6-T6* and registered the Go adapter there,
  and Go stayed refused by `internal/cli` for four more tasks — first naming the missing SDK, then
  naming M6-T10, which lifted the gate as part of the end-to-end proof. Nothing a user can reach has
  been run against a real database yet, and `executable` already names the task that will.

So `adapterTask` is deleted and `runnable` keys its refusal on `executable` alone.

## Decision Outcome

`internal/runtime/node` holds `Name`, `Discover` — `exec.LookPath` and nothing else — and a `Command`
that probes, reads the package's `bin`, and returns
`runtime.Command{Path: <node>, Args: []string{<executable>}, Dir: p.Root}`.

`cmd/migr/runtimes.go` registers it, which is the one place that may name a concrete adapter
(invariant #1).

## Consequences

**The adapter is exercised end to end without a database.** `internal/runtime/node`'s own tests
start a real `runtime.Session` over `driver.Offline`, discover a `.ts` migration on disk, and read
its operations off the wire — because `host.schema.apply` is recorded rather than executed, which is
the same property that lets `migr validate` run with no server. That is M7-T8's proof minus
PostgreSQL, and it means a wrong `Path`, `Args` or `Dir` fails a test rather than a user.

**A fake toolchain is what makes the interesting failures testable.** This machine has one node and
it is the right version, so an old release, a node that exits non-zero, and a node whose stdout
carries something else are only reachable by writing one onto `PATH`. The same trick proves
`Discover` opens no process: a fake that fails whenever it runs must not bother it.

**Those tests are untagged, with announced skips.** Two of them need the real toolchain and the built
package, and they say so when either is missing. Tagging them `conformance` would have hidden them
from `golangci-lint`, which reads no tagged file, and from CI, which runs `lint`, `test` and `build`
and nothing else — the argument M2-T4 made against tagging the driver conformance suite. CI installs
Node on both OS legs, so they run there.

**What is still missing is a run.** `migr up` against a TypeScript project reaches the gate and
names M7-T8; `migr create --lang ts` and the whole SDK are unaffected. M7-T7 is next, and it needs
nothing from this package beyond `New` — its conformance run lives here because depguard permits
only `cmd/migr` to name a concrete runtime.
