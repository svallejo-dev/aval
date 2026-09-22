# ADR-0002 — ID de obligación en el nombre del requirement

- **Estado:** aceptada
- **Fecha:** 2026-09-21
- **Origen:** spike S1. La evidencia reproducible está en [`internal/openspec/testdata/spike/`](../../internal/openspec/testdata/spike/README.md).

## Contexto

aval convierte cada obligación de la spec en una fila `obligación → verificador → evidencia → gate`. Para eso necesita un ID estable que viaje de la spec de OpenSpec al test de Go y a la evidencia.

En OpenSpec, la identidad de un requirement es el texto que sigue a `### Requirement:`. Archive casa los requirements por esa cabecera normalizada (trim y sin `#` de cierre), distinguiendo mayúsculas. `openspec show --json` no devuelve los nombres de requirements ni de escenarios, solo el cuerpo. Por eso aval parsea el markdown por su cuenta y usa la CLI solo para `validate`, `new change` y `status`.

La pregunta del spike: **¿un ID al principio del nombre sobrevive a `validate --strict` y a `archive` con ADDED, MODIFIED, RENAMED y REMOVED?**

## Convención

- **Formato:** `<CTX>-<K><NN>`.
  - `CTX` es el contexto en mayúsculas (`ORD`).
  - `K` es la clase de obligación, una de `F N I S A O`.
  - `NN` son dos dígitos.
  - Regex: `^[A-Z]{2,6}-[FNISAO][0-9]{2}`.
- **En OpenSpec:** el ID abre el nombre del requirement, seguido de un espacio y un título.

  ```markdown
  ### Requirement: ORD-F01 Refund is idempotent
  ```

  El nombre completo tiene menos de 50 caracteres, sin backticks (RENAMED los usa como delimitador) y sin `#` al final.
- **En Go:** el subtest empieza por el mismo ID: `t.Run("ORD-F01 Refund is idempotent", …)`. test2json lo emite como `TestRefund/ORD-F01_Refund_is_idempotent`, porque Go cambia los espacios por `_` y el ID no tiene ninguno. aval casa por el ID del segmento con `^ORD-F01(_|$)`, no por el título, y `go test -run 'TestRefund/^ORD-F01_'` ejecuta solo esa obligación.
- **Caracterización:** si un requirement describe comportamiento que ya existía, lleva la línea de metadatos `**aval**: characterization` en su cuerpo.

## Evidencia

Entorno: `@fission-ai/openspec@1.13.1` con `npx -y`, Node v26.7.0, `OPENSPEC_TELEMETRY=0 OPENSPEC_NO_UPDATE_CHECK=1`, Go 1.27.1 y un repo temporal.

### 1. Spec base

- `openspec init --tools none --no-animation .` funciona sin preguntar nada y crea `openspec/{config.yaml,specs/,changes/archive/}` con `schema: spec-driven`.
- Se escribió `openspec/specs/refunds/spec.md` con cuatro requirements:
  - `ORD-F01 Refund is idempotent`
  - `ORD-N01 Old title`
  - `ORD-I01 Ledger entry per refund`, con `**aval**: characterization`
  - `ORD-A01 Legacy refund email`
- `openspec validate --all --strict --json` devuelve `"valid": true, "issues": []`.
- `openspec show refunds --type spec --json` no incluye nombres: cada requirement trae solo `text` y `scenarios[].rawText`. La línea `**aval**: characterization` sigue en el fichero, pero se elimina del `text` (`"text": "The system SHALL write exactly one ledger entry…"`). `-r` selecciona por índice empezando en 1, no por nombre.

### 2. Change completo y archive

- `openspec new change add-refund-limits --json` crea solo `.openspec.yaml`. Después se escribieron:
  - `proposal.md`, con `## Why` y `## What Changes`;
  - `tasks.md`;
  - el delta `specs/refunds/spec.md`:
    - ADDED `ORD-F02`;
    - MODIFIED `ORD-F01`, que conserva sus dos escenarios y añade uno;
    - MODIFIED `ORD-I01`, que conserva la marca;
    - REMOVED `ORD-A01`, con `**Reason**` y `**Migration**`;
    - RENAMED de `ORD-N01 Old title` a `ORD-N01 New title`.
- `openspec validate add-refund-limits --type change --strict --json` devuelve `"valid": true, "issues": []`.
- `openspec show … --json --deltas-only`: solo RENAMED lleva nombres (`"rename": {"from": "ORD-N01 Old title", "to": "ORD-N01 New title"}`). ADDED, MODIFIED y REMOVED traen solo el cuerpo.
- `openspec archive add-refund-limits -y` sale con 0 y muestra `+ 1 added`, `~ 2 modified`, `- 1 removed` y `→ 1 renamed`.
- La spec resultante (`refunds/spec-after.md`) queda así:
  1. `ORD-F01 Refund is idempotent`, en su sitio y con sus 3 escenarios;
  2. `ORD-N01 New title`, en su sitio y con el mismo cuerpo;
  3. `ORD-I01 Ledger entry per refund`, en su sitio y conservando `**aval**: characterization`;
  4. `ORD-F02 Refund capped at order total`, añadido **al final**.

  `ORD-A01` desaparece. Todos los IDs siguen abriendo su nombre y `validate --all --strict` pasa.

### 3. Casos límite

Cada caso se aplicó sobre la spec resultante del paso 2. Los ficheros están en `negative/`.

| Caso | `validate --strict --json` | `archive -y` |
|---|---|---|
| MODIFIED con otra capitalización (`ORD-F01 refund is idempotent`) | **`valid: true`**, exit 0, issue `INFO`: *"Archive would refuse this delta: refunds MODIFIED failed for header … - not found"* | Rechazado, exit 1, *"Aborted. No files were changed."* |
| RENAMED que cambia el ID (`ORD-N01 New title` → `ORD-N09 New title`) | `valid: true` | **Aplicado:** la spec pasa a tener `ORD-N09` |
| ID entre corchetes (`[ORD-F03] Refund reason required`) | `valid: true` | **Aplicado** tal cual |
| ADDED con un ID que ya existe (`ORD-F01 Refund needs approval above limit`) | `valid: true` | **Aplicado:** dos requirements `ORD-F01` en la misma spec |
| MODIFIED con `##` de cierre (`ORD-F02 … total ##`) | `valid: true` | Aplicado: casa con `ORD-F02`, pero **escribe la cabecera con ` ##`** |
| MODIFIED de `ORD-I01` sin la línea `**aval**` | `valid: true` | Aplicado: **la marca se pierde sin aviso** |
| MODIFIED que omite escenarios | `ERROR` *"omits scenario(s) the current spec still has"*, exit 1 | Rechazado |
| RENAMED y MODIFIED con el nombre viejo | `ERROR` *"MODIFIED references old name from RENAMED"*, exit 1 | Rechazado |
| RENAMED y MODIFIED con el nombre nuevo | `valid: true` | Aplicado |

OpenSpec no sabe nada de IDs: los trata como texto dentro del nombre. Protege la identidad por nombre exacto, pero acepta cambiar un ID, duplicarlo o escribirlo mal.

### 4. Esquema propio `aval`

- `openspec schema fork spec-driven aval --json` copia el esquema del paquete a `openspec/schemas/aval/`, con `schema.yaml` y `templates/`. Todos los comandos `schema` avisan por stderr: *"Schema commands are experimental and may change."*
- En el código de 1.13.1, el esquema de un artefacto solo admite `id`, `generates`, `description`, `template`, `instruction` y `requires`. **No existe el concepto de artefacto opcional:** "opcional" solo puede significar que el artefacto no está en `apply.requires` y que ningún artefacto requerido depende de él.
- Se añadieron dos artefactos:
  - `premortem`, que genera `premortem.md` y depende de `proposal`;
  - `trace`, que genera `trace.yaml` y depende de `specs`.

  `apply.requires` sigue siendo `[ tasks ]`. `openspec schema validate aval --json` devuelve `"valid": true`.
- `openspec new change x --schema aval` escribe `schema: aval` en `.openspec.yaml`. Con proposal, specs, design y tasks, pero sin premortem ni trace:
  - `status --change x --json` muestra `"applyRequires": ["tasks"]` y los seis artefactos, con `premortem` y `trace` en `ready`;
  - `instructions apply --change x --json` devuelve `"state": "ready"` sin `missingPrerequisites`: tier 0 no paga ningún artefacto extra;
  - **pero** `isPlanningComplete` e `isComplete` siguen en `false`, porque cuentan todos los artefactos, y `nextSteps` propone *"Run openspec instructions premortem …"*.
- El workflow `continue` de OpenSpec se detiene con `isPlanningComplete` y, si no, crea el primer artefacto en `ready`: empujaría a escribir premortem y trace. Los workflows `ff` y `propose` solo cubren `applyRequires` y su cierre transitivo por `requires` (*"Leave artifacts outside that set alone"*), así que no los tocan.
- `archive` mueve `premortem.md` y `trace.yaml` con el resto del change a `changes/archive/<fecha>-<change>/`.

## Decisión

**GO.** El ID al principio del nombre del requirement sobrevive a `validate --strict` y a `archive` en las cuatro operaciones. Mantiene la posición y los escenarios, y la marca `**aval**` sobrevive siempre que el bloque MODIFIED la incluya.

Se descarta la alternativa de una línea `**ID**: ORD-F01`. Sería metadato, igual que `**aval**`, y el caso `modified-drops-marker` demuestra que un MODIFIED que la omite la borra sin aviso. La cabecera, en cambio, es la clave de casado de OpenSpec: un ID mal copiado no casa y archive rechaza el cambio, y cambiar un ID exige un RENAMED explícito que aval puede detectar.

## Reglas que aval debe imponer

OpenSpec no aplica ninguna de estas reglas, o no lo hace de forma bloqueante.

1. **Formato.** El nombre del requirement cumple `^[A-Z]{2,6}-[FNISAO][0-9]{2} \S`, tiene menos de 50 caracteres y no lleva backticks, `#` al final ni corchetes alrededor del ID. Un requirement sin ID válido es un error.
2. **Unicidad.** Un ID aparece una sola vez en todas las specs de `openspec/specs/` y en los deltas de los changes activos. Un ADDED con un ID existente es un error. Un ID retirado no se reutiliza.
3. **RENAMED conserva el ID.** Solo cambia el título. Si cambia el ID, aval lo trata como REMOVED del viejo más ADDED del nuevo y lo reporta: para cambiar un ID hay que escribir REMOVED y ADDED explícitos.
4. **Un `INFO` que empieza por `Archive would refuse this delta` es un fallo.** `validate --strict` devuelve `valid: true` y exit 0 aunque archive vaya a rechazar el delta.
5. **Normalizar la cabecera igual que OpenSpec:** trim, quitar la secuencia de `#` de cierre y comparar distinguiendo mayúsculas. Además, el lint rechaza el `#` de cierre, porque archive lo copia a la spec principal.
6. **La marca de caracterización no se pierde en silencio.** Si un MODIFIED toca un requirement con `**aval**: characterization` y el bloque no la trae, aval lo reporta como cambio de clase que hay que confirmar.
7. **La identidad sale del markdown, nunca de `show --json`,** porque solo RENAMED expone nombres. El orden no significa nada, ya que ADDED añade al final.
8. **Test ↔ obligación por ID.** El segmento del subtest debe casar con `^<ID>(_|$)`. El título del test puede diferir del de la spec.

## Consecuencias

- Los ficheros de `internal/openspec/testdata/spike/` son los fixtures de las pruebas diferenciales del parser de M1. Para cada spec y delta, aval debe extraer los mismos IDs, nombres y escenarios que OpenSpec valida y archiva.
- **Esquema `aval`:** se adopta el fork de `spec-driven` con `premortem` y `trace` fuera de `apply.requires`, con tres condiciones:
  - aval **nunca** decide con `isPlanningComplete` ni con `isComplete`. Usa `applyRequires` y su cierre por `requires`, más el `state` de `instructions apply`;
  - las skills de aval le indican al agente que ignore premortem y trace en `nextSteps` salvo que el tier los exija;
  - el fork copia unas 230 líneas de instrucciones de upstream, así que se fija la versión de OpenSpec y, al subirla, se vuelve a hacer el fork y se revisa el diff.
- `openspec status` solo mira si los ficheros existen: `tasks` sale en `done` aunque falte `design`. aval no debe leer `done` como que las dependencias están satisfechas.
