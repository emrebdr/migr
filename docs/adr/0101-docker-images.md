# 0101. Each release publishes a minimal distroless image and a Node one to ghcr.io, signed keyless

## Status

Accepted

## Date

2026-10-23

## Context

M14-T4's done-when is a minimal image that runs migrations in CI/CD pipelines, documented with an
example GitHub Actions job that includes a `--require-safe` gate. ADR 0098's release already builds
static `linux/amd64` and `linux/arm64` binaries.

One fact shapes what "minimal" can promise: each migration language needs something different at
run time.

- **A `.sql` project** needs the binary alone.
- **A TypeScript project** needs Node, and its own `node_modules` for `@migr/sdk`.
- **A Go project** needs a Go toolchain, because the engine builds a runner in the user's module
  (ADR 0035).

And the gate matters most where the binary alone isn't enough: every `.sql` statement is `raw`, whose
lock cost is `unknown`, so a `.sql` project passes `--require-safe` only at `table_rewrite`.

## Decision Drivers

- A pipeline should run migr with nothing to install, from an image whose contents are stated.
- The gate the task names should run in the image the guide recommends.
- No new secret, and the same signing story as the archives.

## Considered Options

### A. What images
- **A1: a minimal image for `.sql` projects, plus a Node image for TypeScript; Go copies the binary
  into its own image** ✔ (by agreement).
  - `ghcr.io/emrebdr/migr:<version>`: `gcr.io/distroless/static-debian12:nonroot`, about 25 MB. It
    has CA certificates, so TLS to a managed database verifies, plus tzdata and a non-root user, and no
    shell.
  - `ghcr.io/emrebdr/migr:<version>-node`: `node:24-slim`, the version `.nvmrc` pins, about 260 MB.
  - For Go, `COPY --from=ghcr.io/emrebdr/migr:<version> /usr/local/bin/migr`.
- A2: the minimal image only. **Rejected:** the gate the task names would then run only in images
  users assemble themselves.
- A3: a Go variant too. **Rejected:** about 800 MB, and it pins a toolchain the user's module may not
  want.
- `scratch`. **Rejected:** no CA certificates or tzdata.
- Alpine. **Rejected:** a shell and musl the binary doesn't need.

### B. Where
- **B1: `ghcr.io/emrebdr/migr` alone** ✔ (by agreement). It's pushed with the job's `GITHUB_TOKEN`,
  so there's no new secret.
- B2: Docker Hub as well. **Rejected:** a token to manage, and pull rate limits for users.

## Decision Outcome

A1 and B1.

- **Built by goreleaser's `dockers_v2`.** It produces one multi-platform manifest per tag, with OCI
  labels for source, version, revision, creation and licence. `latest` and `latest-node` move only on
  a final release.
- **Signed by `docker_signs`.** cosign keyless signs each manifest by digest, under the release
  workflow's identity, the same identity ADR 0098 signs `checksums.txt` under.
- **Run as a pipeline runs a tool.** Both images have entrypoint `migr`, work in `/work`, and run as a
  non-root user, so the exit status the job reads is migr's own.
- **Tested from the release's own Dockerfiles.**
  - `make docker-image` builds both over this machine's binary, laid out as `dockers_v2` lays out its
    context.
  - `make test-docker` runs them against compose's `postgres17`, as the documented job does: a `.sql`
    project up and down on the minimal image. On the Node image, `validate --require-safe` exits 5 on a
    retype, then 0 without it, `plan --require-safe` exits 0, and the project goes up and down.
  - The test also checks the version each binary reports and the user each image runs as.
  - CI runs it as the `docker images` job.

## Consequences

### Positive
- A pipeline runs migr with one `docker run`, and the guide's job does exactly that.
- An image is verifiable with `cosign verify`, with no key to fetch.

### Negative
- The Node image is mostly Node, about 260 MB.
- A new GHCR package is private until its owner makes it public, once, after the first release.
- The non-root user can't write into a mount owned by someone else, so SQLite and Go projects pass
  `--user`.

### Neutral
- A `.sql` project gains less from the gate than a typed one does; the guide says so beside the job.
