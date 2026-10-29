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

.PHONY: help setup tools easyp-bin fmt fmt-check lint test test-integration bench bench-gen bench-compare readme-art proto builtins check

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: tools easyp-bin ## Install every tool into ./bin (easyp needs the local clone; local dev only)

tools: ## Install the pinned golangci-lint into ./bin
	GOBIN=$(CURDIR)/$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

easyp-bin: ## Build easyp from the local clone into ./bin (local dev only)
	@[ -x $(BIN)/easyp ] || { mkdir -p $(BIN) && cd $(EASYP_DIR) && $(GO) build -o $(CURDIR)/$(BIN)/easyp ./cmd/easyp; }

# The Go files of module $$m, without those of the modules nested in it:
# golangci-lint fmt walks directories, and a nested module's files need that
# module's import sections. testdata is skipped, as golangci-lint skips it.
MODULE_FILES = nested=$$(for o in $(MODULES); do case $$o in $$m/*) printf '%s|' "$${o\#$$m/}";; esac; done); \
	git ls-files --cached --others --exclude-standard -- '*.go' | grep -vE "^($${nested%|})/|(^|/)testdata/"

fmt: ## Format every module (gofumpt + gci from .golangci.yaml)
	for m in $(MODULES); do
		echo ">> fmt $$m"
		( cd $$m && $(CURDIR)/$(BIN)/golangci-lint fmt $$($(MODULE_FILES)) )
	done

fmt-check: ## Fail if any module is not formatted
	for m in $(MODULES); do
		echo ">> fmt-check $$m"
		( cd $$m && $(CURDIR)/$(BIN)/golangci-lint fmt --diff $$($(MODULE_FILES)) )
	done

lint: ## Lint every module of go.work, integration suites included
	for m in $(MODULES); do
		echo ">> lint $$m"
		( cd $$m && $(CURDIR)/$(BIN)/golangci-lint run --build-tags integration )
	done

test: ## Run unit tests (-race) in every module
	for m in $(MODULES); do
		echo ">> test $$m"
		( cd $$m && $(GO) test -race ./... )
	done

test-integration: ## Run integration suites (needs Docker; RATEL_PG_IMAGE overrides postgres:18-alpine)
	( cd cmd/ratel && $(GO) test -tags integration ./tests/... )
	( cd rateltest && $(GO) test -tags integration ./... )
	( cd tests && $(GO) test -tags integration ./... )
	( cd bench && $(GO) test -tags integration ./... )
	( cd examples/shop && $(GO) test -tags integration ./... )

bench: ## Run every benchmark in every module
	for m in $(MODULES); do
		echo ">> bench $$m"
		( cd $$m && $(GO) test -run '^$$' -bench . -benchmem ./... )
	done

# The sqlc release the comparison generates with, run from its image.
SQLC_IMAGE := sqlc/sqlc:1.31.1
# Repetitions of each comparison benchmark, enough for benchstat's intervals.
BENCH_COUNT ?= 10

bench-gen: ## Regenerate the code of every library in the comparison (needs Docker)
	( cd bench && $(GO) run ../cmd/ratel generate )
	( cd bench && docker run --rm -u $$(id -u):$$(id -g) -v $$(pwd):/src -w /src $(SQLC_IMAGE) generate )
	( cd bench && $(GO) generate ./ent )

bench-compare: ## Compare ratel with pgx, sqlc, GORM, bun and ent on PostgreSQL (needs Docker)
	( cd bench && $(GO) test -tags integration -run '^$$' -bench . -benchmem -count $(BENCH_COUNT) . | tee results.txt )
	$(GO) run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e \
		-row /scenario -col /library bench/results.txt | tee bench/results-table.txt

# Pinned fonts of the README art, so text-to-path output is reproducible.
FONTS := $(BIN)/fonts
JETBRAINS_MONO := https://raw.githubusercontent.com/JetBrains/JetBrainsMono/v2.304/fonts/ttf
NOTO_SANS := https://raw.githubusercontent.com/google/fonts/9862abc066c9b83bf84c3e8ed1fc2d8d48f9bbf0/ofl/notosans/NotoSans%5Bwdth,wght%5D.ttf

readme-art: ## Redraw docs/assets/{banner,bench}.svg from bench/results-table.txt (needs python3, inkscape)
	mkdir -p $(FONTS)
	for w in Bold ExtraBold; do
		[ -f $(FONTS)/JetBrainsMono-$$w.ttf ] || curl -fsSL -o $(FONTS)/JetBrainsMono-$$w.ttf $(JETBRAINS_MONO)/JetBrainsMono-$$w.ttf
	done
	[ -f $(FONTS)/NotoSans.ttf ] || curl -fsSL -o $(FONTS)/NotoSans.ttf '$(NOTO_SANS)'
	tmp=$$(mktemp -d)
	trap 'rm -rf "$$tmp"' EXIT
	# Only the pinned fonts are visible, never the system ones.
	printf '<fontconfig><dir>%s</dir><cachedir>%s</cachedir></fontconfig>\n' "$(CURDIR)/$(FONTS)" "$$tmp/cache" > "$$tmp/fonts.conf"
	python3 tools/readme_art.py docs/assets/ratel.svg bench/results-table.txt "$$tmp"
	for n in banner bench; do
		FONTCONFIG_FILE="$$tmp/fonts.conf" inkscape "$$tmp/$$n.svg" --export-text-to-path --export-plain-svg --export-filename=docs/assets/$$n.svg
	done

proto: ## Generate Go from proto/ (needs protoc-gen-go on PATH; no-op until .proto files exist)
	# easyp fails on a module with no sources, so the target is an explicit
	# no-op until the first .proto file lands.
	if find proto -name '*.proto' -print -quit | grep -q .; then
		( cd proto && ../$(BIN)/easyp generate )
	else
		echo ">> proto: no .proto files yet, nothing to generate"
	fi

builtins: ## Generate builtin signatures from pinned PostgreSQL 16, 17 and 18 containers (needs Docker)
	( cd cmd/ratel && $(GO) generate ./internal/builtins )

check: fmt-check lint test ## Run the CI gates: format check, linter, unit tests
