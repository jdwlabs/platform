.PHONY: build test lint snapshot release install clean

VERSION := $(shell git describe --tags --always --dirty)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/platformctl ./cmd/platformctl

test:
	go test ./... -race -count=1

lint:
	golangci-lint run

# Requires goreleaser installed locally.
snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean

install: build
	install -m 0755 bin/platformctl /usr/local/bin/platformctl

clean:
	rm -rf bin/ dist/
