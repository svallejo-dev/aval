# Spike S1 fixtures: obligation IDs under OpenSpec 1.13.1

Captured on 2026-09-21 with `@fission-ai/openspec@1.13.1` (Node v26.7.0), run as
`npx -y @fission-ai/openspec@1.13.1 …` with `OPENSPEC_TELEMETRY=0 OPENSPEC_NO_UPDATE_CHECK=1`
in a throwaway repo created with `openspec init --tools none --no-animation .`.
Rationale and conclusions: [ADR-0002](../../../../docs/adr/0002-id-de-obligacion.md).

These files are the input and the expected output for the M1 parser's
differential tests: aval parses the markdown itself and must agree with what
OpenSpec validates and archives.

## Normalization

- The absolute path of the temp repo is replaced with `<ROOT>` and the npx cache
  path with `<NPX_CACHE>`.
- `durationMs` in `validate` output is wall-clock time: tests must ignore it.
- Everything else is byte-for-byte OpenSpec output (stdout only; `schema *`
  commands also print `Note: Schema commands are experimental and may change.`
  on stderr).

## Layout

| Path | What it is |
|---|---|
| `refunds/spec-before.md` | Main spec `openspec/specs/refunds/spec.md` before archiving: ORD-F01, ORD-N01 (`Old title`), ORD-I01 (with `**aval**: characterization`), ORD-A01. |
| `refunds/spec-after.md` | The same file after `openspec archive add-refund-limits -y`. |
| `changes/add-refund-limits/` | The change as archived: `proposal.md`, `tasks.md`, `.openspec.yaml` and the delta `specs/refunds/spec.md` with ADDED ORD-F02, MODIFIED ORD-F01 (+1 scenario) and ORD-I01 (keeps the marker), REMOVED ORD-A01, RENAMED `ORD-N01 Old title` → `ORD-N01 New title`. |
| `changes/x/` | Tier-0 change created with `openspec new change x --schema aval`, filled with proposal, specs, design and tasks only (no premortem, no trace). Its base spec is `refunds/spec-after.md`. |
| `schemas/aval/` | The custom schema: `openspec schema fork spec-driven aval` plus the `premortem` and `trace` artifacts and their templates. `apply.requires` is still `[ tasks ]`. |
| `json/` | Captured `--json` outputs (table below). |
| `negative/<case>/` | Edge cases, each applied to `refunds/spec-after.md`: `delta.md` (the change's `specs/refunds/spec.md`), `validate.json` (`validate <case> --type change --strict --json`), `archive.txt` (`archive <case> -y`, stdout+stderr), and `spec-after.md` only when archive rewrote the spec. |

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

### `negative/`

| Case | validate (`--strict`) | archive | Meaning for aval |
|---|---|---|---|
| `modified-case-variant` | valid, `INFO` "Archive would refuse this delta" | refused, exit 1 | Treat that INFO as a failure. |
| `modified-drops-scenario` | `ERROR`, exit 1 | refused, exit 1 | Already caught by OpenSpec. |
| `modified-old-name-after-rename` | `ERROR`, exit 1 | refused, exit 1 | Already caught by OpenSpec. |
| `renamed-then-modified` | valid | applied | MODIFIED must use the new name. |
| `renamed-id-change` | valid | applied: `ORD-N01` becomes `ORD-N09` | aval must report it (removed + added). |
| `duplicate-id` | valid | applied: two `ORD-F01` requirements | aval must enforce unique IDs. |
| `bracket-id` | valid | applied: `[ORD-F03] …` | aval must reject the malformed ID. |
| `trailing-hash` | valid | applied, header written with ` ##` | Normalize like OpenSpec; lint it. |
| `modified-drops-marker` | valid | applied, `**aval**: characterization` lost | aval must flag the dropped marker. |
