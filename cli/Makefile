.PHONY: build test lint fmt snapshot release install clean

VERSION := $(shell git describe --tags --always --dirty)
COMMIT  := $(shell git rev-parse --short HEAD)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Destination directory for `make install`. Override for workstations that
# don't have (or don't want) a system-wide /usr/local/bin, e.g.
# `make install PREFIX=~/bin`.
PREFIX ?= /usr/local/bin

# On Windows, an extensionless build output can sit next to a pre-existing
# platformctl.exe: `which`/`ls` resolve the extensionless file, but PATH
# lookup still executes the .exe, so an "installed" rebuild silently keeps
# running the old binary. Naming the output .exe on Windows removes the
# ambiguity instead of relying on operators to notice.
EXE :=
ifeq ($(OS),Windows_NT)
EXE := .exe
endif

# -buildvcs=false: a linked git worktree makes Go's automatic VCS stamping
# fail ("error obtaining VCS status: exit status 128"); LDFLAGS above already
# stamps commit+date explicitly, so the Go toolchain's own stamping is redundant.
build:
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o bin/platformctl$(EXE) ./cmd/platformctl

test:
	go test ./... -race -count=1

# CI resolves the linter from this same file, so a local verdict that cannot
# match CI's is reported here rather than discovered as a surprise red gate.
GOLANGCI_VERSION := $(shell cat .golangci-lint-version)

lint:
	@installed=$$(golangci-lint version --short 2>/dev/null); \
	if [ "v$$installed" != "$(GOLANGCI_VERSION)" ]; then \
		echo "warning: golangci-lint v$$installed installed, CI pins $(GOLANGCI_VERSION)" >&2; \
	fi
	golangci-lint run

# gofmt moved under `formatters` in golangci-lint v2: `run` reports formatting
# problems but will not rewrite them, so fixing them needs this separate target.
fmt:
	golangci-lint fmt

# Requires goreleaser installed locally.
snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean

install: build
	install -m 0755 bin/platformctl$(EXE) $(PREFIX)/platformctl$(EXE)

clean:
	rm -rf bin/ dist/
