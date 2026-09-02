# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Build/lint/format entry point for dash0-agent-plugin.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

ROOT := $(shell git rev-parse --show-toplevel 2>/dev/null || pwd)
LOCALBIN := $(ROOT)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

GOLANGCI_LINT := $(LOCALBIN)/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.9.0
GOLANGCI_LINT_VERSION_NUMBER = $(patsubst v%,%,$(GOLANGCI_LINT_VERSION))

SHELLCHECK := $(LOCALBIN)/shellcheck
SHELLCHECK_VERSION ?= v0.11.0
SHELLCHECK_VERSION_NUMBER = $(patsubst v%,%,$(SHELLCHECK_VERSION))

.DEFAULT_GOAL := help

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build all Go packages.
	go build ./...

.PHONY: build-binary
build-binary: ## Build one command to $(OUT). Example: make build-binary PKG=./cmd/claude-on-event OUT=bin/on-event
	go build -o $(OUT) $(PKG)

.PHONY: fmt
fmt: ## Format Go code (go fmt).
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: test-scripts ## Run Go unit + integration tests with the race detector.
	go test -race -coverprofile=cover.out ./...

.PHONY: test-scripts
test-scripts: ## Run the unit tests for the Python diagnostic scripts.
	python3 -m unittest discover -s claude/tools -p '*_test.py'

.PHONY: test-e2e
test-e2e: ## Run the build-tagged end-to-end tests.
	go test -tags=e2e -v -timeout=300s ./test/e2e/

.PHONY: go-mod-tidy
go-mod-tidy: ## Run go mod tidy and fail if go.mod/go.sum change.
	go mod tidy
	git diff --exit-code go.mod go.sum

.PHONY: go-version-check
go-version-check: ## Check go.mod Go version matches scripts/docker/Dockerfile.
	./scripts/go-version-check.sh

.PHONY: version-check
version-check: ## Check every manifest and bootstrap pins the same release version.
	./scripts/version.sh check

.PHONY: golangci-lint-install
golangci-lint-install: $(LOCALBIN)
	@installed_version="$$( [ -x $(GOLANGCI_LINT) ] && $(GOLANGCI_LINT) --version 2>/dev/null | awk '{print $$4; exit}' || true )"; \
	if [ "$$installed_version" != "$(GOLANGCI_LINT_VERSION_NUMBER)" ]; then \
	  tmp="$$(mktemp -d)"; staged="$$(mktemp "$(LOCALBIN)/.golangci-lint.XXXXXX")"; \
	  trap 'rm -rf "$$tmp"; rm -f "$$staged"' EXIT; \
	  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
	    | sh -s -- -b "$$tmp" $(GOLANGCI_LINT_VERSION); \
	  install -m 0755 "$$tmp/golangci-lint" "$$staged"; \
	  mv -f "$$staged" $(GOLANGCI_LINT); \
	fi

.PHONY: golangci-lint
golangci-lint: golangci-lint-install ## Run golangci-lint (static analysis + formatters check).
	$(GOLANGCI_LINT) run

.PHONY: golangci-lint-fix
golangci-lint-fix: golangci-lint-install ## Run golangci-lint with --fix.
	$(GOLANGCI_LINT) run --fix

.PHONY: shellcheck-install
shellcheck-install: $(LOCALBIN)
	@installed_version="$$( [ -x $(SHELLCHECK) ] && $(SHELLCHECK) --version 2>/dev/null | awk '/^version:/ {print $$2}' || true )"; \
	if [ "$$installed_version" != "$(SHELLCHECK_VERSION_NUMBER)" ]; then \
	os=$$(uname -s | tr '[:upper:]' '[:lower:]') ;\
	arch=$$(uname -m) ;\
	case "$$arch" in \
	  x86_64|amd64)  arch=x86_64 ;; \
	  arm64|aarch64) arch=aarch64 ;; \
	  *) echo "shellcheck-install: unsupported architecture $$arch" >&2; exit 1 ;; \
	esac ;\
	tmp="$$(mktemp -d)"; staged="$$(mktemp "$(LOCALBIN)/.shellcheck.XXXXXX")"; \
	trap 'rm -rf "$$tmp"; rm -f "$$staged"' EXIT; \
	curl -sSfL -o "$$tmp/shellcheck.tar.gz" \
	  "https://github.com/koalaman/shellcheck/releases/download/$(SHELLCHECK_VERSION)/shellcheck-$(SHELLCHECK_VERSION).$$os.$$arch.tar.gz"; \
	tar -xzf "$$tmp/shellcheck.tar.gz" -C "$$tmp" \
	  "shellcheck-$(SHELLCHECK_VERSION)/shellcheck"; \
	install -m 0755 "$$tmp/shellcheck-$(SHELLCHECK_VERSION)/shellcheck" "$$staged"; \
	mv -f "$$staged" $(SHELLCHECK); \
	fi

.PHONY: shellcheck-lint
shellcheck-lint: shellcheck-install ## Lint all shell scripts with shellcheck.
	find . -name '*.sh' -not -path './bin/*' -print0 | xargs -0 $(SHELLCHECK) -x

.PHONY: lint
lint: go-version-check version-check golangci-lint shellcheck-lint ## Run all static analysis (Go, shell, version sync).

.PHONY: ci
ci: lint test ## Run the full CI check set locally.
