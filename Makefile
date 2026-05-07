# gau Makefile
#
# Common workflow:
#   make ci         — full validation: tidy + vet + lint + race tests + coverage
#   make test       — race tests with tparse output
#   make coverage   — generate HTML coverage report
#   make build      — local binary at bin/gau
#   make smoke      — run the binary against real wayback (manual sanity check)

SHELL          := /bin/bash
GO             ?= go
GOPATH_BIN     := $(shell $(GO) env GOPATH)/bin
GOLANGCI_LINT  ?= $(GOPATH_BIN)/golangci-lint
TPARSE         ?= $(GOPATH_BIN)/tparse
COVER_DIR      := coverage
COVER_OUT      := $(COVER_DIR)/coverage.out
COVER_HTML     := $(COVER_DIR)/coverage.html
PKG            := ./...

.DEFAULT_GOAL := help

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile the binary into ./bin/gau
	@mkdir -p bin
	$(GO) build -o bin/gau ./cmd/gau

.PHONY: install
install: ## Install gau into $$GOBIN
	$(GO) install ./cmd/gau

.PHONY: fmt
fmt: ## Run gofmt
	$(GO) fmt $(PKG)

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(PKG)

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run --config .golangci-lint.yml

.PHONY: test
test: cover-dir $(TPARSE) ## Run all tests with race + atomic coverage, rendered with tparse
	@$(GO) test -covermode=atomic -coverprofile=$(COVER_OUT) -race -json $(PKG) | $(TPARSE) -all

.PHONY: test-short
test-short: cover-dir $(TPARSE) ## Run tests with -short
	@$(GO) test -short -covermode=atomic -coverprofile=$(COVER_OUT) -race -json $(PKG) | $(TPARSE) -all

.PHONY: coverage
coverage: test ## Render an HTML coverage report
	$(GO) tool cover -func=$(COVER_OUT) | tail -n 1
	$(GO) tool cover -html=$(COVER_OUT) -o $(COVER_HTML)
	@echo "open $(COVER_HTML)"

.PHONY: cover-dir
cover-dir:
	@mkdir -p $(COVER_DIR)

.PHONY: bench
bench: ## Run benchmarks
	$(GO) test -run=^$$ -bench=. -benchmem $(PKG)

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

.PHONY: ci
ci: tidy vet lint test ## Full CI pipeline (what GH Actions should call)

.PHONY: smoke
smoke: build ## Hit real wayback for example.com — manual sanity check
	@printf example.com | ./bin/gau --providers wayback --from 202401 --to 202401 --threads 1 | head -5

.PHONY: clean
clean: ## Remove build artifacts and coverage data
	rm -rf bin dist $(COVER_DIR)

# --- Tool installation guards ---

$(GOLANGCI_LINT):
	@echo ">> installing golangci-lint"
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

$(TPARSE):
	@echo ">> installing tparse"
	$(GO) install github.com/mfridman/tparse@latest
