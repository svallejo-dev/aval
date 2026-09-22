#!/usr/bin/env bash
# regen.sh captures `go test -json` output from the fixture module in
# fixturemod/ and writes one normalized *.jsonl fixture per scenario next to
# this script. It is idempotent: a second run leaves the files unchanged.
# See README.md for what each fixture shows.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mod="$here/fixturemod"

# Keep the capture independent of the caller's environment. GOFLAGS must be
# non-empty: an empty value does not override `go env -w GOFLAGS=...`.
export GOWORK=off GOFLAGS=-mod=readonly
# rapid: never write fail files into the repo, and start from a fixed seed so
# the failing state machine shrinks to the same counterexample every run.
export RAPID_NOFAILFILE=1 RAPID_SEED=1

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

(cd "$here" && go build -o "$tmp/normalize" normalize.go)
goroot="$(go env GOROOT)"
gomodcache="$(go env GOMODCACHE)"

echo "regen: $(go version)"

# capture NAME WANT ARGS... runs `go test -json -count=1 ARGS...` inside the
# fixture module, checks that it exits with status WANT (failing scenarios
# fail on purpose) and writes the normalized stream to NAME.jsonl. The
# fixture is only replaced once the whole stream has been normalized.
capture() {
	local name=$1 want=$2
	shift 2

	local got=0
	(cd "$mod" && go test -json -count=1 "$@") >"$tmp/$name.raw" || got=$?
	if [[ $got -ne $want ]]; then
		echo "regen: $name: go test exited $got, want $want" >&2
		exit 1
	fi

	"$tmp/normalize" -fixturemod "$mod" -goroot "$goroot" -gomodcache "$gomodcache" \
		<"$tmp/$name.raw" >"$tmp/$name.jsonl"
	mv "$tmp/$name.jsonl" "$here/$name.jsonl"
	echo "regen: wrote $name.jsonl"
}

capture pass 0 ./pass
capture fail 1 ./fail
capture rapid-pass 0 ./rapidpass
capture rapid-fail 1 ./rapidfail
capture leak 1 ./leak
capture buildfail 1 ./buildfail
capture timeout 1 -timeout 1s ./timeout
capture panic 1 ./panic
capture notests 0 ./notests
# One CPU and one parallel slot make the pause/cont interleaving repeatable;
# with more, the order of the subtests' reports varies from run to run.
capture parallel 0 -cpu 1 -parallel 1 ./parallel
capture run-selection 0 -run '^TestOrder$/^ORD-F01(_|$)' ./pass
capture run-selection-bare 0 -run '^TestOrder$/^ORD-N01(_|$)' ./pass
capture run-selection-miss 0 -run '^TestOrder$/^ORD-F99(_|$)' ./pass
