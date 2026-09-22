# `go test -json` fixtures

Real `go test -json` streams, captured from a small fixture module, that pin
down how an obligation ID in a subtest name (`ORD-F01`) shows up in the
test2json output that aval parses. They drive the M1 parser: every fact
below is visible in these files, except the few marked *(ad hoc)*, which were
checked in a throwaway run or in the library source.

Captured with **go1.27.1 darwin/arm64**, `pgregory.net/rapid v1.3.0` and
`go.uber.org/goleak v1.3.0`.

## Layout

| Path | What it is |
|---|---|
| `fixturemod/` | Self-contained module `example.com/fixturemod` (own `go.mod`). Test data, not product code. |
| `*.jsonl` | One normalized `go test -json` stream per scenario. |
| `regen.sh` | Regenerates every `*.jsonl`. Idempotent. |
| `normalize.go` | Filter that makes the streams stable (`//go:build ignore`, run by `regen.sh`). |

`fixturemod` is invisible to the root module: it has its own `go.mod` and
lives under `testdata/`, so `./...`, `go mod tidy` and `make verify` skip it.
`buildfail/` never compiles by design; vet the rest from inside `fixturemod/`
with `go vet $(go list ./... | grep -v buildfail)`.

## Regenerating

```sh
internal/gotest/testdata/regen.sh
```

For each scenario it runs `go test -json -count=1 <args>` inside
`fixturemod/`, checks the exit status (failing scenarios must fail), pipes
the stream through `normalize.go` into a temporary file and only then moves
it over the fixture, so a failed run never leaves a truncated fixture. It
sets:

- `GOWORK=off` and `GOFLAGS=-mod=readonly` so the caller's environment does
  not leak in. `GOFLAGS` must be non-empty: an empty value does not override
  `go env -w GOFLAGS=...` (checked with `-trimpath` set that way: the
  fixtures stay identical);
- `RAPID_NOFAILFILE=1` so rapid writes no fail files into the repo;
- `RAPID_SEED=1` so the failing state machine shrinks to the same
  counterexample every run.

A different Go version or rapid release may legitimately change the output;
the diff is then the finding.

### What normalization changes

Only these values; every other byte, field order and event shape is exactly
what `go test` wrote.

| Varies with | Field or text | Becomes |
|---|---|---|
| clock | `"Time"` | `"2006-01-02T15:04:05Z"` |
| clock | `"Elapsed"` | `0` |
| clock | `--- PASS: X (0.01s)` | `(0.00s)` |
| clock | `ok  \t<pkg>\t0.216s`, `FAIL\t<pkg>\t0.190s` | `0.000s` |
| clock | rapid `passed 100 tests (2.4ms)` | `(0s)` |
| clock | `-timeout` panic's running tests `\t\tTestQueue (1s)` | `(0s)` |
| machine | absolute paths to `fixturemod/`, GOROOT, GOMODCACHE | `$FIXTUREMOD`, `$GOROOT`, `$GOMODCACHE` |
| runtime | `goroutine 35 [`, `in goroutine 36`, goleak `Goroutine 37 in state` | `goroutine N [`, `in goroutine N`, `Goroutine N in state` |
| runtime, GOARCH | hex in stack traces: arguments `(0x51f60afd8008, {0x10035da1d?, ...})`, offsets `+0x24` | `0x0` (a trailing `?` is dropped too) |

Stack traces also carry GOROOT line numbers (`testing.go:2193`), which change
with the Go version.

Build events (`build-output`, `build-fail`) carry no `Time`, so they need no
change.

## Fixtures

| File | Command (inside `fixturemod/`) | Exit | Shows |
|---|---|---|---|
| `pass.jsonl` | `go test -json -count=1 ./pass` | 0 | ID subtests, `#01` duplicates, bare ID, `/` in a name, colon after ID, nesting both ways, `attr`, skip |
| `fail.jsonl` | `go test -json -count=1 ./fail` | 1 | failing ID subtest with a multi-line message; failing test with no ID |
| `rapid-pass.jsonl` | `go test -json -count=1 ./rapidpass` | 0 | rapid state machine inside an ID subtest, passing |
| `rapid-fail.jsonl` | `go test -json -count=1 ./rapidfail` | 1 | same machine with a bug: failure, reproduction hint, shrunk steps |
| `leak.jsonl` | `go test -json -count=1 ./leak` | 1 | `goleak.VerifyTestMain` failing after every test passed |
| `buildfail.jsonl` | `go test -json -count=1 ./buildfail` | 1 | test file that does not compile |
| `timeout.jsonl` | `go test -json -count=1 -timeout 1s ./timeout` | 1 | ID subtest blocked on `select {}` until the alarm panics the binary |
| `panic.jsonl` | `go test -json -count=1 ./panic` | 1 | ID subtest that panics; later ID subtests and tests never run |
| `notests.jsonl` | `go test -json -count=1 ./notests` | 0 | package without test files |
| `parallel.jsonl` | `go test -json -count=1 -cpu 1 -parallel 1 ./parallel` | 0 | two ID subtests calling `t.Parallel`: `pause` and `cont` |
| `run-selection.jsonl` | `go test -json -count=1 -run '^TestOrder$/^ORD-F01(_\|$)' ./pass` | 0 | precise selection by ID |
| `run-selection-bare.jsonl` | `... -run '^TestOrder$/^ORD-N01(_\|$)' ./pass` | 0 | the same pattern misses the `#01` duplicate of a bare ID |
| `run-selection-miss.jsonl` | `... -run '^TestOrder$/^ORD-F99(_\|$)' ./pass` | 0 | selecting an ID no test carries still passes |

## Observed facts

### Test names

`t.Run` names are rewritten (spaces to `_`) and joined with `/`. Exact
`Test` values in `pass.jsonl`, and what aval's segment regex
`^([A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4})(?:_|$|#[0-9]+$)` extracts from them:

| `t.Run` name(s) | `Test` field | ID |
|---|---|---|
| `ORD-F01 rejects duplicates` | `TestOrder/ORD-F01_rejects_duplicates` | `ORD-F01` |
| same name again | `TestOrder/ORD-F01_rejects_duplicates#01` | `ORD-F01` |
| `ORD-N01` | `TestOrder/ORD-N01` | `ORD-N01` |
| `ORD-N01` again | `TestOrder/ORD-N01#01` | `ORD-N01` |
| `ORD-F03 keeps in/out order` | `TestOrder/ORD-F03_keeps_in/out_order` (fake third level `out_order`) | `ORD-F03` |
| `ORD-F04: colon after the ID` | `TestOrder/ORD-F04:_colon_after_the_ID` | **none**: `:` is kept and the regex needs `_`, end or `#` |
| `ORD-F05 ...` > `empty order` | `TestOrderNested/ORD-F05_lists_skus_in_insertion_order/empty_order` | `ORD-F05` (inherited by scanning every segment) |
| `happy path` > `ORD-F06 ...` | `TestOrderNested/happy_path/ORD-F06_accepts_a_single_sku` | `ORD-F06` |
| `duplicate sku is rejected` + `t.Attr` | `TestOrderAttr/duplicate_sku_is_rejected` | none by name; see `attr` |
| `ORD-F07 survives a restart` (skips) | `TestOrderSkip/ORD-F07_survives_a_restart` | `ORD-F07` |

Descendants of an ID subtest carry its segment, so a parser that scans every
segment attributes them to the same ID. The ID subtest's own `pass`/`fail`
already aggregates them.

### Event shapes

A package's test2json events start with `{"Action":"start","Package":...}`
and end with a package-level `pass`, `fail` or `skip` (no `Test`). The stream
does not always open with `start`: cmd/go's build events for the package come
first (`buildfail.jsonl`). Per test:
`run` → `output` `=== RUN` (frame) → body output → `output` `--- PASS|FAIL|SKIP`
(frame) → `pass`|`fail`|`skip` with `Elapsed`. A test killed by a timeout or
a panic breaks this pattern; see below.

`OutputType` values seen:

| `OutputType` | Used for |
|---|---|
| `frame` | `=== RUN`, `=== ATTR`, `=== PAUSE`, `=== CONT`, `--- PASS/FAIL/SKIP`, `PASS`, `FAIL`, and the `FAIL\t<pkg>\t...` summary |
| `error` | first line of `t.Error`/`t.Fatal` output |
| `error-continue` | following lines of the same multi-line error |
| absent | `t.Log`/`t.Skip` output, panic and timeout dumps, package-level output, and the `ok  \t<pkg>\t...` and `?   \t<pkg>\t[no test files]` summaries |

A failing child makes every ancestor `fail` (`TestOrder` in `fail.jsonl`);
a skipped child leaves its parent `pass` (`TestOrderSkip`).

### `attr`

`t.Attr(key, value)` emits an `attr` event with `Test`, `Key` and `Value`,
immediately **before** its `=== ATTR` frame line:

```json
{"Time":"...","Action":"attr","Package":"example.com/fixturemod/pass","Test":"TestOrderAttr/duplicate_sku_is_rejected","Key":"aval.req","Value":"ORD-F01"}
{"Time":"...","Action":"output","Package":"example.com/fixturemod/pass","Test":"TestOrderAttr/duplicate_sku_is_rejected","Output":"=== ATTR  TestOrderAttr/duplicate_sku_is_rejected aval.req ORD-F01\n","OutputType":"frame"}
```

- Setting the same key twice emits two events; nothing is deduplicated.
- *(ad hoc, from `testing.go`: `Attr` returns early when not chatty)* Only
  emitted with `-v` or `-json`.
- *(ad hoc)* test2json splits the frame line at the first space after the
  key, so a value keeps its spaces. `testing` rejects keys with whitespace and values
  with newlines through `t.Errorf`.

### Build failures (`buildfail.jsonl`)

cmd/go emits build events before the package's `start`. They have
`ImportPath` (with the test variant suffix) and no `Time` or `Package`:

```json
{"ImportPath":"example.com/fixturemod/buildfail [example.com/fixturemod/buildfail.test]","Action":"build-output","Output":"# example.com/fixturemod/buildfail [example.com/fixturemod/buildfail.test]\n"}
{"ImportPath":"example.com/fixturemod/buildfail [example.com/fixturemod/buildfail.test]","Action":"build-output","Output":"buildfail/order_test.go:8:13: undefined: Subtotal\n"}
{"ImportPath":"example.com/fixturemod/buildfail [example.com/fixturemod/buildfail.test]","Action":"build-fail"}
{"Time":"...","Action":"start","Package":"example.com/fixturemod/buildfail"}
{"Time":"...","Action":"output","Package":"example.com/fixturemod/buildfail","Output":"FAIL\texample.com/fixturemod/buildfail [build failed]\n","OutputType":"frame"}
{"Time":"...","Action":"fail","Package":"example.com/fixturemod/buildfail","Elapsed":0,"FailedBuild":"example.com/fixturemod/buildfail [example.com/fixturemod/buildfail.test]"}
```

`FailedBuild` equals the build events' `ImportPath`. Compiler paths are
relative to the directory `go test` ran in. There are no `run` events, so the
obligations of a package that does not build (`ORD-F10` here) never appear in
the stream.

### goleak (`leak.jsonl`)

Every test `pass`es. After the `PASS` frame, goleak writes package-level
output with no `Test` and no `OutputType`, then the package `fail`s without
`FailedBuild`:

```
goleak: Errors on successful test run: found unexpected goroutines:
[Goroutine N in state chan receive, with example.com/fixturemod/leak.Start.func1 on top of the stack:
example.com/fixturemod/leak.Start.func1()
	$FIXTUREMOD/leak/worker.go:9 +0x0
created by example.com/fixturemod/leak.Start in goroutine N
	$FIXTUREMOD/leak/worker.go:9 +0x0
]
FAIL	example.com/fixturemod/leak	0.000s
```

So a `PASS` line followed by a package `fail` is the signature. The leak is
not tied to `ORD-O01`, the subtest that caused it. *(ad hoc)*
`VerifyTestMain` only checks when every test passed, so a package with a
failing test never reports its leaks.

### rapid

Passing (`rapid-pass.jsonl`), a plain log line:

```
    counter_test.go:28: [rapid] OK, passed 100 tests (0s)
```

Failing (`rapid-fail.jsonl`), on the ID subtest:

```
    counter_test.go:28: [rapid] failed after 1 tests: Value() = 4, want within [0, 3]     <- error
        To reproduce, specify -run="TestCounter/ORD-I01_counter_stays_within_bounds" -rapid.seed=2   <- error-continue
        Failed test output:                                                             <- error-continue
    counter_test.go:29: [rapid] draw action: "Inc"                                      <- no OutputType (x4)
    counter_test.go:22: Value() = 4, want within [0, 3]                                 <- no OutputType
```

- The shrunk replay, including the invariant's own `t.Fatalf` message, is
  plain output, not `error`.
- The `-run` hint is `regexp.QuoteMeta(t.Name())`, unanchored.
- The printed seed is the failing iteration's (base `RAPID_SEED=1`, one
  passing test, `-rapid.seed=2`).
- *(ad hoc)* Without `RAPID_NOFAILFILE` (or `-rapid.nofailfile`), rapid writes
  `testdata/rapid/<name>/<name>-<yyyymmddhhmmss>-<pid>.fail` under the
  package directory, where `<name>` is the test name with every character
  other than letters, digits, `-` and `_` turned into `_`
  (`TestCounter_ORD-I01_counter_stays_within_bounds`). The hint then reads
  `-rapid.failfile="testdata/rapid/..." (or -rapid.seed=N)`. On later runs
  rapid replays existing fail files first (`failed after 0 tests`), even with
  `RAPID_NOFAILFILE` set.
- Not captured: `[rapid] panic after N tests` (with a traceback) and
  `[rapid] flaky test, can not reproduce a failure`.

### Timeout (`timeout.jsonl`)

`TestQueue` and `TestQueue/ORD-F08_drains_the_queue` get a `run` event and
**nothing else**: no `--- FAIL` frame and no `pass`, `fail` or `skip`. When
the `-timeout` alarm fires, the panic goes to the test that started last,
the subtest, with no `OutputType`:

```
panic: test timed out after 1s
	running tests:
		TestQueue (0s)
		TestQueue/ORD-F08_drains_the_queue (0s)

goroutine N [running]:
testing.(*M).startAlarm.func1()
...
goroutine N [select (no cases)]:
example.com/fixturemod/timeout.Drain(...)
...
```

The dump lists every goroutine. Then only the package fails: a
`FAIL\t<pkg>\t...` frame and a package `fail` without `FailedBuild`. A test
with a `run` event but no final event when its package ends was killed. The
`running tests:` lines name the tests that were killed.

### Panic (`panic.jsonl`)

`ORD-F20` passes. `ORD-F21` panics: its `--- FAIL` frame and `fail` event
come first, with no error output. The parent's `--- FAIL: TestRefund` frame
follows, and then the panic, tagged to the **parent** `TestRefund` with no
`OutputType`:

```
panic: refund: negative amount -1 [recovered, repanicked]

goroutine N [running]:
testing.tRunner.func1.2({0x0, 0x0})
...
example.com/fixturemod/panic.TestRefund.func2(0x0)
	$FIXTUREMOD/panic/refund_test.go:13 +0x0
```

Only the panicking goroutine is dumped. After the dump come `fail` for
`TestRefund`, the `FAIL` frame and the package `fail`. `ORD-F22` and
`TestRefundLater/ORD-F23` get **no events at all**, so in the stream they look
the same as tests that do not exist. Only the stack frame (`TestRefund.func2`)
ties the panic message to a subtest, and not by name.

### No test files (`notests.jsonl`)

`start`, then one package-level output line `?   \t<pkg>\t[no test files]`
(no `OutputType`), then a package `skip` with `Elapsed`. There are no `run`
events.

### Parallel subtests (`parallel.jsonl`)

`t.Parallel` pauses the subtest until its parent returns, and then resumes it.
Unlike every other action, the `=== PAUSE` frame comes **before** the `pause`
event. On resume, the `cont` event comes first, then the `=== CONT` frame.

The fixture is captured with `-cpu 1 -parallel 1`, because otherwise the
order changes from run to run. With default flags, 10 of 40 runs (11 of 40
with `-parallel 1` alone) printed `ORD-F31`'s `cont` before `ORD-F30`'s
`--- PASS` and `pass`. With `-cpu 1 -parallel 1`, all 80 runs were identical. In
real runs, events of parallel tests interleave, so key state by `Test`, never
by position.

### `-run` selection

- Tests and subtests the pattern does not select produce **no events**, not
  even `skip` (`run-selection.jsonl` has only `TestOrder` and the two
  `ORD-F01` cases).
- `(_|$)` does not select `ORD-N01#01`, the duplicate of a bare ID
  (`run-selection-bare.jsonl`). *(ad hoc)* `^ORD-N01([_#]|$)` does.
- Anchoring the ID at the second level misses IDs deeper down
  (`TestOrderNested/happy_path/ORD-F06_...`) and tests bound only by `attr`.
- Selecting an ID no test carries still runs and passes the parent, prints
  `testing: warning: no tests to run` as package-level output, appends
  ` [no tests to run]` to the `ok` summary and exits 0
  (`run-selection-miss.jsonl`). A zero exit does not prove the obligation ran.

### Not captured

- Several packages in one run: events interleave and must be keyed by
  `Package` (and `ImportPath` for build events).
- `t.ArtifactDir` with `-artifacts`, which emits `artifacts` events with `Path`.
- Panics in goroutines other than the test's own, and `t.Parallel` under
  default flags (not repeatable, see above).
