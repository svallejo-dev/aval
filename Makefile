.PHONY: build test lint verify

# Tool versions come from .tool-versions (asdf).

build:
	go build -trimpath -o bin/aval ./cmd/aval

test:
	go test -race ./...

lint:
	golangci-lint run ./...

# verify is what agents and CI run before declaring work done.
verify: build test lint
