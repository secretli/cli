.PHONY: build test lint vuln

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/secretli/cli/internal/cli.Version=$(VERSION)

# The binary, at bin/secretli
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/secretli ./cmd/secretli

test:
	go test -race ./...

lint:
	golangci-lint run ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
