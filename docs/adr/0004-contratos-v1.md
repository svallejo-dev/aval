# ADR-0004 — Contratos v1

- **Estado:** aceptada
- **Fecha:** 2026-09-21

## Contexto

Varios sub-agentes van a construir paquetes en paralelo (`openspec`, `gotest`, `evidence`, `gate`, `profile`…). Solo pueden hacerlo sin pisarse si los formatos que se pasan entre ellos, y los que ven los agentes, el CI y los servicios, están fijados de antemano y versionados.

## Decisión

Cada contrato vive en el paquete del concepto que representa, con tipos Go, JSON Schema (cuando es un fichero o una salida) y tests golden o de paridad.

| Contrato | Paquete | Formato |
|---|---|---|
| **ID de obligación** | `internal/obligation` | `<CTX>-<K><NN>`, p. ej. `ORD-F01` (convención y spike en ADR-0002). Política por tipo: F/N/I exigen test vinculado, S/A avisan, O bloquea |
| **Manifiesto del repo** | `internal/manifest` | `aval.yaml`: `version: 1`, `context`, `mode: observe\|enforce`, `tierDefault`, `openspec.version`, `paths.{dx,feat,seam}`. Schema en `internal/manifest/schema/aval.v1.json` |
| **Manifiesto del change** | `internal/manifest` | `openspec/changes/<id>/aval.yaml`: `version: 1`, `tier`, `owner`. Schema en `internal/manifest/schema/change.v1.json` |
| **Bundle de evidencia** | `internal/evidence` | JSON con `schemaVersion: 3` desde ADR-0005 (v1 tenía `override` y un solo `base`; v2 cambió `override` por `approvals`; v3 cambia `base` por `baseRef`, `trustBase` y `changeBase`). Schema en `internal/evidence/schema/bundle.v3.json` y golden en `internal/evidence/testdata/` |
| **Baseline** | `internal/baseline` | `.aval/baseline.json`: `version: 1`, `failing: [{package, test}]` (ADR-0005 §7). Schema en `internal/baseline/schema/baseline.v1.json` |
| **Envelope de `--json`** | `internal/envelope` (sin Charm, ADR-0003) | `{schemaVersion, command, ok, data, errors[{code, message, hint}]}`. `errors` es siempre un array, también en los fallos. Con `ok: false`, `data` trae el resultado si el comando lo produjo y falló sus comprobaciones (p. ej. `aval trace`), y es `null` si no llegó a producirlo |
| **Códigos de salida** | `internal/cli` | 0 OK · 1 verificación o gate fallido · 2 uso (también un manifiesto inválido) · 3 herramienta ausente o con versión distinta |
| **Bloques gestionados** | `internal/profile` (M4) | Marcadores con versión del perfil: `<!-- aval:begin profile=1 -->` / `# aval:begin profile=1`. Nunca `OPENSPEC:START/END`, porque OpenSpec los borra |

### Una sola fuente de verdad por contrato

- **Validación en tiempo de ejecución:** los manifiestos y el bundle se validan contra su propio JSON Schema embebido. Encima, Go comprueba solo las reglas que un schema no puede expresar.
- **Sin divergencias:** así Go y el schema no pueden separarse. Un campo ausente o `null` (por ejemplo un `tier` omitido, que valdría 0 y relajaría la política) o un número con decimales en un campo entero se rechazan igual en los dos.
- **Tests de paridad:** comprueban en ambas direcciones que las dos validaciones coinciden. Cada constante de Go tiene que ser aceptada por el schema.

### Reglas de los manifiestos

- **Decodificación estricta:** un campo desconocido, un documento vacío, más de un documento YAML o un fichero que supere el límite de tamaño son errores. Una errata nunca debe debilitar la política en silencio.
- **Validación completa:** se devuelven todos los problemas juntos, no solo el primero.
- **Reglas que solo comprueba Go:** la sintaxis de los globs y los patrones repetidos, dentro de una familia o entre familias.

### Reglas del bundle

- **SHAs completos:** `trustBase`, `changeBase` y `head` son de 40 caracteres, y `generatedAt` es obligatorio. `baseRef` es un nombre de rama, no un SHA. **No hay ningún campo `base`:** desde v3 el bundle obliga a nombrar cuál de las dos bases (ADR-0005 §1 y §7).
- **Listas nunca nulas:** "ningún hallazgo" se escribe `[]`, nunca `null`. En v1 el único campo que podía ser `null` era `override`; desde v2, ninguno.
- **Veredicto justificado:** un veredicto `warn` o `block` lleva al menos un motivo.
- **Fuerza de la evidencia, coherente con los estados:**

  | Fuerza | Requiere |
  |---|---|
  | `strong` | Falla en la base del cambio y pasa en head |
  | `weak` | Pasa en head y falla en la base del cambio por una causa que quizá no sea la falta del comportamiento: no compila su propio paquete de test, o head añade o modifica ficheros que no son Go ni están en `testdata/` dentro de un paquete de la obligación (en ese caso, con `note` obligatoria; ADR-0005 §2) |
  | `characterization` | Requisito marcado con `**aval**: characterization` que pasa en la base del cambio y en head |
  | `none` | Nada: es la ausencia de evidencia aceptable |

  Falla-antes solo aplica a obligaciones con `delta` `added` o `modified`.
- **Override:** se registra aunque se rechace, incluso si le falta el motivo. Es válido solo si lo puso un CODEOWNER, con motivo, y después de un último commit conocido (`labeledAt` > `lastCommitAt`). Si no es válido, lleva `rejection`. **Sustituido en el bundle v2** por `approvals` (reviews ligados al SHA head, ADR-0005 §5 y §7).
- **Commit mixto:** es `mixed` exactamente cuando toca `dx` **y** `feat`, y las lista en `families`; `seam` y `other` nunca lo hacen mixto (enmendado por ADR-0005 §3b).
- **Tipos de manipulación:** `fingerprint_changed`, `test_removed`, `skip_added`, `policy_edited`, `baseline_edited`.
- **La política viene de la base de confianza:** el gate lee `aval.yaml` y el baseline del tip de la **rama por defecto del repositorio**, nunca del head, ni de la rama base del PR, ni del merge-base (ADR-0005 §1: la base de confianza es la rama por defecto; la base del cambio, el merge-base, solo delimita qué hizo este PR). En modo `observe` el veredicto se informa sin bloquear; en `enforce`, un `block` bloquea.
- **Recalcular en el mismo job:** el gate siempre recalcula la evidencia y nunca reutiliza un bundle del disco (ADR-0005 §7). `CheckHead` comprueba la coherencia del bundle que él mismo acaba de calcular; no es permiso para leer uno ajeno.
- **`notCollected`** declara con tokens estables la evidencia que la v0 aún no reúne (`mutation`, `rollback`, `slo`) y la que se saltó en esta ejecución (`openspec_validate` sin política en la base de confianza), para que ninguna ausencia parezca un pase. El schema no los enumera: añadir un token no cambia la forma ni sube la versión.

## Versionado

- **Los schemas son cerrados** (`additionalProperties: false`): un lector nunca acepta campos que no entiende.
- **Por tanto, cualquier cambio de forma sube la versión** (`schemaVersion` o `version`), incluso añadir un campo opcional, y exige un ADR nuevo.
- **Es barato:** el bundle lo produce y lo consume el mismo binario en el mismo job, y los manifiestos se validan con la versión de aval fijada en el repo.
- **aval rechaza versiones que no conoce** con exit 2, en lugar de interpretarlas.
- **Ratificado por el usuario el 2026-09-22.** Esto sustituye la regla del plan inicial, según la cual añadir campos opcionales mantenía la versión.

## Consecuencias

- Los sub-agentes reciben el contrato como especificación y no tocan estos paquetes sin pasar por el orquestador.
- Los tests golden y de paridad hacen visible cualquier cambio de formato en el diff del PR.
