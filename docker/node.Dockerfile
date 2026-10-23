# The image for TypeScript projects (M14-T4, ADR 0101): Node, the version sdk/typescript/.nvmrc pins,
# plus the static binary. The project mounted at /work brings its own node_modules, @migr/sdk among
# them, so nothing is installed here.
FROM node:24-slim

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/migr /usr/local/bin/migr

WORKDIR /work
USER node
ENTRYPOINT ["/usr/local/bin/migr"]
