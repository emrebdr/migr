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
COMPOSE          := test/docker-compose.yml
# The fallback is load-bearing: git can fail inside a container whose ownership
# does not match the build user, and a build should not die over a version string.
VERSION          ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS          := -X github.com/emrebdr/migr/internal/version.Version=$(VERSION)
GOLANGCI_VERSION := v2.11.4
GOLANGCI         := $(CURDIR)/bin/golangci-lint
# Absolute, because the loop below cd's into each module. Relying on golangci-lint's
# upward config search would let the nested module silently pick up a different one.
GOLANGCI_CONFIG  := $(CURDIR)/.golangci.yml

.DEFAULT_GOAL := help

FUZZTIME         ?= 30s

.PHONY: help build test test-integration test-conformance fuzz lint golden-update tools fmt vet \
        tidy check clean

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
	@echo "  fuzz              run each fuzz target for $(FUZZTIME) (make test runs the seeds)"
	@echo "  lint              comment rules + golangci-lint across both modules"
	@echo "  golden-update     regenerate golden files -- read the diff before committing"
	@echo ""
	@echo "  tools             install pinned dev tools into ./bin, and the SDK's npm deps"
	@echo "  fmt vet tidy      formatting, vet, and go mod tidy across both modules"
	@echo "  check             lint + test, the CLAUDE.md §10 gate"
	@echo "  clean             remove build output and tear down containers"

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

test:
	$(call go_each,go test ./...)
	$(call ts_run,npm test)

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
test-integration:
	@echo "==> docker compose up  ($(COMPOSE))"
	@docker compose -f $(COMPOSE) up -d --wait
	$(call ts_run,npm run build)
	$(call go_each,go test -tags=integration ./...)
	@echo "    containers left running; 'make clean' tears them down"

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
