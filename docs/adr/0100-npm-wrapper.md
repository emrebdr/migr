# 0100. npm installs migr as @migr/cli, which pins one optional package per platform to its own version

## Status

Accepted

## Date

2026-10-23

## Context

M14-T3's done-when is that `npx migr` downloads the correct platform binary, with the wrapper's
version and the binary's version pinned together. ADR 0098's release builds the six binaries. A
JavaScript project already depends on `@migr/sdk` for its TypeScript migrations, and an npm package
lets it run migr with no Go toolchain.

Three facts shaped the decision:

- **The unscoped name `migr` is taken.** It was published in 2013 by an unrelated project and carries
  a `migr` bin, so `npx migr` runs that package today. The `@migr` scope is unclaimed.
- **Install scripts often don't run.** pnpm 10 runs none by default, many CI setups pass
  `--ignore-scripts`, and a mirror or an air-gapped registry can't reach GitHub at install time.
- **Each binary is about 21 MB.** One package holding all six would be about 125 MB for every install.

## Decision Drivers

- `npx` should get exactly one binary, the one for the machine, through the registry alone.
- The wrapper and the binary can't drift apart: not in what is published, not in what is installed.
- A release that fails partway should be safe to run again.

## Considered Options

### A. The name
- **A1: `@migr/cli`, bin `migr`** ✔ (by agreement). It sits under the scope `@migr/sdk` already uses.
  The done-when becomes `npx @migr/cli`.
- A2: `migr-cli`. **Rejected:** unscoped, apart from the SDK.
- A3: wait for `migr` through npm's name dispute. **Rejected as a blocker:** it can still be pursued,
  and `migr` would then be a package depending on `@migr/cli`.

### B. How the binary arrives
- **B1: six `@migr/cli-<os>-<arch>` packages, each gated by `os` and `cpu`, as optional
  dependencies of `@migr/cli`** ✔ (by agreement). This is the pattern esbuild and biome use. npm skips
  every optional dependency whose platform doesn't match, so it downloads one binary and runs nothing.
- B2: a postinstall that downloads the archive from GitHub Releases and checks it against
  `checksums.txt`. **Rejected:** install scripts are often disabled, and it needs GitHub reachable.
- B3: one package holding all six. **Rejected:** about 125 MB for every install.

## Decision Outcome

A1 and B1.

- **The pin is exact, and it is checked three times.**
  - `npm/cli/build.mjs` writes all seven packages from goreleaser's `dist/` at one version, with every
    optional dependency pinned to exactly that version (never a range). It refuses any release that
    isn't exactly the six targets.
  - `npm/cli/publish.mjs` runs the runner's own binary and refuses one whose `version --json` isn't
    `v<version>`.
  - The launcher refuses to start a platform package whose version isn't its own.
- **The launcher**, `bin/migr.js`, is plain CommonJS for Node 18 and later.
  - It resolves the platform package from its own location, so npm, pnpm and yarn layouts all work.
  - It names the package and the `--omit=optional` cause when that package is missing.
  - It passes the exit status through, because exit 6 means a held cutover.
  - It ignores SIGINT, which the terminal already delivers to migr, so migr stops itself and reports its
    own status. It forwards SIGTERM and SIGHUP.
  - It re-raises a signal that ended the binary.
- **Publishing:**
  - the platform packages go first and the wrapper last, so the registry never holds a wrapper whose
    dependencies are missing;
  - a version the registry already holds is skipped, so a partial release re-runs cleanly;
  - a pre-release goes under the `next` tag;
  - every publish carries npm provenance.

## Consequences

### Positive
- `npx @migr/cli` installs one binary through the registry alone, under `--ignore-scripts` too.
- No combination of wrapper and binary versions other than equal ones can run.

### Negative
- `npx migr` doesn't reach this package until the unscoped name is transferred.
- Seven packages per release, and `NPM_TOKEN` is a long-lived secret until each package switches to
  trusted publishing, which npm only allows once a package exists.
- An install made with `--omit=optional`, or from a lockfile written on another platform, has no
  binary. The launcher says so; it can't fix it.

### Neutral
- Verified against a local Verdaccio registry before any real release. `npx @migr/cli version` from an
  empty directory installed `@migr/cli-darwin-arm64` alone and ran it. An `--omit=optional` install
  printed the stated refusal and exited 1.
