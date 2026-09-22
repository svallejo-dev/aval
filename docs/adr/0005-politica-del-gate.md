# ADR-0005 — Política del gate v0

- **Estado:** aceptada
- **Fecha:** 2026-09-22

## Contexto

M2 construye `aval verify`, que reúne la evidencia, y `aval gate`, que decide si un PR puede entrar. Varios agentes van a implementar en paralelo sus piezas: falla-antes por superposición, scope por rutas, hooks, el motor del gate y los comandos. Necesitan un contrato común sobre cinco cosas:
- qué entra al gate;
- qué reglas aplica;
- con qué códigos explica su veredicto;
- cuándo vale un override;
- cómo se traduce todo a un código de salida.

Los formatos (bundle, manifiestos, envelope) ya están en ADR-0004. Este ADR fija el **comportamiento**.

## Decisión

### 1. Entradas

El gate trabaja sobre un rango `base..head`:
- **En CI:** `base` es el merge-base del PR (con `fetch-depth: 0`) y `head` es el commit del PR.
- **En local:** `--base` y `--head` (por defecto `origin/main` y `HEAD`).

| Entrada | De dónde sale |
|---|---|
| Política | `aval.yaml` **del SHA base** (`git show <base>:aval.yaml`), nunca del head |
| Changes del PR | Directorios de `openspec/changes/<id>/` o `openspec/changes/archive/<fecha>-<id>/` que el diff `base..head` toca |
| Tier | El mayor `tier` de los manifiestos de esos changes; si un change no tiene manifiesto, `tierDefault` de la política. **Sin changes, Tier 0** |
| Obligaciones del delta | Requisitos ADDED y MODIFIED de esos changes (`Delta`: `added`/`modified`); el resto son `unchanged` |
| Specs y reglas | `openspec.Load` en head, más `Check(CheckOptions{Base: <repo en base>})` |
| Validación | `openspec.Validate` con la versión de la política, si el PR toca `openspec/` |
| Declaraciones | `testsource.Scan` en base y en head, y `Compare` con los IDs del delta |
| Ejecución | `gotest` en head; falla-antes sobre la superposición; regresiones aisladas |
| Scope | Clasificación de cada commit `base..head` por las rutas de la política |
| Etiquetas | `aval:override` y `aval:human-approved`: quién, cuándo y motivo, leídos de la API de GitHub |

### 2. Falla-antes por superposición

Aplica solo a las obligaciones **F, N e I** con delta `added` o `modified`:
1. Se crea `git worktree add --detach <tmp> <base>`.
2. Encima se copian, desde head, los `*_test.go` añadidos o modificados y los ficheros de `testdata/` añadidos o modificados de esos paquetes.
3. Se ejecuta `go test -json -run '^TestX$/^(ID1|ID2)([_#]|$)'`, una vez por Test.
4. Se calcula el estado de cada ID con `gotest.Report.Status(id, <paquetes de sus tests en head>)`.
5. El worktree se elimina siempre, también si algo falla.

| Estado en la base | Fuerza (ADR-0004) | Efecto |
|---|---|---|
| `fail` | `strong` | Válido |
| `build_fail` | `weak` | Válido, se informa como evidencia débil |
| `pass` con `**aval**: characterization` | `characterization` | Válido |
| `pass` sin la marca | `none` | Bloquea (`fail_before_missing`) |
| `not_run` / `skipped` | `none` | Bloquea (`fail_before_missing`) |

### 3. Regresiones aisladas

Las obligaciones F, N e I **sin** delta que tengan tests vinculados se ejecutan en head **aisladas**: un `go test -run '^TestX$/^(ID1|ID2)([_#]|$)'` por Test. Así los subtests hermanos y los Tests anteriores no pueden alterar su estado compartido (huecos aceptados de `testsource`). Tienen que pasar.

### 4. Reglas y códigos de motivo

Cada regla que no se cumple añade un `Reason{Code, Message, ID}` al veredicto. Los códigos son estables: los scripts y el resumen de CI dependen de ellos.

| Código | Cuándo | Tier | Efecto |
|---|---|---|---|
| `spec_rule` | `openspec.Check` devuelve un hallazgo de severidad error | 0–3 | block |
| `openspec_invalid` | `Validate` falla (todo INFO es fallo salvo la allowlist de ADR-0002) | 0–3, si el PR toca `openspec/` | block |
| `open_question` | Hay una obligación **O** en el delta | 1–3 | block |
| `unverified` | Una obligación F/N/I del delta no tiene test vinculado | 1–3 | block |
| `fail_before_missing` | Falla-antes no es válido (§2) | 1–3 | block |
| `after_not_passing` | Un test vinculado (del delta o no) no pasa en head | 0–3 | block |
| `regression` | Un test que pasaba en la base falla en head y no está en el baseline | 0–3 | block |
| `build_failed` | Algún paquete no compila, o `go test` falla al preparar la ejecución, en head | 0–3 | block |
| `tamper` | `testsource.Compare` devuelve un hallazgo para un ID fuera del delta, o el PR edita `aval.yaml` o `.aval/baseline.json` | 0–3 | block |
| `undeclared_runtime` | Un test con ID se ejecuta sin declaración estática que lo empareje | 0–3 | block |
| `mixed_commit` | Un commit toca rutas de `dx` y de `feat` | 0–3 | block |
| `premortem_missing` | Un change no tiene `premortem.md` | 2–3 | block |
| `premortem_unmapped` | Un ítem de lista de `premortem.md` no cita ningún ID del delta | 2–3 | block |
| `approval_missing` | No hay una etiqueta `aval:human-approved` válida (§5) | 3 | block |
| `lint_new_issues` | golangci-lint informa de problemas en código nuevo (`issues.new-from-merge-base`) | 0–3, si el repo tiene `.golangci.yml` | block |
| `assumption` | Hay una obligación **A** en el delta | 1–3 | warn |
| `slo_unverified` | Hay una obligación **S** en el delta (la v0 no mide SLOs) | 1–3 | warn |
| `spec_warning` | `openspec.Check` devuelve un hallazgo de severidad warn | 0–3 | warn |
| `seam_touched` | Un commit `feat` toca rutas de `seam` | 0–3 | warn |
| `weak_evidence` | Alguna obligación tiene fuerza `weak` | 1–3 | warn |
| `no_base_policy` | El SHA base no tiene `aval.yaml` (PR de adopción) | — | warn; el gate corre en `observe` |

Códigos **reservados** para hitos posteriores, que no se emiten en la v0 de M2:
- `contract_breaking` (oasdiff, M4);
- `contract_lint` (vacuum, M4);
- `change_not_archived` (M3).

**El resultado del veredicto** es `block` si alguna regla bloquea, `warn` si solo hay avisos, y `pass` si no hay motivos.

### 5. Etiquetas y override

Las dos etiquetas se validan igual. Una etiqueta es **válida** solo si se cumplen tres condiciones:
1. **La puso un CODEOWNER.** En la v0 eso significa:
   - un usuario listado **individualmente** en el `CODEOWNERS` del SHA base para `aval.yaml`;
   - o un usuario con permiso `admin` o `maintain` en el repo.

   Los equipos de GitHub exigen un token con `read:org` y quedan para más adelante.
2. **Se puso después del último commit.** La hora de referencia es el `created_at` de la primera *check suite* de GitHub para el SHA head. Es hora del servidor, así que no se puede falsificar con la fecha de un commit.
3. **Tiene motivo** (solo para `aval:override`): una sección `## aval override` con texto no vacío en la descripción del PR.

Efectos:
- **`aval:override` válida** convierte un `block` en `warn`. Los motivos se conservan y el override queda en el bundle (`valid: true`).
- **`aval:override` inválida** también se registra, con `valid: false` y su `rejection`, y no cambia el veredicto.
- **`aval:human-approved` válida** solo satisface `approval_missing`, no ninguna otra regla.

### 6. Modos y códigos de salida

- **El modo sale de la política del SHA base.** Si el propio PR cambia `mode`, eso no tiene efecto hasta que se fusiona, y además dispara `tamper`.
- **`observe`:** el gate calcula e informa el veredicto completo y sale con 0.
- **`enforce`:** sale con 1 si el veredicto es `block`, y con 0 si es `pass` o `warn`.

| Código | Cuándo |
|---|---|
| 0 | `pass` o `warn`; o cualquier veredicto en `observe` |
| 1 | `block` en `enforce` |
| 2 | Uso incorrecto o política inválida (ADR-0004) |
| 3 | Falta una herramienta (`go`, `git`, `node`/`npm`) o su versión no es la fijada |

### 7. Evidencia y resumen

- **`aval verify`** escribe el bundle en `.aval/evidence/<head>.json` y deja el estado para los hooks en `.aval/cache/verify-status.json`, con clave `HEAD` + hash de `git diff HEAD`.
- **`aval gate`** recalcula la evidencia en el mismo job (o reutiliza la de `verify` si `CheckHead` coincide), decide, escribe el veredicto en el bundle y, en GitHub Actions, un resumen en `$GITHUB_STEP_SUMMARY`.
- El workflow sube el bundle como artefacto.

### 8. Seguridad del workflow

- **El gate corre con `on: pull_request`, nunca con `pull_request_target`:** ejecuta código del PR, así que no tiene secretos.
- **Permisos de solo lectura:** `contents: read`, `pull-requests: read`, `issues: read` y `checks: read`.
- **Eventos `labeled` y `unlabeled`** también disparan el gate, para que una etiqueta lo reevalúe.

## Consecuencias

- **Los motivos son un contrato:** añadir un código es compatible, pero cambiar el significado de uno existente exige un ADR nuevo.
- **La primera adopción no puede bloquear:** un PR que introduce `aval.yaml` corre en `observe` (`no_base_policy`), y la política entra en vigor desde el siguiente PR.
- **Huecos que quedan fuera de la v0:**
  - equipos de GitHub en CODEOWNERS;
  - reglas de contrato (M4);
  - archivo obligatorio del change (M3).
