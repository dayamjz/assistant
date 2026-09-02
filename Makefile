BINARY := assistant
PKG    := github.com/dayamjz/assistant
BIN    := bin

GO ?= go

.PHONY: all build test race lint fmt vet check clean tidy

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

lint: vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; ran go vet only"; \
	fi

tidy:
	$(GO) mod tidy

# What CI runs, and what the validation gate runs.
check: lint test

clean:
	rm -rf $(BIN) coverage.out
