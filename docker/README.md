# migr images

Every release publishes two multi-platform images (`linux/amd64`, `linux/arm64`) to
`ghcr.io/emrebdr/migr`, each signed keyless with cosign (ADR 0101).

| Tag | Base | For | Size |
|---|---|---|---|
| `<version>`, `latest` | `gcr.io/distroless/static-debian12:nonroot` | `.sql` projects | ~25 MB |
| `<version>-node`, `latest-node` | `node:24-slim` | TypeScript projects | ~260 MB |

`latest` and `latest-node` move only on a final release; a pre-release gets its version tag alone.

Both images:
- run `/usr/local/bin/migr` as the entrypoint, so arguments are migr's: `docker run … ghcr.io/emrebdr/migr:0.1.0 up`;
- work in `/work`, where the project is mounted: `-v "$PWD:/work"`;
- run as a non-root user (`nonroot` and `node`). Where migr writes into the mount (SQLite's database
  file, a Go project's `.migr/`), pass `--user "$(id -u):$(id -g)"`.

The minimal image carries CA certificates and tzdata, so TLS to a managed database verifies, and
nothing else: no shell. The node image brings Node only. The project's own `node_modules`, with
`@migr/sdk` in it, comes from the mount. A Go project copies the binary into its own toolchain image:

```dockerfile
COPY --from=ghcr.io/emrebdr/migr:0.1.0 /usr/local/bin/migr /usr/local/bin/migr
```

`make docker-image` builds both from these Dockerfiles for this machine as `migr:test` and
`migr:test-node`, and `make test-docker` runs them against a database the way a pipeline does.
