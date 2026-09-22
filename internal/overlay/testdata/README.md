# Overlay fixture repository

`repo/base` and `repo/head` are the two commits of the repository the
integration tests build: each test copies `base`, commits it, replaces the
tree with `head` and commits again. The Go module lives in `svc/`, not at the
repository root, so the tests also cover a module in a subdirectory. Head is
green, except for `hang`, which never ends.

| Package | What head changes | Obligation | At the base |
|---|---|---|---|
| `calc` | fixes `Add`, adds `TestSpec/ORD-F01_…` | `ORD-F01` | `fail` → `strong` |
| `calc` | adds `TestSuite/TestAdd/ORD-F08_…` next to a sibling | `ORD-F08` | `fail` → `strong`; the sibling never runs |
| `money` | adds `Double` and `TestSpec/ORD-F02_…`, which calls it | `ORD-F02` | `build_fail` of its own tests → `weak` |
| `legacy` | adds tests for behavior the base already has | `ORD-F03`, `ORD-F04` | `pass` → `none`, or `characterization` when marked |
| `tax` | renames `old_test.go` to `tax_test.go`, changing `TestTax` and `Rate` | `ORD-F05` | `fail` → `strong`; keeping `old_test.go` would redeclare `TestTax` |
| `banner` | adds a test that reads `testdata/want.txt`, added too | `ORD-F06` | `pass` only if the testdata travels |
| `hang` | adds a test that writes `$AVAL_OVERLAY_STARTED` and sleeps | `ORD-F07` | never ends; the tests cancel it |
| `reader` | adds a test that reads `fixtures/in.txt`, outside `testdata/` | `ORD-F10` | `fail` → `weak`, with a note naming the file |
| `gitty` | adds a test that runs `git init` and `git add` in a temporary directory | `ORD-F11` | `pass`; must not touch the caller's index |
| `casing` | renames `Casing_test.go` to `casing_test.go`, changing `Loud` | `ORD-F12` | `fail` → `strong`, also on a case-insensitive file system |
| `deps` | adds a test that imports `example.com/dep`, a module `go.mod` requires only at head | `ORD-F13` | `build_fail` → `none`, with a note |
| `uses` | adds a test that imports `helper`, a package head adds | `ORD-F14` | `build_fail` → `weak`, with a note |
| `shared` | fixes `Raise`, and adds `ORD-F19`, which leaks state at the base, before `ORD-F18` | `ORD-F18`, `ORD-F19` | `ORD-F18` `pass` → `none` in its own run; `ORD-F19` `fail` → `strong` |

`ORD-F09` names a test that exists nowhere, so it does not run.
