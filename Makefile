.PHONY: build test lint verify latency

# Tool versions come from .tool-versions (asdf).

build:
	go build -trimpath -o bin/aval ./cmd/aval

test:
	go test -race ./...

lint:
	golangci-lint run ./...

# latency holds the agent hooks to ADR-0003's 50 ms p95. It runs alone, serially
# and without -race: beside the other packages' tests the number measures the
# machine, not aval, and no budget survives that. CI runs it as its own step.
latency:
	AVAL_LATENCY=1 go test -count=1 -run TestLatency -v ./internal/hook

# verify is what agents and CI run before declaring work done.
verify: build test lint
