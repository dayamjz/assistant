BINARY := assistant
PKG    := github.com/dayamjz/assistant
BIN    := bin

GO ?= go

# The golangci-lint major series this module's .golangci.yml is written for.
# The config declares `version: "2"`, and a v1 binary rejects it before it lints
# anything. The exact pinned patch release lives in .github/workflows/ci.yml.
GOLANGCI_MAJOR := 2

.PHONY: all build test race lint lint-guard-test fmt vet check clean tidy

all: check build

build:
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/$(BINARY) ./cmd/$(BINARY)

test:
	$(GO) test -race ./...

# Coverage over the whole module, written where CI can pick it up.
cover:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

# golangci-lint is required, not best-effort. Degrading to `go vet` alone when
# it is missing, or accepting a binary from the wrong major series that cannot
# read .golangci.yml, would let `make check` report green with no lint having
# run. A gate that cannot run completely refuses and explains; so does this one.
# The version string is parsed defensively: an unrecognized one is a refusal,
# not an assumption that the binary is fine.
lint: vet
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "make lint: golangci-lint is required and is not on PATH." >&2; \
		echo "  Install the v$(GOLANGCI_MAJOR) series (see .github/workflows/ci.yml for the pinned release)." >&2; \
		echo "  https://golangci-lint.run/docs/welcome/install/" >&2; \
		exit 1; \
	fi; \
	if ! reported=$$(golangci-lint version 2>&1); then \
		echo "make lint: 'golangci-lint version' failed, so its version cannot be checked." >&2; \
		echo "  It reported: $$reported" >&2; \
		exit 1; \
	fi; \
	major=$$(printf '%s\n' "$$reported" | tr -cs '0-9A-Za-z.' '\n' \
		| sed -n 's/^v\{0,1\}\([0-9][0-9]*\)\.[0-9].*$$/\1/p' | head -n 1); \
	if [ -z "$$major" ]; then \
		echo "make lint: could not parse a version from golangci-lint; refusing to lint." >&2; \
		echo "  'golangci-lint version' reported: $$reported" >&2; \
		echo "  The v$(GOLANGCI_MAJOR) series is required (see .github/workflows/ci.yml for the pinned release)." >&2; \
		exit 1; \
	fi; \
	if [ "$$major" != "$(GOLANGCI_MAJOR)" ]; then \
		echo "make lint: golangci-lint v$(GOLANGCI_MAJOR) is required, but v$$major is installed." >&2; \
		echo "  It reported: $$reported" >&2; \
		echo "  .golangci.yml is a schema version 2 config; another major series rejects it before linting." >&2; \
		echo "  See .github/workflows/ci.yml for the pinned release." >&2; \
		exit 1; \
	fi; \
	golangci-lint run ./...

# Drives `make lint` under a PATH with no golangci-lint, and under stub linters
# reporting the wrong major series, to prove the refusals above actually fire.
lint-guard-test:
	@MAKE="$(MAKE)" sh scripts/lint-guard-test.sh

tidy:
	$(GO) mod tidy

# What CI runs, and what the validation gate runs.
check: lint test

clean:
	rm -rf $(BIN) coverage.out
