# Overlay fixture repository

`repo/base` and `repo/head` are the two commits of the repository the
integration tests build: each test copies `base`, commits it, replaces the
tree with `head` and commits again. The Go module lives in `svc/`, not at the
repository root, so the tests also cover a module in a subdirectory.

| Package | What head changes | Obligation | At the base |
|---|---|---|---|
| `calc` | fixes `Add`, adds `TestSpec/ORD-F01_…` | `ORD-F01` | `fail` → `strong` |
| `calc` | adds `TestSuite/TestAdd/ORD-F08_…` next to a failing sibling | `ORD-F08` | `fail` → `strong`, the sibling never runs |
| `money` | adds `Double` and `TestSpec/ORD-F02_…`, which calls it | `ORD-F02` | `build_fail` → `weak` |
| `legacy` | adds tests for behavior the base already has | `ORD-F03`, `ORD-F04` | `pass` → `none`, or `characterization` when marked |
| `tax` | renames `old_test.go` to `tax_test.go`, changing `TestTax` and `Rate` | `ORD-F05` | `fail` → `strong`; keeping `old_test.go` would redeclare `TestTax` |
| `banner` | adds a test that reads `testdata/want.txt`, added too | `ORD-F06` | `pass` only if the testdata is copied |
| `hang` | adds a test that writes `$AVAL_OVERLAY_STARTED` and sleeps | `ORD-F07` | never ends; the tests cancel it |

`ORD-F09` names a test that exists nowhere, so it does not run.
