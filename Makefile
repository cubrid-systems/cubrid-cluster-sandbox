# csb — build, check, ship.
#
# Go and a POSIX shell are the whole toolchain. There is nothing to install
# beyond the compiler, which is the point: the audience includes contributors
# with nothing you can assume (docs/design/ADR-001-implementation-language.md).

GO      ?= go
PKG     := github.com/cubrid-systems/cubrid-cluster-sandbox
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X '$(PKG)/internal/cli.Version=$(VERSION)'
BIN     := bin/csb

.PHONY: all build test vet fmt check dist clean

all: check build

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/csb

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# fmt reports rather than rewrites, so a dirty tree is caught in CI and not in a
# reviewer's diff. Use `gofmt -w .` to fix.
fmt:
	@out="$$(gofmt -l . )"; \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

check: fmt vet test

# A static binary, which is the distribution story the language was chosen for.
dist:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/csb
	@file $(BIN) 2>/dev/null || true

clean:
	rm -rf bin
