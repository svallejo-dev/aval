# ✗ aval: block · tier 2 · enforce

**repo** `svallejo-dev/aval-sandbox` · **trust base** `111111111111` · **change base** `444444444444` (main) → **head** `222222222222` · **aval** `v0.1.0` · **generated** `2026-09-24T12:00:00Z`

**changes** `add-refunds`

## Reasons (7)

### Blocking (5)

- `fail_before_missing` — no valid evidence that the test failed before the change
  - `ORD-N01`: no valid fail-before (before=pass, after=pass)
- `mixed_commit` — one commit touches dx and feat paths at once
  - commit 2222222222222222222222222222222222222222 touches dx and feat paths
- `open_question` — an added or modified open question: a human has to resolve it

> Cut here: the report reached the size limit of a step summary. The whole evidence bundle is in the run's artifacts.

## Reproduce

```sh
trust=1111111111111111111111111111111111111111
change=4444444444444444444444444444444444444444
head=2222222222222222222222222222222222222222
git fetch origin && git checkout "$head"
aval verify --trust-base "$trust" --change-base "$change" --head "$head"
aval gate --trust-base "$trust" --change-base "$change" --head "$head"
```
