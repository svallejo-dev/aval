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
| **Bundle de evidencia** | `internal/evidence` | JSON con `schemaVersion: 1`. Schema en `internal/evidence/schema/bundle.v1.json` y golden en `internal/evidence/testdata/` |
| **Envelope de `--json`** | `internal/cli` (pasará a `internal/ui`) | `{schemaVersion, command, ok, data, errors[{code, message, hint}]}`. `errors` es siempre un array, también en los fallos |
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

- **SHAs completos:** `base` y `head` son de 40 caracteres, y `generatedAt` es obligatorio.
- **Listas nunca nulas:** "ningún hallazgo" se escribe `[]`, nunca `null`. El único campo que puede ser `null` es `override`.
- **Veredicto justificado:** un veredicto `warn` o `block` lleva al menos un motivo.
- **Fuerza de la evidencia, coherente con los estados:**

  | Fuerza | Requiere |
  |---|---|
  | `strong` | Falla en la base y pasa en head |
  | `weak` | No compila en la base y pasa en head |
  | `characterization` | Requisito marcado con `**aval**: characterization` y que pasa en head |
  | `none` | Nada: es la ausencia de evidencia aceptable |

  Falla-antes solo aplica a obligaciones con `delta` `added` o `modified`.
- **Override:** se registra aunque se rechace. Es válido solo si lo puso un CODEOWNER, con motivo, y después del último commit (`labeledAt` > `lastCommitAt`). Si no es válido, lleva `rejection`.
- **Commit mixto:** es `mixed` exactamente cuando toca dos o más familias, y las lista en `families`.
- **Tipos de manipulación:** `fingerprint_changed`, `test_removed`, `skip_added`, `policy_edited`, `baseline_edited`.
- **La política viene de la base:** el gate lee `aval.yaml` y el baseline del SHA base, nunca del head. En modo `observe` el veredicto se informa sin bloquear; en `enforce`, un `block` bloquea.
- **Recalcular en el mismo job:** el gate recalcula la evidencia y rechaza un bundle cuyo `head` no coincide con `HEAD` (`CheckHead`).
- **`notCollected`** declara la evidencia que la v0 aún no reúne (`mutation`, `rollback`, `slo`), para que su ausencia sea explícita y no parezca un pase.

## Versionado

- **Los schemas son cerrados** (`additionalProperties: false`): un lector nunca acepta campos que no entiende.
- **Por tanto, cualquier cambio de forma sube la versión** (`schemaVersion` o `version`), incluso añadir un campo opcional, y exige un ADR nuevo.
- **Es barato:** el bundle lo produce y lo consume el mismo binario en el mismo job, y los manifiestos se validan con la versión de aval fijada en el repo.
- **aval rechaza versiones que no conoce** con exit 2, en lugar de interpretarlas.

## Consecuencias

- Los sub-agentes reciben el contrato como especificación y no tocan estos paquetes sin pasar por el orquestador.
- Los tests golden y de paridad hacen visible cualquier cambio de formato en el diff del PR.
