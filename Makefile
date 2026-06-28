# ==============================================================================
# rcast - RCast DMR
# ==============================================================================

PROJECT_NAME := rcast
BINARY_NAME := $(PROJECT_NAME)
MAIN_PACKAGE := .
MODULE_PATH := github.com/tr1v3r/rcast

GO ?= go
GOWORK ?= off
GO_FLAGS ?=
GO_TEST_FLAGS ?=
RUN_ARGS ?=

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GO_VERSION ?= $(shell $(GO) env GOVERSION)

OUTPUT_DIR ?= output
BIN_DIR := $(OUTPUT_DIR)/bin
LOG_DIR := $(OUTPUT_DIR)/log
RELEASE_BIN := $(BIN_DIR)/$(BINARY_NAME)
DEV_BIN := $(BIN_DIR)/$(BINARY_NAME)-dev

GO_LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.buildTime=$(BUILD_TIME) \
	-X main.gitCommit=$(GIT_COMMIT) \
	-X main.goVersion=$(GO_VERSION)

FMT_BIN ?= goimports-reviser
FMT_VERSION ?= v3.12.6
FMT_SOURCE := github.com/incu6us/goimports-reviser/v3@$(FMT_VERSION)
LINT_BIN ?= golangci-lint
LINT_VERSION ?= v2.11.4
LINT_SOURCE := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)

.DEFAULT_GOAL := help

.PHONY: all build build-dev build-all run run-dev \
	test race-test test-coverage vet lint fmt format fmt-check format-check \
	tidy tidy-check check dev ci tools fmt-tool-check lint-tool-check \
	deps prepare clean clean-all version help FORCE

all: build

# ==============================================================================
# Build
# ==============================================================================

build: $(RELEASE_BIN) ## Build the stripped, versioned release binary
	@echo "Build completed: $(RELEASE_BIN)"

build-dev: $(DEV_BIN) ## Build the development binary with debug information
	@echo "Development build completed: $(DEV_BIN)"

build-all: build build-dev ## Build release and development binaries

$(RELEASE_BIN): FORCE | $(BIN_DIR) $(LOG_DIR)
	@echo "Building $(BINARY_NAME) $(VERSION)..."
	@GOWORK=$(GOWORK) $(GO) build $(GO_FLAGS) -ldflags "$(GO_LDFLAGS)" -o $@ $(MAIN_PACKAGE)

$(DEV_BIN): FORCE | $(BIN_DIR) $(LOG_DIR)
	@echo "Building development version of $(BINARY_NAME)..."
	@GOWORK=$(GOWORK) $(GO) build $(GO_FLAGS) -o $@ $(MAIN_PACKAGE)

$(BIN_DIR) $(LOG_DIR):
	@mkdir -p $@

FORCE:

# ==============================================================================
# Run
# ==============================================================================

run: build ## Build and run the release binary
	@$(RELEASE_BIN) $(RUN_ARGS)

run-dev: build-dev ## Build and run the development binary with debug logging
	@$(DEV_BIN) --debug $(RUN_ARGS)

# ==============================================================================
# Tests and quality
# ==============================================================================

test: ## Run unit tests
	@GOWORK=$(GOWORK) $(GO) test $(GO_FLAGS) $(GO_TEST_FLAGS) ./...

race-test: ## Run tests with the race detector
	@GOWORK=$(GOWORK) $(GO) test $(GO_FLAGS) $(GO_TEST_FLAGS) -race ./...

test-coverage: ## Generate an HTML coverage report
	@GOWORK=$(GOWORK) $(GO) test $(GO_FLAGS) $(GO_TEST_FLAGS) -coverprofile=coverage.out ./...
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

vet: ## Run go vet
	@GOWORK=$(GOWORK) $(GO) vet $(GO_FLAGS) ./...

lint: | lint-tool-check ## Run golangci-lint
	@$(LINT_BIN) run

fmt: | fmt-tool-check ## Format all Go files
	@$(FMT_BIN) -project-name $(MODULE_PATH) -format -recursive ./...

format: fmt ## Alias for fmt

fmt-check: | fmt-tool-check ## Check formatting without modifying files
	@$(FMT_BIN) -project-name $(MODULE_PATH) -format -recursive -list-diff -set-exit-status ./...

format-check: fmt-check ## Alias for fmt-check

tidy: ## Update go.mod and go.sum
	@GOWORK=$(GOWORK) $(GO) mod tidy

tidy-check: ## Check module files without modifying them
	@GOWORK=$(GOWORK) $(GO) mod tidy -diff

check: tidy-check fmt-check vet lint race-test ## Run all non-mutating checks

# ==============================================================================
# Development tools
# ==============================================================================

tools: ## Install pinned development tools
	@$(GO) install $(FMT_SOURCE)
	@$(GO) install $(LINT_SOURCE)

fmt-tool-check:
	@command -v $(FMT_BIN) >/dev/null 2>&1 || { \
		echo "$(FMT_BIN) is not installed; run 'make tools'"; \
		exit 1; \
	}

lint-tool-check:
	@command -v $(LINT_BIN) >/dev/null 2>&1 || { \
		echo "$(LINT_BIN) is not installed; run 'make tools'"; \
		exit 1; \
	}

# ==============================================================================
# Workflows
# ==============================================================================

dev: ## Format, validate, test, and build for local development
	@$(MAKE) tidy
	@$(MAKE) fmt
	@$(MAKE) vet lint race-test build
	@echo "Development workflow completed successfully"

ci: tidy-check fmt-check vet lint race-test test-coverage build ## CI workflow; never modifies source files
	@echo "CI workflow completed successfully"

# ==============================================================================
# Utilities
# ==============================================================================

prepare: $(BIN_DIR) $(LOG_DIR) ## Create output directories

deps: ## Download Go module dependencies
	@GOWORK=$(GOWORK) $(GO) mod download

clean: ## Remove binaries and coverage artifacts
	@rm -rf $(BIN_DIR)
	@rm -f coverage.out coverage.html

clean-all: ## Remove the complete output directory and coverage artifacts
	@rm -rf $(OUTPUT_DIR)
	@rm -f coverage.out coverage.html

version: ## Show build metadata
	@echo "Project: $(PROJECT_NAME)"
	@echo "Version: $(VERSION)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Go Version: $(GO_VERSION)"

help: ## Show available targets
	@echo "$(PROJECT_NAME) - RCast DMR"
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
