# Spike S1 fixtures: obligation IDs under OpenSpec 1.13.1

Captured on 2026-09-21 with `@fission-ai/openspec@1.13.1` (Node v26.7.0), run as
`npx -y @fission-ai/openspec@1.13.1 …` with `OPENSPEC_TELEMETRY=0 OPENSPEC_NO_UPDATE_CHECK=1`
in throwaway repos created with `openspec init --tools none --no-animation .`.
Rationale and conclusions: [ADR-0002](../../../../docs/adr/0002-id-de-obligacion.md).

These files are the input and the expected output for the M1 parser's
differential tests: aval parses the markdown itself and must agree with what
OpenSpec validates and archives. They are also a small semantic corpus: every ID
uses the kind letter its requirement really is.

## ID contract

Every requirement name satisfies the v1 contract of `internal/obligation`
(ADR-0004):

- ID: `^[A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4}$`
- requirement name, after header normalization: `^ID[ \t]+(\S(?:.*\S)?)[ \t]*$`
- test2json segment: `^([A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4})(?:_|$|#[0-9]+$)`

There are two deliberate exceptions, both expected to fail: `[ORD-F04] …` in
`negative/bracket-id`, and the loose header `###requirement:ORD-F06 …` in
`negative/loose-header`.

| ID | Kind | Requirement |
|---|---|---|
| ORD-F01 | F, must happen | Refund is idempotent |
| ORD-S01 | S, SLO | Fast refund answer → Refund latency under 300 ms (renamed) |
| ORD-I01 | I, invariant (characterization) | Ledger entry per refund |
| ORD-A01 | A, assumption (removed) | Gateway confirms refunds at once |
| ORD-N01 | N, must never happen (added) | Refund never exceeds order total |
| ORD-F03 | F | Refund metrics exported (tier-0 change `x`) |

No fixture uses kind O: an open question blocks the gate, so it cannot appear in
a tier-0 example.

## Normalization

- The absolute path of the temp repo is replaced with `<ROOT>` and the npx cache
  path with `<NPX_CACHE>`.
- `durationMs` in `validate` output is wall-clock time: tests must ignore it.
- **The run date is not normalized.** It appears in `.openspec.yaml`
  (`created: 2026-09-21`) and in every `archive.txt`
  (`archived as '2026-09-21-<change>'`). Tests must ignore or normalize
  `[0-9]{4}-[0-9]{2}-[0-9]{2}` there, and a re-capture on another day changes
  only those lines.
- Everything else is byte-for-byte OpenSpec output (stdout only; `schema *`
  commands also print `Note: Schema commands are experimental and may change.`
  on stderr).

## Layout

| Path | What it is |
|---|---|
| `refunds/spec-before.md` | Main spec `openspec/specs/refunds/spec.md` before archiving: ORD-F01, ORD-S01 (`Fast refund answer`), ORD-I01 (with `**aval**: characterization`), ORD-A01. |
| `refunds/spec-after.md` | The same file after `openspec archive add-refund-limits -y`: F01, S01, I01 in place, ORD-N01 appended, ORD-A01 gone. |
| `refunds/archive.txt` | Output of that archive (stdout and stderr). |
| `changes/add-refund-limits/` | The change as archived: `proposal.md`, `tasks.md`, `.openspec.yaml` and the delta `specs/refunds/spec.md` with ADDED ORD-N01, MODIFIED ORD-F01 (+1 scenario) and ORD-I01 (keeps the marker), REMOVED ORD-A01, RENAMED `ORD-S01 Fast refund answer` → `ORD-S01 Refund latency under 300 ms`. |
| `json/` | Captured `--json` outputs (table below). The `*-x*` and `schema-*` files belong to the rejected custom-schema option. |
| `negative/<case>/` | Cases aval must report (error or warn). Each is applied to `refunds/spec-after.md`: `delta.md` (the change's `specs/refunds/spec.md`), `validate.json` (`validate <case> --type change --strict --json`), `archive.txt` (`archive <case> -y`, stdout and stderr), and `spec-after.md` only when archive rewrote the spec. |
| `positive/<case>/` | Positive controls with the same layout: OpenSpec and aval both accept them. |
| `extra-files/` | The files aval manages itself inside `openspec/changes/<id>/` with the built-in `spec-driven` schema: `aval.yaml` (change manifest), `premortem.md`, `trace.yaml`. OpenSpec ignores them (table below). |
| `changes/x/` | **Reference only (rejected option).** Tier-0 change created with `openspec new change x --schema aval`, with proposal, specs, design and tasks only (no premortem, no trace). Its base spec is `refunds/spec-after.md`. |
| `schemas/aval/` | **Reference only (rejected option).** The custom schema: `openspec schema fork spec-driven aval` plus the `premortem` and `trace` artifacts and their templates, with `apply.requires` still `[ tasks ]`. v0 does not ship it (ADR-0002): OpenSpec has no optional artifacts, so `isPlanningComplete` stays `false` and `nextSteps` asks for premortem even at tier 0. It is kept as evidence for that decision. |

### `json/`

| File | Command |
|---|---|
| `validate-spec-before.json` | `validate --all --strict --json` on the initial spec |
| `show-spec-before.json` | `show refunds --type spec --json` (no requirement or scenario names; the `**aval**` line is stripped from `text`) |
| `new-change.json` | `new change add-refund-limits --json` |
| `validate-change.json` | `validate add-refund-limits --type change --strict --json` |
| `show-change-deltas.json` | `show add-refund-limits --type change --json --deltas-only` (only RENAMED carries names) |
| `status-change.json` | `status --change add-refund-limits --json` (built-in `spec-driven` schema) |
| `validate-spec-after.json` | `validate --all --strict --json` after archive |
| `show-spec-after.json` | `show refunds --type spec --json` after archive |
| `schema-fork.json` | `schema fork spec-driven aval --json` |
| `schema-validate.json` | `schema validate aval --json` after adding `premortem` and `trace` |
| `schema-which.json` | `schema which aval --json` |
| `new-change-x.json` | `new change x --schema aval --json` |
| `status-x-new.json` | `status --change x --json` right after creation |
| `status-x-planned.json` | `status --change x --json` with proposal, specs, design and tasks: `applyRequires` is `["tasks"]`, premortem and trace are `ready`, `isPlanningComplete` is `false` |
| `instructions-apply-x.json` | `instructions apply --change x --json`: `state` is `ready`, no `missingPrerequisites` |
| `validate-x.json` | `validate x --type change --strict --json` |

### `negative/` and `positive/`

| Case | validate (`--strict`) | archive | aval expects |
|---|---|---|---|
| `negative/modified-case-variant` | valid, `INFO` "Archive would refuse this delta" | refused, exit 1 | **error**: every INFO is a failure |
| `negative/non-requirement-h3` | valid, `INFO` "`### Notes` … is not a "### Requirement:" header and is ignored" | refused, exit 1 (the rebuilt requirement has no scenario) | **error**: every INFO is a failure; no `###` other than `### Requirement:` in requirement sections |
| `negative/modified-drops-scenario` | `ERROR`, exit 1 | refused, exit 1 | **error** (OpenSpec already catches it) |
| `negative/modified-old-name-after-rename` | `ERROR`, exit 1 | refused, exit 1 | **error** (OpenSpec already catches it) |
| `negative/renamed-id-change` | valid | applied: `ORD-S01` becomes `ORD-S09` | **error**: RENAMED must keep the ID |
| `negative/duplicate-id` | valid | applied: two `ORD-F01` requirements | **error**: the ID is already defined |
| `negative/bracket-id` | valid | applied: `[ORD-F04] …` | **error**: no valid ID at the start of the name |
| `negative/trailing-hash` | valid | applied, header written with ` ##` | **error**: non-canonical header |
| `negative/loose-header` | valid | applied, `###requirement:ORD-F06 …` written verbatim; `show` on the resulting spec then counts 4 requirements, not 5, and folds ORD-F06's scenario into ORD-N01 | **error**: non-canonical header |
| `negative/modified-drops-marker` | valid | applied, `**aval**: characterization` lost | **warn**: the characterization marker was dropped |
| `positive/renamed-then-modified` | valid | applied: S01 renamed and modified | **accept** (MODIFIED uses the new name) |
| `positive/fenced-requirement` | valid | applied; the `### Requirement: ORD-F99 …` inside a code fence stays body text (`show` counts 5 requirements) | **accept**; ORD-F99 is not an obligation |
| `positive/c-sharp-name` | valid | applied: `ORD-F07 Refund SDK for C#` keeps its `#` | **accept** |

### `extra-files/`

Same change as `changes/add-refund-limits/`, built-in `spec-driven` schema, plus
`aval.yaml`, `premortem.md` and `trace.yaml` in the change directory.

| File | Command and result |
|---|---|
| `validate.json` | `validate add-refund-limits --type change --strict --json`: exit 0, `valid: true`, no issues |
| `status.json` | `status --change add-refund-limits --json`: the three files appear in neither `artifacts` nor `artifactPaths` |
| `archive.txt` | `archive add-refund-limits -y`: exit 0, same totals; the resulting spec is byte-identical to `refunds/spec-after.md` and the three files move to `changes/archive/<date>-add-refund-limits/` |
| `validate-archived.json` | `validate --archived --json` after the archive: passes |
