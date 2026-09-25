# ! aval: warn · tier 3 · enforce

**repo** `svallejo-dev/aval-sandbox` · **base** `111111111111` → **head** `222222222222` · **aval** `v0.1.0` · **generated** `2026-09-24T12:00:00Z`

**changes** `add-refunds`

## Reasons (7)

A valid override lowered the verdict to warn; the blocking reasons below still stand (ADR-0005 §5).

### Blocking (5)

- `fail_before_missing` — no valid evidence that the test failed before the change
  - `ORD-N01`: no valid fail-before (before=pass, after=pass)
- `mixed_commit` — one commit touches dx and feat paths at once
  - commit 2222222222222222222222222222222222222222 touches dx and feat paths
- `open_question` — an added or modified open question: a human has to resolve it
  - `ORD-O01`: added open question: a human must resolve it
- `tamper` — a tampering signal: a bound test or a protected file changed
  - policy_edited: .golangci.yml
  - `ORD-N01`: skip_added: t.Skip added to TestRefunds/ORD-N01_rejects_excess

### Warnings (2)

- `seam_touched` — a feat commit touches seam paths
  - feat commit 5555555555555555555555555555555555555555 touches seam paths
- `weak_evidence` — weak fail-before evidence: the failure at the base may not prove the change
  - `ORD-F02`: weak fail-before evidence (before=build_fail): refund.New did not exist at the base

## Obligations (5)

| ID | kind | delta | bound tests | before → after | strength | note |
| --- | --- | --- | --- | --- | --- | --- |
| `ORD-F01` | F must | added | `TestRefunds/ORD-F01_second_refund_is_a_no-op` | fail → pass | strong | — |
| `ORD-F02` | F must | added | `TestRefunds/ORD-F02_unknown_charge`, `TestRefunds/ORD-F02_empty_charge` | build_fail → pass | weak | refund.New did not exist at the base |
| `ORD-I01` | I invariant | modified | `TestRefunds/ORD-I01_totals` | pass → pass | characterization | — |
| `ORD-N01` | N must-not | added | `TestRefunds/ORD-N01_rejects_excess` | pass → pass | none | — |
| `ORD-O01` | O open question | added | — | n/a → not_run | none | no bound test: an open question is resolved by a human |

## Tamper (2)

| signal | ID | detail |
| --- | --- | --- |
| skip_added | `ORD-N01` | t.Skip added to TestRefunds/ORD-N01_rejects_excess |
| policy_edited | — | \.golangci.yml |

## Scope

3 commits in the range: 1 mixed, 1 seam. Only those are listed.

| commit | family | paths |
| --- | --- | --- |
| `444444444444` | seam | `internal/platform/git/git.go` |
| `222222222222` | mixed | `.golangci.yml`, `internal/refund/refund.go`, `internal/refund/refund_test.go`, `Makefile` (+1 more) |

## Approvals (3)

| kind | actor | commit | valid | reason | rejection |
| --- | --- | --- | --- | --- | --- |
| override | `@dev` | `333333333333` (not head) | no | flaky regression | review of an earlier commit |
| approval | `@lead` | `222222222222` | yes | — | — |
| override | `@lead` | `222222222222` | yes | payment outage: the refund fix ships now and the evidence lands tomorrow | — |

## Checks (3)

| check | status | exit | duration | command | artifact |
| --- | --- | --- | --- | --- | --- |
| `go-test` | pass | 0 | 4.2s | `go test -json ./...` | — |
| `fail-before` | fail | 1 | 1m11s | `go test -json -run ^TestRefunds$/^ORD-F01 ./internal/refund` | — |
| `golangci-lint` | fail | 1 | 9.1s | `golangci-lint run --new-from-merge-base=main` | `lint.json` |

## Not collected (3)

`mutation`, `rollback`, `slo`

## Reproduce

```sh
base=1111111111111111111111111111111111111111
head=2222222222222222222222222222222222222222
git fetch origin && git checkout "$head"
aval verify --base "$base" --head "$head"
aval gate --base "$base" --head "$head"
```
