# 0098. A release is signed once, keyless, on its checksum file, and every archive carries a provenance attestation

## Status

Accepted

## Date

2026-10-23

## Context

M14-T1's done-when is that tagging a release produces signed binaries for linux, darwin and windows
on amd64 and arm64. Until now the only build was `make build`, for the machine it runs on. Three later
tasks depend on what this one publishes: the Homebrew tap, the npm wrapper and the Docker image
(M14-T2 to T4) each download a release archive by name and check it against its checksum.

Two facts shaped the decision:

- **The binary cross-compiles with no C toolchain.** SQLite is `modernc.org/sqlite`, so with
  `CGO_ENABLED=0` one Linux runner builds all six targets. A local snapshot builds them in 38 s.
- **"Signed" can mean three different identities.** A signature over the artifacts says *this came
  from that build*. macOS notarization and Windows Authenticode say *the operating system vouches for
  this publisher*, and each needs a paid, renewed identity: an Apple Developer membership, a
  code-signing certificate.

## Decision Drivers

- No long-lived secret should be able to sign a release outside the release workflow.
- A user should be able to verify a download with one widely available tool and no key to fetch.
- The three downstream channels should verify one thing, not six.

## Considered Options

### A. What signs
- **A1: cosign keyless** ✔ (by agreement). The release job's GitHub OIDC token is exchanged with
  Sigstore for a short-lived certificate naming
  `https://github.com/emrebdr/migr/.github/workflows/release.yml@refs/tags/<tag>`, and the signature
  is recorded in the public transparency log. Nothing is stored, so nothing can leak.
- A2: a GPG key held as a repository secret. **Rejected:** it is a long-lived secret that signs
  anything for anyone who reads it, and it has to be rotated and published.

### B. What is signed
- **B1: `checksums.txt` alone** ✔. It holds every archive's SHA-256, so one verified signature
  covers all six, and the checksum check a downstream channel already does carries the rest.
- B2: each archive separately. **Rejected:** six signatures saying what one already says.

### C. Provenance
- **C1: a GitHub build-provenance attestation over every archive and the checksum file** ✔ (by
  agreement). It records the commit, workflow and runner that built each file, and
  `gh attestation verify` checks it without cosign.

### D. Operating-system signing
- macOS notarization and Windows Authenticode. **Deferred:** both need a paid identity. A browser
  download on macOS is quarantined, and the README gives the remedy. Homebrew (M14-T2) installs
  without the quarantine. To be revisited when a user needs it.

### E. Gate
- **The release job runs `make test` before goreleaser** ✔ (by agreement), so a tag pushed on an
  untested commit cannot publish.

## Decision Outcome

A1, B1, C1, E, with D deferred.

- `.goreleaser.yaml` builds `./cmd/migr` CGO-free and `-trimpath`, with the commit's timestamp on the
  binary and on every archived file, so one tag reproduces the same archives.
- `Version` is stamped `v{{ .Version }}`, the form `make build` stamps from `git describe`.
- Archives are `migr_<version>_<os>_<arch>.tar.gz`, `.zip` on Windows. Each holds the binary,
  `LICENSE` and `README.md`.
- `.github/workflows/release.yml` runs on `v*` tags. `sdk/go`'s own `sdk/go/v*` tags match neither
  the trigger nor goreleaser, whose `git.ignore_tags` excludes them.
- Run by hand, the workflow builds a snapshot through the same signing and attestation steps and
  uploads it instead of publishing.
- CI runs `goreleaser check` on every change, and `make release-check` and `make release-snapshot`
  do the same locally, signing skipped.

## Consequences

### Positive
- No signing secret exists.
- A downstream channel verifies one signature and then checks hashes as it already would.

### Negative
- Verification needs cosign, or the GitHub CLI for the attestation.
- The signature is only as trustworthy as the release workflow file and the branch protection
  around tags. A change to `release.yml` is a change to what signs.
- macOS and Windows show their unidentified-publisher warnings.

### Neutral
- Signing cannot be exercised off GitHub, since it needs the job's OIDC token. The hand-run snapshot
  is how it is proven before a real tag.
