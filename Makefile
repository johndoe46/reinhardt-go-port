.DEFAULT_GOAL := help
.DELETE_ON_ERROR:
SHELL := /bin/sh
.SHELLFLAGS := -eu -c

GO ?= go
GOFMT ?= gofmt
GOVULNCHECK_VERSION ?= v1.9.0
GOVULNCHECK_DIR = $(abspath $(BUILD_DIR))/tools/govulncheck-$(GOVULNCHECK_VERSION)
GOVULNCHECK ?= $(GOVULNCHECK_DIR)/govulncheck
AIR ?= air
BUILD_DIR ?= bin
BIN ?= $(BUILD_DIR)/reinhardt
DEBUG_BIN ?= $(BUILD_DIR)/reinhardt-debug
LINUX_ARM64_BIN ?= $(BUILD_DIR)/reinhardt-linux-arm64
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
ENV_FILE ?= .env
DATABASE ?=
LISTEN ?=
ARGS ?=
TEST ?= .
TEST_FLAGS ?=
COVERAGE ?= $(BUILD_DIR)/coverage.out
COVERAGE_HTML ?= $(BUILD_DIR)/coverage.html

# Production and normal verification use pure Go and vendored sources.
# Go's race detector requires CGO, so test-race is an explicit exception.
export CGO_ENABLED := 0
export GOTOOLCHAIN ?= go1.27.3
export GO
MODULE_FLAGS := -mod=vendor
APP_FLAGS = --env-file "$(ENV_FILE)" $(if $(DATABASE),--database "$(DATABASE)")
LISTEN_FLAGS = $(if $(LISTEN),--listen "$(LISTEN)")
DEV_BUILD_CMD = $(MAKE) build BIN="$(abspath $(BUILD_DIR))/dev/reinhardt"

.PHONY: help doctor deps env setup install build build-debug build-linux-arm64 dev run run-http \
	worker worker-once cli migrate check-app fmt fmt-check vet vulncheck check test \
	test-verbose test-coverage test-race ci verify vendor tidy clean

##@ Help and prerequisites
help: ## Show targets, variables, and examples (default)
	@awk 'BEGIN { print "Usage: make <target> [VARIABLE=value]" } /^##@ / { printf "\n%s\n", substr($$0, 5) } /^[a-zA-Z0-9_-]+:.*## / { split($$0, parts, "## "); split(parts[1], target, ":"); printf "  %-20s %s\n", target[1], parts[2] }' $(MAKEFILE_LIST)
	@printf '\nVariables:\n'
	@printf '  GO, GOFMT             Go tools (requires Go 1.27.3+)\n'
	@printf '  GOVULNCHECK           Optional path to an existing vulnerability scanner\n'
	@printf '  GOVULNCHECK_VERSION   Managed scanner version (default v1.9.0)\n'
	@printf '  AIR                   Live reload tool for make dev (default air)\n'
	@printf '  BUILD_DIR, BIN        Build directory and release executable\n'
	@printf '  LINUX_ARM64_BIN       Linux/aarch64 executable (default bin/reinhardt-linux-arm64)\n'
	@printf '  PREFIX, BINDIR        Install prefix (default ~/.local) and binary directory\n'
	@printf '  DESTDIR               Optional packaging/staging root\n'
	@printf '  ENV_FILE, DATABASE    Dotenv file and optional SQLite path override\n'
	@printf '  LISTEN, ARGS          Server listen address and extra CLI arguments\n'
	@printf '  TEST, TEST_FLAGS      Test name regexp and extra go test flags\n'
	@printf '\nExamples:\n'
	@printf '  make setup\n  make install\n  make run LISTEN=127.0.0.1:8000\n'
	@printf '  make dev LISTEN=127.0.0.1:8000\n'
	@printf '  make cli ARGS="giveaccess example-user --superuser"\n'
	@printf '  make cli ARGS="addmusicdir /path/to/music"\n'
	@printf '  make test TEST=TestStreamingAndDelegate TEST_FLAGS=-count=1\n'
	@printf '  make migrate DATABASE=/path/to/copy.sqlite3\n'
	@printf '  make build-linux-arm64\n'

doctor: ## Report Go and optional media tools
	@command -v "$(GO)" >/dev/null || { printf 'Missing Go: install Go 1.27.3 or newer.\n' >&2; exit 1; }
	@$(GO) version
	@for tool in ffmpeg yt-dlp; do \
		if command -v "$$tool" >/dev/null; then printf '%s: available\n' "$$tool"; \
		else printf '%s: absent (optional media features)\n' "$$tool"; fi; \
	done

##@ Setup and installation
deps: ## Verify all required Go packages are available from vendor, offline
	@$(GO) list $(MODULE_FLAGS) -buildvcs=false -deps ./... >/dev/null
	@printf 'Vendored dependencies OK\n'

env: ## Create ENV_FILE from .env.example, preserving existing configuration
	@if [ -e "$(ENV_FILE)" ]; then printf 'Keeping existing %s\n' "$(ENV_FILE)"; \
	else cp -n .env.example "$(ENV_FILE)"; printf 'Created %s; edit it for your installation.\n' "$(ENV_FILE)"; fi

setup: deps env build ## Verify dependencies, prepare configuration, and build

install: build ## Install the release executable under BINDIR (supports DESTDIR)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BIN)" "$(DESTDIR)$(BINDIR)/reinhardt"

##@ Builds
build: ## Build a stripped, CGO-disabled binary with embedded assets
	@mkdir -p "$(dir $(BIN))"
	$(GO) build $(MODULE_FLAGS) -buildvcs=false -trimpath -ldflags='-s -w' -o "$(BIN)" ./cmd/reinhardt

build-debug: ## Build a CGO-disabled executable with debugger symbols
	@mkdir -p "$(dir $(DEBUG_BIN))"
	$(GO) build $(MODULE_FLAGS) -buildvcs=false -gcflags='all=-N -l' -o "$(DEBUG_BIN)" ./cmd/reinhardt

build-linux-arm64: ## Cross-compile for Linux/aarch64 with CGO disabled and embedded assets
	$(MAKE) build GOOS=linux GOARCH=arm64 CGO_ENABLED=0 BIN="$(LINUX_ARM64_BIN)"

##@ Run and administration
dev: ## Run HTTP and background jobs, rebuilding on source/asset changes (requires Air)
	@command -v "$(AIR)" >/dev/null || { printf 'Missing Air: install with go install github.com/air-verse/air@latest and add Go bin to PATH.\n' >&2; exit 1; }
	@mkdir -p "$(BUILD_DIR)/dev"
	$(AIR) -c .air.toml -tmp_dir "$(BUILD_DIR)/dev" \
		-build.cmd '$(DEV_BUILD_CMD)' \
		-build.full_bin '"$(abspath $(BUILD_DIR))/dev/reinhardt" $(APP_FLAGS) serve $(LISTEN_FLAGS) $(ARGS)'

run: build ## Run HTTP and background jobs; accepts LISTEN and ARGS
	"$(abspath $(BIN))" $(APP_FLAGS) serve $(LISTEN_FLAGS) $(ARGS)

run-http: build ## Run HTTP only; use worker separately
	"$(abspath $(BIN))" $(APP_FLAGS) serve --no-worker $(LISTEN_FLAGS) $(ARGS)

worker: build ## Run durable jobs and periodic imports without HTTP
	"$(abspath $(BIN))" $(APP_FLAGS) worker $(ARGS)

worker-once: build ## Process currently ready jobs and exit
	"$(abspath $(BIN))" $(APP_FLAGS) worker --once $(ARGS)

cli: build ## Run any integrated management command using ARGS
	"$(abspath $(BIN))" $(APP_FLAGS) $(ARGS)

migrate: build ## Initialize SQLite and rebuild FTS for the configured database
	"$(abspath $(BIN))" $(APP_FLAGS) migrate

check-app: build ## Check the configured SQLite database and embedded templates
	"$(abspath $(BIN))" $(APP_FLAGS) check

##@ Formatting and static checks
fmt: ## Format application Go sources, leaving vendor untouched
	$(GOFMT) -w cmd internal

fmt-check: ## Fail if application Go sources need formatting
	@unformatted=$$($(GOFMT) -l cmd internal); \
	if [ -n "$$unformatted" ]; then printf 'Run make fmt for:\n%s\n' "$$unformatted" >&2; exit 1; fi

vet: ## Run go vet with vendored dependencies and CGO disabled
	$(GO) vet $(MODULE_FLAGS) ./...

$(GOVULNCHECK_DIR)/govulncheck:
	@mkdir -p "$(GOVULNCHECK_DIR)"
	GOFLAGS= GOBIN="$(GOVULNCHECK_DIR)" $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

ifeq ($(GOVULNCHECK),$(GOVULNCHECK_DIR)/govulncheck)
vulncheck: $(GOVULNCHECK_DIR)/govulncheck
endif

vulncheck: ## Install govulncheck if needed, then scan application code, tests, and vendored dependencies
	@command -v "$(GOVULNCHECK)" >/dev/null || { printf 'Cannot find GOVULNCHECK: %s\n' "$(GOVULNCHECK)" >&2; exit 1; }
	GOFLAGS="$(GOFLAGS) $(MODULE_FLAGS)" "$(GOVULNCHECK)" -test ./...

check: fmt-check vet ## Check formatting and run static analysis

##@ Tests
test: ## Run Go regression tests; optionally select a TEST regexp
	$(GO) test $(MODULE_FLAGS) $(TEST_FLAGS) -run "$(TEST)" ./...

test-verbose: ## Run regression tests with verbose output
	$(GO) test $(MODULE_FLAGS) $(TEST_FLAGS) -v -run "$(TEST)" ./...

test-coverage: ## Write coverage data and an HTML report under BUILD_DIR
	@mkdir -p "$(dir $(COVERAGE))" "$(dir $(COVERAGE_HTML))"
	$(GO) test $(MODULE_FLAGS) $(TEST_FLAGS) -run "$(TEST)" -covermode=atomic -coverprofile="$(COVERAGE)" ./...
	$(GO) tool cover -html="$(COVERAGE)" -o "$(COVERAGE_HTML)"
	@printf 'Coverage report: %s\n' "$(COVERAGE_HTML)"

test-race: ## Run the race detector (requires a C compiler; CGO enabled for tests)
	CGO_ENABLED=1 $(GO) test $(MODULE_FLAGS) $(TEST_FLAGS) -race -run "$(TEST)" ./...

ci: check test build ## Run normal CI checks, regressions, and a release build

verify: ci ## Run all CI checks

##@ Dependency maintenance and cleanup
vendor: ## Refresh vendored sources from go.mod (may download modules)
	$(GO) mod vendor

tidy: ## Reconcile go.mod/go.sum and refresh vendor (may download modules)
	$(GO) mod tidy
	$(GO) mod vendor

clean: ## Remove release/debug/Linux arm64 binaries and coverage reports
	rm -f "$(BIN)" "$(DEBUG_BIN)" "$(LINUX_ARM64_BIN)" "$(COVERAGE)" "$(COVERAGE_HTML)"

