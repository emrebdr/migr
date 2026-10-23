# Releasing migr

This page is for maintainers. Releases are built, signed and published by
`.github/workflows/release.yml`, never from a laptop: signing needs that job's GitHub OIDC token
(ADR 0098).

## Cut a release

1. Make sure the commit you release is on `main` and its CI run is green.
2. Tag it and push the tag:

   ```console
   $ git tag -a v0.1.0 -m "v0.1.0"
   $ git push origin v0.1.0
   ```

   A tag with a pre-release suffix, such as `v0.1.0-rc.1`, is published as a pre-release.
3. Watch the **Release** workflow. It:
   1. runs `make test`;
   2. builds the six binaries (`linux`, `darwin`, `windows` × `amd64`, `arm64`) and archives them;
   3. writes `checksums.txt` and signs it keyless with cosign (`checksums.txt.sig`,
      `checksums.txt.pem`);
   4. attests the provenance of every archive and the checksum file;
   5. publishes the GitHub release, with a changelog of `feat` and `fix` commits since the previous tag.
4. Check one download against the commands in the README's **Installing** section.

A failed run publishes nothing, or leaves a partial release. Delete the release and its tag, fix
the cause, and tag again.

## Prove the pipeline without publishing

Run the **Release** workflow by hand (**Actions → Release → Run workflow**). It runs every step as a
snapshot, signing and attestation included, and uploads the archives, `checksums.txt` and its
signature as a workflow artifact named `snapshot` instead of publishing. Verify that artifact with
the README's `cosign verify-blob` command, replacing `refs/tags/v` in the identity with the branch
the run used, such as `refs/heads/main`.

Locally, with goreleaser installed (`go install github.com/goreleaser/goreleaser/v2@v2.17.1`):

```console
$ make release-check      # the configuration is valid
$ make release-snapshot   # every archive in dist/, unsigned
```

## The Go SDK is released separately

`sdk/go` is a Go module of its own, so it is versioned by a tag of the form `sdk/go/vX.Y.Z`. Those
tags trigger no binary release: the workflow runs on `v*`, and `.goreleaser.yaml` ignores `sdk/*`
tags when it looks for the previous release.

## Changing the release

`.goreleaser.yaml` and `release.yml` decide what is published and what signs it. The archive names
are a contract for the Homebrew tap, the npm wrapper and the Docker image, so renaming one breaks
them. The goreleaser version is pinned in three places that must agree: `GORELEASER_VERSION` in the
Makefile, and the `version:` of both goreleaser steps in the workflows.
