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

### Reglas de los manifiestos

- **Decodificación estricta:** un campo desconocido, un documento vacío o más de un documento YAML son errores. Una errata nunca debe debilitar la política en silencio.
- **Validación completa:** se devuelven todos los problemas juntos, no solo el primero.
- **Paridad:** la validación en Go y el JSON Schema coinciden. Las únicas excepciones son reglas que solo Go puede comprobar (sintaxis de globs, patrones repetidos entre familias, un solo documento), y el test las nombra.

### Reglas del bundle

- **SHAs completos:** `base` y `head` son de 40 caracteres.
- **Veredicto justificado:** un veredicto `warn` o `block` lleva al menos un motivo.
- **Override con motivo:** un override sin motivo es inválido.
- **Recalcular en el mismo job:** el gate recalcula la evidencia y rechaza un bundle cuyo `head` no coincide con `HEAD` (`CheckHead`).
- **`notCollected`** declara la evidencia que la v0 aún no reúne (`mutation`, `rollback`, `slo`), para que su ausencia sea explícita y no parezca un pase.

## Versionado

- **Añadir campos opcionales** mantiene la versión.
- **Quitar un campo o cambiar su significado** sube la versión (`schemaVersion`, `version`) y obliga a un ADR nuevo.
- **aval rechaza versiones que no conoce** con exit 2, en lugar de interpretarlas.

## Consecuencias

- Los sub-agentes reciben el contrato como especificación y no tocan estos paquetes sin pasar por el orquestador.
- Los tests golden y de paridad hacen visible cualquier cambio de formato en el diff del PR.
