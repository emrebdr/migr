# The minimal image (M14-T4): the static binary on distroless, which carries the CA
# certificates a TLS connection to a managed database verifies against, tzdata, and a non-root user,
# and nothing else -- no shell, no package manager. It runs .sql projects; a TypeScript project
# needs node.Dockerfile's image, and a Go project copies /usr/local/bin/migr into its own toolchain.
#
# The release builds it through goreleaser's dockers_v2, which puts each platform's binary at
# $TARGETPLATFORM/migr in the build context; `make docker-image` lays the context out the same way.
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/migr /usr/local/bin/migr

WORKDIR /work
USER nonroot
ENTRYPOINT ["/usr/local/bin/migr"]
