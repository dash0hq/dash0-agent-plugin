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

.DEFAULT_GOAL := help

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build all Go packages.
	go build ./...

.PHONY: build-binary
build-binary: ## Build one command to $(OUT). Example: make build-binary PKG=./cmd/on-event OUT=bin/on-event
	go build -o $(OUT) $(PKG)

.PHONY: fmt
fmt: ## Format Go code (go fmt).
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: ## Run Go unit + integration tests with the race detector.
	go test -race -coverprofile=cover.out ./...

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

.PHONY: golangci-lint-install
golangci-lint-install: $(LOCALBIN)
	@[ -f $(GOLANGCI_LINT) ] || { \
	set -e ;\
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCALBIN) $(GOLANGCI_LINT_VERSION) ;\
	}

.PHONY: golangci-lint
golangci-lint: golangci-lint-install ## Run golangci-lint (static analysis + formatters check).
	$(GOLANGCI_LINT) run

.PHONY: golangci-lint-fix
golangci-lint-fix: golangci-lint-install ## Run golangci-lint with --fix.
	$(GOLANGCI_LINT) run --fix

.PHONY: shellcheck-lint
shellcheck-lint: ## Lint all shell scripts with shellcheck.
	@command -v shellcheck >/dev/null 2>&1 || { echo "error: shellcheck is not installed"; exit 1; }
	find . -name '*.sh' -not -path './bin/*' -print0 | xargs -0 shellcheck -x

.PHONY: lint
lint: go-version-check golangci-lint shellcheck-lint ## Run all static analysis (Go, shell, version sync).

.PHONY: ci
ci: lint test ## Run the full CI check set locally.
