# ADR-0002 — ID de obligación en el nombre del requirement

- **Estado:** aceptada
- **Fecha:** 2026-09-21
- **Origen:** spike S1. La evidencia reproducible está en [`internal/openspec/testdata/spike/`](../../internal/openspec/testdata/spike/README.md).

## Contexto

aval convierte cada obligación de la spec en una fila `obligación → verificador → evidencia → gate`. Para eso necesita un ID estable que viaje de la spec de OpenSpec al test de Go y a la evidencia.

En OpenSpec, la identidad de un requirement es el texto que sigue a `### Requirement:`. Archive casa los requirements por esa cabecera normalizada (trim y sin `#` de cierre), distinguiendo mayúsculas. `openspec show --json` no devuelve los nombres de requirements ni de escenarios, solo el cuerpo. Por eso aval parsea el markdown por su cuenta y usa la CLI solo para `validate`, `new change` y `status`.

La pregunta del spike: **¿un ID al principio del nombre sobrevive a `validate --strict` y a `archive` con ADDED, MODIFIED, RENAMED y REMOVED?**

## Convención

Las expresiones regulares son las del contrato v1, ya implementado en `internal/obligation` (ADR-0004).

- **Formato:** `<CTX>-<K><NN>`.
  - `CTX` es el contexto: de 2 a 10 caracteres en mayúsculas o dígitos, empezando por letra (`ORD`).
  - `K` es la clase de obligación, una de `F N I S A O`. Su política en el gate está en ADR-0004.
  - `NN` tiene de 2 a 4 dígitos.
  - ID completo: `^[A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4}$`.
  - Dos IDs son la misma obligación solo si sus cadenas son iguales: `ORD-F01` y `ORD-F001` son obligaciones distintas.
- **En OpenSpec:** el ID abre el nombre del requirement, seguido de blancos y un título.

  ```markdown
  ### Requirement: ORD-F01 Refund is idempotent
  ```

  El nombre cumple `^ID[ \t]+(\S(?:.*\S)?)[ \t]*$`, donde `ID` es el patrón anterior sin anclas y el grupo es el título. Tiene menos de 50 caracteres, sin backticks (RENAMED los usa como delimitador) y sin `#` al final.
- **En Go:** el subtest empieza por el mismo ID: `t.Run("ORD-F01 Refund is idempotent", …)`. test2json lo emite como `TestRefund/ORD-F01_Refund_is_idempotent`, porque Go cambia los espacios por `_` y el ID no tiene ninguno. aval casa cada segmento del nombre con `^([A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4})(?:_|$|#[0-9]+$)`, no con el título. El sufijo `#NN` es el que añade Go a los subtests con el nombre repetido. `go test -run 'TestRefund/^ORD-F01_'` ejecuta solo esa obligación.
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

### 4. Esquema propio `aval` (opción descartada)

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
- El fork es una copia completa: unas 230 líneas de instrucciones de upstream que habría que volver a copiar y revisar en cada subida de OpenSpec.

### 5. Ficheros de aval en un change con el esquema `spec-driven`

Se repitió el change del paso 2 en un repo nuevo, con el esquema integrado `spec-driven`, añadiendo tres ficheros que OpenSpec no conoce:

- `aval.yaml`, el manifiesto del change de ADR-0004;
- `premortem.md`;
- `trace.yaml`.

Los ficheros están en `extra-files/`. Resultados:

- `validate add-refund-limits --type change --strict --json` sale con exit 0 y devuelve `"valid": true, "issues": []`. `validate --all --strict` también pasa.
- `status --change add-refund-limits --json` ignora los tres ficheros: no aparecen en `artifacts` ni en `artifactPaths`. `openspec list --json` marca el change como `complete`.
- `archive add-refund-limits -y` sale con 0 y muestra los mismos totales (`+ 1 added`, `~ 2 modified`, `- 1 removed`, `→ 1 renamed`). La spec resultante es idéntica byte a byte a `refunds/spec-after.md`.
- Los tres ficheros se mueven a `changes/archive/2026-09-21-add-refund-limits/`, y `validate --archived --json` pasa.

## Decisión

**GO.** El ID al principio del nombre del requirement sobrevive a `validate --strict` y a `archive` en las cuatro operaciones. Mantiene la posición y los escenarios, y la marca `**aval**` sobrevive siempre que el bloque MODIFIED la incluya.

Se descarta la alternativa de una línea `**ID**: ORD-F01`. Sería metadato, igual que `**aval**`, y el caso `modified-drops-marker` demuestra que un MODIFIED que la omite la borra sin aviso. La cabecera, en cambio, es la clave de casado de OpenSpec: un ID mal copiado no casa y archive rechaza el cambio, y cambiar un ID exige un RENAMED explícito que aval puede detectar.

**La v0 no usa un esquema propio de OpenSpec.** aval usa el esquema integrado `spec-driven` y gestiona sus propios ficheros dentro de `openspec/changes/<id>/`:

- `aval.yaml`, el manifiesto del change;
- `premortem.md`, solo si el tier del change lo exige (tier 2 o superior);
- `trace.yaml`, solo si el tier del change lo exige.

Hay dos motivos:

- OpenSpec no tiene artefactos opcionales. Con premortem y trace en el esquema, `isPlanningComplete` sigue en `false` y `nextSteps` empuja a los agentes a escribirlos, también en tier 0 (paso 4).
- El fork, de unas 230 líneas, habría que rehacerlo en cada subida de OpenSpec.

El paso 5 demuestra que OpenSpec tolera esos ficheros: no rompen `validate --strict` ni `archive`, y viajan con el change al archivo.

## Reglas que aval debe imponer

OpenSpec no aplica ninguna de estas reglas, o no lo hace de forma bloqueante.

1. **Formato.** Tras normalizar la cabecera (regla 5), el nombre del requirement cumple `^ID[ \t]+(\S(?:.*\S)?)[ \t]*$` con el ID del contrato v1. Tiene menos de 50 caracteres y no lleva backticks, `#` al final ni corchetes alrededor del ID. Un requirement sin ID válido es un error: `[ORD-F03] …` no casa.
2. **Unicidad.** Un ID aparece una sola vez en todas las specs de `openspec/specs/` y en los deltas de los changes activos. Un ADDED con un ID existente es un error. Un ID retirado no se reutiliza.
3. **RENAMED conserva el ID.** Solo cambia el título. Si cambia el ID, aval lo trata como REMOVED del viejo más ADDED del nuevo y lo reporta: para cambiar un ID hay que escribir REMOVED y ADDED explícitos.
4. **Un `INFO` que empieza por `Archive would refuse this delta` es un fallo.** `validate --strict` devuelve `valid: true` y exit 0 aunque archive vaya a rechazar el delta.
5. **Normalizar la cabecera igual que OpenSpec:** trim, quitar la secuencia de `#` de cierre y comparar distinguiendo mayúsculas. Hay que normalizar antes de aplicar la regex del nombre, porque `ORD-F02 Refund capped at order total ##` también casa sin normalizar, con ` ##` dentro del título. Además, el lint rechaza el `#` de cierre, porque archive lo copia a la spec principal.
6. **La marca de caracterización no se pierde en silencio.** Si un MODIFIED toca un requirement con `**aval**: characterization` y el bloque no la trae, aval lo reporta como cambio de clase que hay que confirmar.
7. **La identidad sale del markdown, nunca de `show --json`,** porque solo RENAMED expone nombres. El orden no significa nada, ya que ADDED añade al final.
8. **Test ↔ obligación por ID.** El segmento del subtest debe casar con `^([A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4})(?:_|$|#[0-9]+$)`. El título del test puede diferir del de la spec.

## Consecuencias

- Los ficheros de `internal/openspec/testdata/spike/` son los fixtures de las pruebas diferenciales del parser de M1. Para cada spec y delta, aval debe extraer los mismos IDs, nombres y escenarios que OpenSpec valida y archiva.
- **Sin esquema propio:**
  - `aval feat new` crea el change con `openspec new change <id>`, que usa `spec-driven`, y escribe al lado `aval.yaml` y, según el tier, `premortem.md` y `trace.yaml`;
  - aval comprueba por su cuenta que existen los ficheros que exige el tier, porque OpenSpec ni los ve;
  - `schemas/aval/` y `changes/x/` se quedan en los fixtures solo como referencia de la opción descartada.
- aval **nunca** decide con `isPlanningComplete` ni con `isComplete`. Usa `applyRequires` y su cierre por `requires`, más el `state` de `instructions apply`.
- `openspec status` solo mira si los ficheros existen: `tasks` sale en `done` aunque falte `design`. aval no debe leer `done` como que las dependencias están satisfechas.
