SHELL := bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c

GO ?= go
BIN := bin

# easyp v1 is not published yet: it is built from the developer's local clone.
# Local development only; CI never needs it.
EASYP_DIR := $(HOME)/devel/github/yaroher/easyp

# Pinned so the same binary checks local code and CI.
GOLANGCI_VERSION := v2.15.0

# golangci-lint does not cross module boundaries: lint, format and test each
# module of go.work in isolation, exactly as an external consumer sees it.
MODULES := $(shell $(GO) work edit -json | sed -n 's/.*"DiskPath": "\([^"]*\)".*/\1/p')

.DEFAULT_GOAL := help

.PHONY: help setup tools easyp-bin fmt fmt-check lint test test-integration bench proto check

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: tools easyp-bin ## Install every tool into ./bin (easyp needs the local clone; local dev only)

tools: ## Install the pinned golangci-lint into ./bin
	GOBIN=$(CURDIR)/$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

easyp-bin: ## Build easyp from the local clone into ./bin (local dev only)
	@[ -x $(BIN)/easyp ] || { mkdir -p $(BIN) && cd $(EASYP_DIR) && $(GO) build -o $(CURDIR)/$(BIN)/easyp ./cmd/easyp; }

fmt: ## Format every module (gofumpt + gci from .golangci.yaml)
	for m in $(MODULES); do
		echo ">> fmt $$m"
		( cd $$m && $(CURDIR)/$(BIN)/golangci-lint fmt )
	done

fmt-check: ## Fail if any module is not formatted
	for m in $(MODULES); do
		echo ">> fmt-check $$m"
		( cd $$m && $(CURDIR)/$(BIN)/golangci-lint fmt --diff )
	done

lint: ## Lint every module of go.work
	for m in $(MODULES); do
		echo ">> lint $$m"
		( cd $$m && $(CURDIR)/$(BIN)/golangci-lint run )
	done

test: ## Run unit tests (-race) in every module
	for m in $(MODULES); do
		echo ">> test $$m"
		( cd $$m && $(GO) test -race ./... )
	done

test-integration: ## Run the integration suite in tests/ (needs Docker; RATEL_PG_IMAGE overrides the image, default postgres:18-alpine)
	( cd tests && $(GO) test -tags integration ./... )

bench: ## Run every benchmark in every module
	for m in $(MODULES); do
		echo ">> bench $$m"
		( cd $$m && $(GO) test -run '^$$' -bench . -benchmem ./... )
	done

proto: ## Generate Go from proto/ (needs protoc-gen-go on PATH; no-op until .proto files exist)
	# easyp fails on a module with no sources, so the target is an explicit
	# no-op until the first .proto file lands.
	if find proto -name '*.proto' -print -quit | grep -q .; then
		( cd proto && ../$(BIN)/easyp generate )
	else
		echo ">> proto: no .proto files yet, nothing to generate"
	fi

check: fmt-check lint test ## Run the CI gates: format check, linter, unit tests
