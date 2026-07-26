BINARY := termp
CMD := ./cmd/termp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Where `make update` installs the binary. Override with e.g. `make update PREFIX=~/bin`.
PREFIX ?= /usr/local/bin

.PHONY: build test install update snapshot

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

test:
	go test ./...

install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

# update: rebuild from the current source and replace the installed binary in one step.
# Uses `install` (a fresh inode), which avoids the macOS code-signature cache that makes an
# in-place `cp` over a running signed binary get killed. Needs sudo when PREFIX is root-owned.
update: build
	@echo "==> Installing $(BINARY) $(VERSION) -> $(PREFIX)/$(BINARY)"
	sudo install -m 0755 $(BINARY) $(PREFIX)/$(BINARY)
	@echo "==> Now installed:"
	@"$(PREFIX)/$(BINARY)" version
	@echo "==> If the daemon is running, restart it to load the new build: termp stop (autostart relaunches it) or termp start"

snapshot:
	@if command -v goreleaser >/dev/null 2>&1; then \
		goreleaser build --snapshot --clean; \
	else \
		echo "goreleaser is not installed"; \
		exit 1; \
	fi
