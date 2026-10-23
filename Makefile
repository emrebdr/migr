# migr build interface. See CLAUDE.md §5 for the documented command surface.
#
# Every target crosses BOTH Go modules. A root `go build ./...` silently skips the
# nested sdk/go module, so a single-module target would report green while the SDK
# is broken.
#
# Requires GNU Make 3.81+ (the version macOS ships). .SHELLFLAGS and .ONESHELL are
# 3.82+ features and are deliberately unused; recipes propagate failure with an
# explicit `|| exit 1` instead. A loop that iterates both modules but discards exit
# codes is worse than a single-module target, because it looks thorough.

MODULES          := . sdk/go
BIN              := bin/migr
TSDIR            := sdk/typescript
# The npm wrapper (M14-T3): plain Node with no dependencies, so it needs no install to test.
NPMCLI           := npm/cli
COMPOSE          := test/docker-compose.yml
# The fallback is load-bearing: git can fail inside a container whose ownership
# does not match the build user, and a build should not die over a version string.
VERSION          ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS          := -X github.com/emrebdr/migr/internal/version.Version=$(VERSION)
GOLANGCI_VERSION := v2.11.4
# Only a maintainer and the release job run goreleaser, so `make tools` leaves it out; the release
# workflow pins the same version.
GORELEASER_VERSION := v2.17.1
GOLANGCI         := $(CURDIR)/bin/golangci-lint
# Absolute, because the loop below cd's into each module. Relying on golangci-lint's
# upward config search would let the nested module silently pick up a different one.
GOLANGCI_CONFIG  := $(CURDIR)/.golangci.yml

.DEFAULT_GOAL := help

FUZZTIME         ?= 30s

.PHONY: help build test test-integration test-conformance test-zerodowntime fuzz lint golden-update tools fmt vet \
        tidy check clean release-check release-snapshot npm-snapshot docker-image test-docker test-examples docs docs-dev

# Run a command in every Go module. The subshell cd means any tool path passed in
# must be absolute -- a relative ./bin/tool resolves at the root and vanishes in sdk/go.
define go_each
	@for m in $(MODULES); do \
	  echo "==> $(1)  ($$m)"; \
	  ( cd $$m && $(1) ) || exit 1; \
	done
endef

# Run a golangci-lint subcommand in every Go module, against the one shared config.
# Resolution and the loop live in a single shell so the resolved path survives; make
# 3.81 gives each recipe LINE its own shell.
define golangci_each
	@if [ -x $(GOLANGCI) ]; then \
	  gcl=$(GOLANGCI); \
	elif command -v golangci-lint >/dev/null 2>&1; then \
	  gcl=golangci-lint; \
	  echo "!!  unpinned golangci-lint from PATH; 'make tools' installs $(GOLANGCI_VERSION)"; \
	else \
	  echo "golangci-lint not found -- run 'make tools'" >&2; exit 1; \
	fi; \
	for m in $(MODULES); do \
	  echo "==> golangci-lint $(1)  ($$m)"; \
	  ( cd $$m && $$gcl $(1) --config $(GOLANGCI_CONFIG) ) || exit 1; \
	done
endef

# Run a command in the TypeScript SDK once it exists. The skip is announced, never
# silent: an invisible skip is how a green run stops meaning anything.
#
# The node_modules check is the difference between an actionable message and npm's own
# "Cannot find module", which names a dependency rather than the step that installs it.
# M7-T1 is where this stopped being a skip, so it is also where a fresh checkout first
# needs `make tools`.
define ts_run
	@if [ ! -f $(TSDIR)/package.json ]; then \
	  echo "==> $(1)  ($(TSDIR)): skipped -- no package.json"; \
	elif [ ! -d $(TSDIR)/node_modules ]; then \
	  echo "==> $(1)  ($(TSDIR)): FAILED -- dependencies are not installed"; \
	  echo "    run \`make tools\`, which installs them with npm ci"; \
	  exit 1; \
	else \
	  echo "==> $(1)  ($(TSDIR))"; \
	  ( cd $(TSDIR) && $(1) ) || exit 1; \
	fi
endef

help:
	@echo "migr -- make targets"
	@echo ""
	@echo "  build             compile both modules and emit $(BIN)"
	@echo "  test              unit + golden tests, no Docker required"
	@echo "  test-integration  Docker-backed tests (build tag: integration)"
	@echo "  test-conformance  cross-language SDK proof (build tag: conformance; needs Node)"
	@echo "  test-zerodowntime every operation under client load, held to a latency budget (tag: zerodowntime)"
	@echo "  fuzz              run each fuzz target for $(FUZZTIME) (make test runs the seeds)"
	@echo "  lint              comment rules + golangci-lint across both modules"
	@echo "  golden-update     regenerate golden files -- read the diff before committing"
	@echo ""
	@echo "  tools             install pinned dev tools into ./bin, and the SDK's npm deps"
	@echo "  fmt vet tidy      formatting, vet, and go mod tidy across both modules"
	@echo "  check             lint + test, the CLAUDE.md §10 gate"
	@echo "  docs              build the documentation site into docs/.vitepress/dist (dead links fail it)"
	@echo "  docs-dev          serve the documentation site locally, rebuilding as pages change"
	@echo "  clean             remove build output and tear down containers"
	@echo ""
	@echo "  release-check     validate .goreleaser.yaml (needs goreleaser $(GORELEASER_VERSION))"
	@echo "  release-snapshot  build every release archive into dist/, unsigned, publishing nothing"
	@echo "  npm-snapshot      release-snapshot, then the seven npm packages packed into dist/npm"
	@echo "  docker-image      build both release images for this machine, as migr:test and migr:test-node"
	@echo "  test-docker       run both images against compose's postgres17 as a pipeline would (Docker)"
	@echo "  test-examples     run every project in examples/ as its README does, each on its own database (Docker)"

# `go build` stamps VCS info from git by default. If .git is present but git
# refuses to read it -- a container copy whose ownership does not match the build
# user is the usual cause -- the build fails with "error obtaining VCS status"
# rather than skipping the stamp. Either fix the ownership
# (git config --global --add safe.directory <path>) or pass -buildvcs=false.
build:
	$(call go_each,go build ./...)
	@echo "==> go build -o $(BIN) ./cmd/migr  ($(VERSION))"
	@go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/migr
	$(call ts_run,npm run build)

# The documentation site (M14-T5): VitePress over docs/ in place, its own npm project so building it
# never touches the SDK's dependencies. A dead in-site link fails the build.
docs:
	@echo "==> npm ci && vitepress build  (docs)"
	@cd docs && npm ci --silent --no-audit --no-fund && npx vitepress build

docs-dev:
	@cd docs && npm ci --silent --no-audit --no-fund && npx vitepress dev

# A release is .github/workflows/release.yml, never a laptop: signing needs that job's OIDC
# token. These two prove the configuration and the six archives locally, signing skipped.
release-check:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not found: go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)"; exit 1; }
	@goreleaser check

release-snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not found: go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)"; exit 1; }
	@goreleaser release --snapshot --clean --skip=sign,docker

# The release's two Dockerfiles over a binary for this machine's architecture, laid out as
# goreleaser's dockers_v2 lays out its context, so what is tested is what is released.
DOCKER_ARCH := $(shell go env GOARCH)
DOCKER_CTX  := dist/docker

docker-image:
	@mkdir -p $(DOCKER_CTX)/linux/$(DOCKER_ARCH)
	@echo "==> go build linux/$(DOCKER_ARCH)  ($(VERSION))"
	@CGO_ENABLED=0 GOOS=linux GOARCH=$(DOCKER_ARCH) go build -trimpath -ldflags "$(LDFLAGS)" \
	  -o $(DOCKER_CTX)/linux/$(DOCKER_ARCH)/migr ./cmd/migr
	@docker buildx build --quiet --load --platform linux/$(DOCKER_ARCH) -f docker/Dockerfile -t migr:test $(DOCKER_CTX)
	@docker buildx build --quiet --load --platform linux/$(DOCKER_ARCH) -f docker/node.Dockerfile -t migr:test-node $(DOCKER_CTX)

test-docker: docker-image
	$(call ts_run,npm run build)
	@docker compose -f $(COMPOSE) up -d --wait postgres17
	@MIGR_IMAGE=migr:test MIGR_NODE_IMAGE=migr:test-node MIGR_VERSION=$(VERSION) COMPOSE=$(COMPOSE) \
	  SDK_DIR=$(TSDIR) test/docker/run.sh

test-examples: build
	@test/examples/run.sh

npm-snapshot: release-snapshot
	@node $(NPMCLI)/build.mjs dist dist/npm
	@cd dist/npm && for p in cli-*/ cli/; do npm pack --silent "./$$p" >/dev/null || exit 1; done && ls *.tgz

# The TypeScript side runs first because `npm test` builds sdk/typescript/dist, and a Go test --
# internal/source/typescript's typecheck of what migr diff renders -- resolves @migr/sdk through that
# package's exports map, which names dist. A fresh checkout has no dist, so run the other way round the
# Go test fails on CI and passes on any machine that built once.
test:
	$(call ts_run,npm test)
	@echo "==> node --test  ($(NPMCLI))"
	@cd $(NPMCLI) && node --test
	$(call go_each,go test ./...)

# `go test -fuzz` drives exactly ONE target per run -- a pattern matching two is an
# error, not a sweep -- so this discovers them and loops. `make test` already runs
# every seed corpus for free; this is the deliberate campaign on top of that, and it
# is not part of `check` because a timed search is not a gate.
#
# A crasher is written to the package's testdata/fuzz/<Target>/ and must be committed:
# that is what turns a one-off finding into a permanent regression case.
fuzz:
	@for m in $(MODULES); do \
	  ( cd $$m && for p in $$(go list ./...); do \
	      for t in $$(go test -list='^Fuzz' $$p 2>/dev/null | grep '^Fuzz'); do \
	        echo "==> $$t  ($$p, $(FUZZTIME))"; \
	        go test $$p -run='^$$' -fuzz="^$$t\$$" -fuzztime=$(FUZZTIME) || exit 1; \
	      done; \
	    done ) || exit 1; \
	done

# The SDK is built here for the same reason the databases are started here: since M7-T8
# the end-to-end suite runs a TypeScript project, and the runtime it starts is the file
# `npm run build` writes. A target that cannot run itself is a target nobody runs.
#
# -p 1 runs one package at a time. Every tagged package drives the same compose servers, and run all
# at once on a CI runner they slowed each other ~4x: internal/driver/postgres took 812s beside mysql,
# against 180s alone. One at a time keeps each well inside go test's ten-minute default.
#
# Three variables narrow it, which is how CI runs it as parallel jobs. INT_ONLY runs just those
# root-module packages, building no SDK, since only the end-to-end suite starts one; INT_EXCLUDE
# leaves out every package whose whole import path matches a grep -E pattern; INT_SERVICES starts
# only those containers.
INT_ONLY     ?=
INT_EXCLUDE  ?=
INT_SERVICES ?=
test-integration:
	@echo "==> docker compose up  ($(COMPOSE)) $(INT_SERVICES)"
	@docker compose -f $(COMPOSE) up -d --wait $(INT_SERVICES)
ifeq ($(INT_ONLY),)
	$(call ts_run,npm run build)
	@for m in $(MODULES); do \
	  echo "==> go test -tags=integration -p 1  ($$m)"; \
	  ( cd $$m && pkgs=$$(go list -tags=integration ./...) && \
	    if [ -n "$(INT_EXCLUDE)" ]; then pkgs=$$(echo "$$pkgs" | grep -vxE '$(INT_EXCLUDE)'); fi && \
	    go test -tags=integration -p 1 $$pkgs ) || exit 1; \
	done
else
	@echo "==> go test -tags=integration -p 1 $(INT_ONLY)"
	@go test -tags=integration -p 1 $(INT_ONLY)
endif
	@echo "    containers left running; 'make clean' tears them down"

# The zero-downtime proof (M13-T12): every operation against every compose server and
# SQLite, up and down, while eight readers and eight writers work the table, each held to its own
# lock-impact verdict against MIGR_ZERODOWNTIME_BUDGET (1.5s unset). It brings the databases up for
# test-integration's reason, and runs only the packages that carry the tag -- the three drivers, each
# running test/conformance/zerodowntime from its own package, as every conformance suite is run.
#
# One package at a time (-p 1), since it measures latency: run together on one CI runner, MySQL's
# million-row rewrites starved a PostgreSQL read for 4s while a concurrent index build was judged.
# ZD_PACKAGES, ZD_SERVERS and ZD_SERVICES narrow it -- to one driver, to the servers whose subtest name
# begins with ZD_SERVERS, and to the containers those need -- which is how CI runs each slice on a
# runner of its own. ZD_SERVICES=none starts no container, which SQLite needs none of.
ZD_PACKAGES ?= ./internal/driver/postgres/ ./internal/driver/mysql/ ./internal/driver/sqlite/
ZD_SERVERS  ?=
ZD_SERVICES ?=
test-zerodowntime:
	@if [ "$(ZD_SERVICES)" != none ]; then \
	  echo "==> docker compose up  ($(COMPOSE)) $(ZD_SERVICES)"; \
	  docker compose -f $(COMPOSE) up -d --wait $(ZD_SERVICES) || exit 1; \
	fi
	@go test -tags=zerodowntime -count=1 -p 1 -timeout 20m \
	  -run 'TestEveryOperationKeepsItsLockPromiseUnderLoad/^$(ZD_SERVERS)' $(ZD_PACKAGES)

# The cross-language proof: every SDK emits byte-identical IR for the fixtures in
# test/conformance/runtime, which belong to no language. It builds the TypeScript SDK
# first for the reason test-integration brings its own databases up -- a target that
# cannot run itself is a target nobody runs. A machine with no Node fails here rather
# than skipping: asking for this run is a statement that the toolchain is present.
#
# Go's own run needs no tag (the Go toolchain is present by definition when `go test`
# is what is running), so it is part of `make test` and is not repeated here.
test-conformance:
	$(call ts_run,npm run build)
	$(call go_each,go test -tags=conformance ./...)

# commentlint takes a single path where every other target loops MODULES, and that is
# deliberate: it walks the filesystem rather than loading packages, so it descends into
# sdk/go on its own. This is the exact inverse of the `go build ./...` trap above -- a
# walk does not know modules exist. Passing both roots would double-report every finding.
#
# The walk is also why it sees files behind //go:build integration, conformance, and
# zerodowntime, which a default golangci-lint run skips entirely.
#
# It runs first because it is the cheaper of the two gates.
lint:
	@echo "==> commentlint ./..."
	@go run ./internal/lint/cmd .
	$(call golangci_each,run ./...)
	$(call ts_run,npm run lint)

# Packages come BEFORE -update. `go test -update ./...` stops parsing at the
# unknown flag and falls back to package ".", which is not a package, so the
# target fails with a message pointing at entirely the wrong thing.
golden-update:
	@echo "==> go test ./internal/driver/... -update"
	@go test ./internal/driver/... -update

tools:
	@mkdir -p bin
	@echo "==> installing golangci-lint $(GOLANGCI_VERSION) into ./bin"
	@GOBIN=$(CURDIR)/bin go install \
	  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@if [ -f $(TSDIR)/package-lock.json ]; then \
	  echo "==> npm ci  ($(TSDIR))"; \
	  ( cd $(TSDIR) && npm ci ) || exit 1; \
	fi

fmt:
	$(call golangci_each,fmt)
	$(call ts_run,npm run fmt)

vet:
	$(call go_each,go vet ./...)

tidy:
	$(call go_each,go mod tidy)

check: lint test

clean:
	@echo "==> removing bin/ dist/ $(TSDIR)/dist/"
	@rm -rf bin dist $(TSDIR)/dist
	@if [ -f $(COMPOSE) ]; then \
	  echo "==> docker compose down"; \
	  docker compose -f $(COMPOSE) down -v; \
	fi
