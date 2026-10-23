# @migr/cli

The [migr](https://github.com/emrebdr/migr) command-line tool for your platform, installed from npm.
migr is a polyglot, database-agnostic migration framework: migrations written in Go, TypeScript or
SQL, run against PostgreSQL, MySQL, MariaDB or SQLite, safe on a live database.

```console
$ npx @migr/cli version                 # run it once
$ npm install --save-dev @migr/cli      # or pin it in a project
$ npx migr up
```

Write TypeScript migrations with [`@migr/sdk`](https://www.npmjs.com/package/@migr/sdk) beside it.

## How it installs

This package holds no binary. It depends, optionally, on six packages, one per platform, and npm
installs only the one matching your machine:

| Platform | Package |
|---|---|
| macOS, Apple silicon | `@migr/cli-darwin-arm64` |
| macOS, Intel | `@migr/cli-darwin-amd64` |
| Linux, x86-64 | `@migr/cli-linux-amd64` |
| Linux, arm64 | `@migr/cli-linux-arm64` |
| Windows, x86-64 | `@migr/cli-windows-amd64` |
| Windows, arm64 | `@migr/cli-windows-arm64` |

Every one of them is released at the same version as this package, and `migr` refuses to start if
the two differ. Nothing runs at install time, so `--ignore-scripts` changes nothing.

**If `migr` says its platform package is not installed**, optional dependencies were left out:
`npm install --omit=optional`, `--no-optional`, or a lockfile written on another platform. Reinstall
without them.

## Exit codes

`migr` exits as the binary does, so a script can branch on it: `0` success, `1` runtime error, `2`
validation failure, `3` lock contention, `4` checksum drift, `5` lock-impact threshold exceeded, `6`
waiting for a cutover confirmation. Ctrl-C reaches migr itself, which stops what it is doing safely.

## Why it is scoped

The unscoped name `migr` on npm belongs to an unrelated project, so this is `@migr/cli`. Its command
is still `migr`.
