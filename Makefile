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
GOLANGCI_VERSION := v2.11.4
GOLANGCI         := $(CURDIR)/bin/golangci-lint
# Absolute, because the loop below cd's into each module. Relying on golangci-lint's
# upward config search would let the nested module silently pick up a different one.
GOLANGCI_CONFIG  := $(CURDIR)/.golangci.yml

.DEFAULT_GOAL := help

.PHONY: help build test test-integration lint golden-update tools fmt vet tidy check clean

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
define ts_run
	@if [ -f $(TSDIR)/package.json ]; then \
	  echo "==> $(1)  ($(TSDIR))"; \
	  ( cd $(TSDIR) && $(1) ) || exit 1; \
	else \
	  echo "==> $(1)  ($(TSDIR)): skipped -- created in M7"; \
	fi
endef

help:
	@echo "migr -- make targets"
	@echo ""
	@echo "  build             compile both modules and emit $(BIN)"
	@echo "  test              unit + golden tests, no Docker required"
	@echo "  test-integration  Docker-backed tests (build tag: integration)"
	@echo "  lint              golangci-lint across both modules"
	@echo "  golden-update     regenerate golden files -- read the diff before committing"
	@echo ""
	@echo "  tools             install pinned dev tools into ./bin"
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
	@echo "==> go build -o $(BIN) ./cmd/migr"
	@go build -o $(BIN) ./cmd/migr
	$(call ts_run,npm run build)

test:
	$(call go_each,go test ./...)
	$(call ts_run,npm test)

test-integration:
	@if [ -f $(COMPOSE) ]; then \
	  echo "==> docker compose up  ($(COMPOSE))"; \
	  docker compose -f $(COMPOSE) up -d --wait || exit 1; \
	else \
	  echo "==> docker compose: skipped -- $(COMPOSE) created in M3-T7"; \
	fi
	$(call go_each,go test -tags=integration ./...)
	@if [ -f $(COMPOSE) ]; then \
	  echo "    containers left running; 'make clean' tears them down"; \
	fi

lint:
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

fmt:
	$(call golangci_each,fmt)
	$(call ts_run,npm run fmt)

vet:
	$(call go_each,go vet ./...)

tidy:
	$(call go_each,go mod tidy)

check: lint test

clean:
	@echo "==> removing bin/ dist/"
	@rm -rf bin dist
	@if [ -f $(COMPOSE) ]; then \
	  echo "==> docker compose down"; \
	  docker compose -f $(COMPOSE) down -v; \
	fi
