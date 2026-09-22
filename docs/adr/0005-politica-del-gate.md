# ADR-0005 — Política del gate v0

- **Estado:** aceptada
- **Fecha:** 2026-09-22

## Contexto

M2 construye `aval verify`, que reúne la evidencia, y `aval gate`, que decide si un PR puede entrar. Varios agentes van a implementar en paralelo sus piezas: falla-antes por superposición, scope por rutas, hooks, el motor del gate y los comandos. Necesitan un contrato común sobre cinco cosas:
- qué entra al gate;
- qué reglas aplica;
- con qué códigos explica su veredicto;
- cuándo vale una aprobación o una excepción;
- cómo se traduce todo a un código de salida.

Los formatos base están en ADR-0004. Este ADR fija el **comportamiento** y los cambios de contrato que ese comportamiento exige (bundle v2 y baseline v1).

## Modelo de amenazas

El gate defiende de un **agente** (o una persona) que quiere fusionar un cambio sin la evidencia que la política exige. Puede editar cualquier fichero del PR y ejecuta código en el job de CI a través de los tests.

**Qué no puede hacer, porque aval no confía en nada del head para decidir:**
- cambiar la política;
- cambiar el tier por debajo del mínimo;
- falsificar aprobaciones;
- alterar lo que aval lee de la base.

**Qué queda fuera de aval y exige configuración de GitHub:** un workflow de `pull_request` ejecuta la definición del workflow **del propio PR**. Un PR podría, por tanto, editar el job del gate. La integridad del gate depende de proteger `.github/**` con CODEOWNERS y **revisión obligatoria de code owner** en el ruleset.

aval informa además de cualquier edición de `.github/**`, `CODEOWNERS`, `.golangci.yml`, el `aval.yaml` raíz y `.aval/baseline.json` como `tamper` (§4). La protección fuerte, sin embargo, es la de GitHub.

**Límites conocidos de la v0:**
- **Credenciales:** un agente que opera con las credenciales de una persona actúa como esa persona. Los agentes deben usar una cuenta o token propio, sin permisos de revisión.
- **Runner comprometido:** el código del PR se ejecuta en el mismo runner que el gate, y en los runners de GitHub tiene sudo sin contraseña. Código malicioso que ataque al propio runner (sustituir el binario de aval, alterar ficheros entre pasos) queda fuera de lo que aval puede defender en la v0. Por eso el gate verifica en el mismo proceso, y la mitigación de fondo es ejecutar los tests en un entorno aislado del gate.

## Decisión

### 1. Entradas

El gate trabaja sobre un rango `base..head`:
- **En CI:** `base` es el merge-base del PR con `main` (`fetch-depth: 0`) y `head` es `github.event.pull_request.head.sha`. El checkout usa `ref:` head, no el merge commit.
- **En local:** `--base` y `--head` (por defecto el merge-base con `origin/main` y `HEAD`).

**Orden obligatorio:** todas las entradas de la base (política, CODEOWNERS, baseline, specs de la base, `.golangci.yml` de la base, declaraciones de la base) se leen **antes** de ejecutar cualquier test del PR. Todo `git` corre con `GIT_NO_REPLACE_OBJECTS=1`.

| Entrada | De dónde sale |
|---|---|
| Política | El `aval.yaml` **raíz del SHA base**. Si no existe, el gate corre en `observe` (`no_base_policy`) |
| Changes del PR | Directorios de `openspec/changes/<id>/` o `openspec/changes/archive/<fecha>-<id>/` que toca el diff `base..head` |
| Tier | **max**(`tierDefault` si hay algún commit `feat` o `mixed` o algún change; el `tier` de cada change en head; el `tier` de ese mismo change en la base si ya existía). Un PR solo `dx`/`seam`/`other` y sin changes es **Tier 0**. El head puede subir el tier, nunca bajarlo |
| Obligaciones del delta | IDs **ADDED ∪ MODIFIED ∪ REMOVED** de esos changes. Solo las ADDED y MODIFIED (`added`/`modified`) exigen falla-antes; el resto son `unchanged`. RENAMED **no** forma parte del delta: solo cambia el título y conserva el ID, y el test no necesita cambiar |
| Specs y reglas | `openspec.Load` en head, `Check(CheckOptions{Base})`, y el mismo `Check` en la base, para reportar solo los hallazgos **nuevos** |
| Validación | `openspec.Validate` con la versión de la política, si el PR toca `openspec/` |
| Declaraciones | `testsource.Scan` en base y en head; `Compare` con `changed` = ADDED ∪ MODIFIED ∪ REMOVED. Un ID solo renombrado sigue protegido contra la manipulación |
| Ejecución | Ejecución completa de `gotest` en head, falla-antes (§2) y regresiones aisladas (§3) |
| Scope | §3b |
| Aprobaciones | Reviews del PR (§5), leídas de la API de GitHub |

### 2. Falla-antes por superposición

Aplica a las obligaciones **F, N e I** con delta `added` o `modified`:
1. **Worktree:** `git worktree add --detach <tmp> <base>`.
2. **Superposición:** en los paquetes de sus tests se copian, desde head, los `*_test.go` añadidos o modificados y los ficheros de `testdata/` añadidos o modificados. **Se borran** las rutas que head eliminó o renombró en esos paquetes.
3. **Selección exacta:** a partir de los **nombres completos** de sus tests en la ejecución de head (por ejemplo `TestSuite/TestX/ORD-F01_…`) se construye un patrón anclado por nivel: `-run '^TestSuite$/^TestX$/^ORD-F01([_#]|$)'`. Cada nivel va escapado, y los IDs se agrupan por Test de primer nivel.
4. **Estado:** el de cada ID sale de `gotest.Report.Status(id, <paquetes de sus tests en head>)`.
5. **Limpieza:** el worktree se elimina siempre.

La fuerza (ADR-0004) exige **además** que el estado en head sea `pass`:

| Estado en la base | Fuerza | Efecto |
|---|---|---|
| `fail` | `strong` | Válido |
| `build_fail` | `weak` | Válido; motivo `weak_evidence` |
| `pass` con `**aval**: characterization` | `characterization` | Válido |
| `pass` sin la marca | `none` | Bloquea (`fail_before_missing`) |
| `not_run` / `skipped` | `none` | Bloquea (`fail_before_missing`) |

### 3. Regresiones aisladas

Las obligaciones F, N e I **fuera del delta** que tienen tests vinculados se ejecutan en head **aisladas**, con la selección exacta de §2.3, un `go test` por Test de primer nivel. Así los subtests hermanos y los Tests anteriores no pueden alterar su estado. Tienen que pasar.

### 3b. Clasificación de commits (scope)

Cada commit de `base..head` se clasifica por sus rutas con los globs `paths.dx`, `paths.feat` y `paths.seam` de la política **del SHA base**. Si una ruta encaja en varias familias, gana `seam` y después el glob más largo.

| Rutas del commit | Familia |
|---|---|
| `dx`, más cualquier `seam` u otras | `dx` |
| `feat`, más cualquier `seam` u otras | `feat` |
| `dx` **y** `feat` | `mixed`, con `families: [dx, feat]` → bloquea (`mixed_commit`) |
| Solo `seam` | `seam` |
| Ninguna familia | `other` |

`seam` y `other` **nunca** hacen `mixed` a un commit. Esto enmienda la frase de ADR-0004 "mixed cuando toca dos o más familias": `mixed` significa que toca `dx` y `feat`.

**Commits de merge:** se clasifican con `git show --remerge-diff --name-only --format=`, que requiere git ≥ 2.36 (con uno más antiguo, exit 3).

| Tipo de merge | Rutas que aporta |
|---|---|
| Merge limpio | Ninguna |
| "Evil merge" | Las que introduce el propio merge |
| Resolución de conflicto | Los ficheros resueltos |
| Resolución que descarta cambios de una rama | Esos ficheros |
| Merge octopus (más de dos padres) | `--remerge-diff` no lo soporta: se usa `--cc`, que da los ficheros que el propio merge cambia pero **no** los cambios descartados. Límite conocido de la v0 |

### 4. Reglas y códigos de motivo

Cada regla incumplida añade un `Reason{Code, Message, ID}` al veredicto. Los códigos son estables.

| Código | Cuándo | Tier | Efecto |
|---|---|---|---|
| `spec_rule` | Un hallazgo de severidad error de `openspec.Check` en head **que no existía en la base**, emparejando por regla, ruta y mensaje, sin número de línea. La ruta de un change archivado en el PR se normaliza a la del change activo en la base, para que moverlo no convierta sus hallazgos en nuevos | 0–3 | block |
| `openspec_invalid` | `Validate` falla (todo INFO es fallo salvo la allowlist de ADR-0002) | 0–3, si el PR toca `openspec/` | block |
| `open_question` | Una obligación **O** ADDED o MODIFIED. Eliminar una O para resolverla no bloquea | 1–3 | block |
| `unverified` | Una obligación F/N/I ADDED o MODIFIED sin test vinculado | 1–3 | block |
| `fail_before_missing` | Falla-antes no válido (§2) | 1–3 | block |
| `after_not_passing` | Un test **vinculado** (del delta o no) no pasa en head | 0–3 | block |
| `regression` | Un test **no vinculado** falla en head y no figura en el baseline de la base | 0–3 | block |
| `build_failed` | Algún paquete no compila o `go test` falla al preparar la ejecución, en head | 0–3 | block |
| `tamper` | `testsource.Compare` devuelve un hallazgo, o el PR edita el `aval.yaml` raíz, `.aval/baseline.json`, `.github/**`, `CODEOWNERS` o `.golangci.yml`. Esas ediciones se registran como hallazgos `policy_edited` (baseline: `baseline_edited`) | 0–3 | block |
| `undeclared_runtime` | Un test con ID se ejecuta sin declaración estática que lo empareje | 0–3 | block |
| `mixed_commit` | Un commit `mixed` (§3b) | 0–3 | block |
| `premortem_missing` | Un change de tier ≥ 2 sin `premortem.md` | según cada change | block |
| `premortem_unmapped` | Un `premortem.md` sin ítems, o con un ítem que no cita ningún ID de **su** change. Ítem = elemento de lista de primer nivel, fuera de bloques de código | según cada change | block |
| `approval_missing` | Tier 3 sin una aprobación válida (§5) | 3 | block |
| `lint_new_issues` | golangci-lint, con **el `.golangci.yml` de la base**, informa de problemas nuevos (`issues.new-from-merge-base`) | 0–3, si la base tiene `.golangci.yml` | block |
| `assumption` | Una obligación **A** ADDED o MODIFIED | 1–3 | warn |
| `slo_unverified` | Una obligación **S** ADDED o MODIFIED (la v0 no mide SLOs) | 1–3 | warn |
| `spec_warning` | Un hallazgo de severidad warn nuevo de `openspec.Check`, emparejado igual que `spec_rule` | 0–3 | warn |
| `seam_touched` | Un commit `feat` toca rutas `seam` | 0–3 | warn |
| `weak_evidence` | Alguna obligación con fuerza `weak` | 1–3 | warn |
| `no_base_policy` | El SHA base no tiene `aval.yaml` raíz (PR de adopción) | — | warn; el gate corre en `observe` |

Códigos **reservados** para hitos posteriores: `contract_breaking` y `contract_lint` (M4), y `change_not_archived` (M3).

**El resultado del veredicto** es `block` si alguna regla bloquea, `warn` si solo hay avisos, y `pass` si no hay motivos.

### 5. Aprobaciones y excepciones: reviews ligados al SHA

Las etiquetas no sirven: no se atan a un commit, y la hora de GitHub que podría anclarlas (la check suite) se puede adelantar empujando el SHA a otra rama. aval usa **reviews del PR**, que GitHub liga al commit revisado.

Una aprobación es **válida** si cumple tres condiciones:
1. es el **último review que no sea `COMMENTED`** de ese revisor (como hace GitHub), tiene `state: APPROVED` y **`commit_id` igual al SHA head**. Un `CHANGES_REQUESTED` o `DISMISSED` posterior la anula;
2. su autor es un **CODEOWNER**: un usuario listado individualmente en el `CODEOWNERS` de la base para el `aval.yaml` raíz, o con `role_name` ∈ {`admin`, `maintain`} (`GET /repos/{o}/{r}/collaborators/{user}/permission`; el campo `permission` no sirve porque reporta `maintain` como `write`);
3. GitHub ya impide que el autor del PR apruebe su propio PR.

Hay dos tipos:

| Tipo | Requisito extra | Efecto |
|---|---|---|
| **Aprobación humana** | Ninguno | Satisface `approval_missing`; nada más |
| **Excepción (override)** | Una línea `aval:override <motivo>` en el cuerpo del review, con motivo no vacío | Convierte un `block` en `warn`; los motivos se conservan |

- **Todas las aprobaciones se registran en el bundle,** también las inválidas, con su `rejection`.
- **Un review de un commit anterior no cuenta.** Cuando llega un commit nuevo, hace falta volver a aprobar.
- **Los equipos de GitHub** exigen un token con `read:org` y quedan fuera de la v0.

### 6. Modos y códigos de salida

- **El modo sale de la política del SHA base.** Cambiarlo en el PR dispara `tamper` y no tiene efecto hasta fusionarse.
- **`observe`:** el gate calcula e informa el veredicto y sale con 0.
- **`enforce`:** sale con 1 si el veredicto es `block`.

| Código | Cuándo |
|---|---|
| 0 | `pass` o `warn`; o cualquier veredicto en `observe` |
| 1 | `block` en `enforce` |
| 2 | Uso incorrecto o política inválida (ADR-0004) |
| 3 | Falta una herramienta (`go`, `git` ≥ 2.36, `node`/`npm`) o su versión no es la fijada |

### 7. Evidencia, baseline y resumen

- **`aval verify`** escribe el bundle en `.aval/evidence/<head>.json` y el estado para hooks en `.aval/cache/verify-status.json` (formato de `internal/hook`), con clave `HEAD` + hash de `git diff HEAD`.
- **`aval gate` en CI** (`GITHUB_ACTIONS=true`) ejecuta la verificación **en el mismo proceso** y **nunca reutiliza un bundle del disco**: el código del PR corre en el mismo runner y podría sobrescribir ficheros entre pasos, y `CheckHead` solo compara un SHA que es público.
- **`aval gate` en local** puede reutilizar el bundle de `verify` si `CheckHead` coincide.
- **Cierre:** el gate decide, escribe el veredicto y, en GitHub Actions, un resumen en `$GITHUB_STEP_SUMMARY`. El workflow sube el bundle como artefacto.

**Bundle v2** (sube `schemaVersion`, según ADR-0004): sustituye `override` por **`approvals`**, una lista de:

```
{kind: "approval" | "override", actor, commitId, submittedAt, reason, valid, rejection}
```

`reason` solo aplica a `override`, y `rejection` solo a las inválidas. El resto del bundle v1 no cambia.

**Baseline v1** (`.aval/baseline.json`, contrato nuevo en `internal/baseline`, con JSON Schema):

```
{version: 1, failing: [{package, test}]}
```

Lista los tests no vinculados que ya fallaban al adoptar aval. Lo escribe `aval dx adopt` (M4); en M2, si no existe, se trata como vacío.

### 8. Workflow

- **Evento `on: pull_request`** con los tipos `opened`, `synchronize`, `reopened` y `ready_for_review`, más **`on: pull_request_review`** para que una aprobación nueva reevalúe el gate.
- **Checkout** con `fetch-depth: 0` y `ref: ${{ github.event.pull_request.head.sha }}`.
- **Permisos de solo lectura:** `contents: read`, `pull-requests: read`, `checks: read`. Sin secretos.
- **Prohibido filtrar el job con `if:` a nivel de job.** Un job saltado cuenta como éxito para un check obligatorio.

## Consecuencias

- **Los códigos de motivo son un contrato:** añadir uno es compatible, cambiar el significado de uno existente exige un ADR nuevo.
- **La primera adopción no puede bloquear** (`no_base_policy`).
- **Bundle v2 y baseline v1** se implementan en `internal/evidence` e `internal/baseline` antes del motor del gate.
- **La aprobación exige una segunda persona.** En un repo de una sola persona (el sandbox), `approval_missing` y `override` solo se pueden probar de punta a punta con una segunda cuenta de GitHub; mientras tanto se prueban contra una API de GitHub simulada.
- **Huecos fuera de la v0:**
  - equipos en CODEOWNERS;
  - reglas de contrato (M4);
  - archivo obligatorio del change (M3);
  - ejecución del gate desde una definición de workflow de confianza (por ejemplo, workflows requeridos por ruleset).
