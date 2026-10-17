# Starpoint verification.
#
# `make check` is the gate: formatting, vet, and the whole test suite. It must
# pass before anything is committed.

GO ?= go
BIN ?= bin/starpoint
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/hxmbl/starpoint/internal/cli.Version=$(VERSION)

.PHONY: all check build test test-race test-root cover fmt fmt-check vet tidy clean run

all: check

## check: run every verification step
check: fmt-check vet test

## build: compile the CLI
build:
	@mkdir -p $(dir $(BIN))
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/starpoint

## test: run the full test suite
test:
	$(GO) test ./...

## test-race: run the suite with the race detector
test-race:
	$(GO) test -race ./...

## test-root: run the tests that need root (ownership and permission fixes)
test-root:
	$(GO) test -c -o /tmp/starpoint-align.test ./internal/align
	$(GO) test -c -o /tmp/starpoint-cli.test ./internal/cli
	cd internal/align && sudo STARPOINT_ROOT_TESTS=1 /tmp/starpoint-align.test -test.run AsRoot -test.v
	cd internal/cli && sudo STARPOINT_ROOT_TESTS=1 /tmp/starpoint-cli.test -test.run AsRoot -test.v

## cover: report test coverage per package
cover:
	$(GO) test -cover ./...

## vet: run the standard static checks
vet:
	$(GO) vet ./...

## fmt: format the tree
fmt:
	$(GO) fmt ./...

## fmt-check: fail if anything is unformatted
fmt-check:
	@unformatted=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

## tidy: prune and verify module dependencies
tidy:
	$(GO) mod tidy
	$(GO) mod verify

## run: build and run against a scratch HOME
run: build
	./$(BIN)

clean:
	rm -rf bin