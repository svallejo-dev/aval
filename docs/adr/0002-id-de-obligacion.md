# ADR-0002 — ID de obligación en el nombre del requirement

- **Estado:** aceptada
- **Fecha:** 2026-09-21
- **Origen:** spike S1. La evidencia reproducible está en [`internal/openspec/testdata/spike/`](../../internal/openspec/testdata/spike/README.md).

## Contexto

aval convierte cada obligación de la spec en una fila `obligación → verificador → evidencia → gate`. Para eso necesita un ID estable que viaje de la spec de OpenSpec al test de Go y a la evidencia.

En OpenSpec, la identidad de un requirement es su nombre, el texto de la cabecera `### Requirement:`. Archive casa los requirements por ese nombre normalizado y distingue mayúsculas; el reconocimiento exacto de cabeceras y la normalización están en la regla 5. `openspec show --json` no devuelve los nombres de requirements ni de escenarios, solo el cuerpo. Por eso aval parsea el markdown por su cuenta y usa la CLI solo para `validate`, `new change` y `status`.

La pregunta del spike: **¿un ID al principio del nombre sobrevive a `validate --strict` y a `archive` con ADDED, MODIFIED, RENAMED y REMOVED?**

## Convención

Las expresiones regulares son las del contrato v1, ya implementado en `internal/obligation` (ADR-0004).

- **Formato:** `<CTX>-<K><NN>`.
  - `CTX` es el contexto: de 2 a 10 caracteres en mayúsculas o dígitos, empezando por letra (`ORD`).
  - `K` es la clase de obligación, una de `F N I S A O`: F, algo que debe pasar; N, algo que nunca debe pasar; I, un invariante; S, un SLO; A, una suposición; O, una pregunta abierta. La política de cada clase en el gate está en ADR-0004.
  - `NN` tiene de 2 a 4 dígitos.
  - ID completo: `^[A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4}$`.
  - Dos IDs son la misma obligación solo si sus cadenas son iguales: `ORD-F01` y `ORD-F001` son obligaciones distintas.
- **En OpenSpec:** el ID abre el nombre del requirement, seguido de blancos y un título.

  ```markdown
  ### Requirement: ORD-F01 Refund is idempotent
  ```

  El nombre, ya normalizado, cumple `^ID[ \t]+(\S(?:.*\S)?)[ \t]*$`, donde `ID` es el patrón anterior sin anclas y el grupo es el título. Las demás restricciones están en la regla 1.
- **En Go:** el subtest empieza por el mismo ID: `t.Run("ORD-F01 Refund is idempotent", …)`.
  - test2json lo emite como `TestRefund/ORD-F01_Refund_is_idempotent`, porque Go cambia los espacios por `_` y el ID no tiene ninguno.
  - aval casa cada segmento del nombre con `^([A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4})(?:_|$|#[0-9]+$)`, no con el título. El sufijo `#NN` es el que añade Go a los subtests con el nombre repetido.
  - `go test -run '^TestRefund$/^ORD-F01([_#]|$)'` ejecuta solo esa obligación, incluidos un subtest llamado solo `ORD-F01` y sus duplicados `ORD-F01#01`.
- **Caracterización:** si un requirement describe comportamiento que ya existía, lleva la línea de metadatos `**aval**: characterization` en su cuerpo.

## Evidencia

Entorno: `@fission-ai/openspec@1.13.1` con `npx -y`, Node v26.7.0, `OPENSPEC_TELEMETRY=0 OPENSPEC_NO_UPDATE_CHECK=1`, Go 1.27.1 y repos temporales. Los IDs de los fixtures usan la clase que corresponde a cada requirement.

### 1. Spec base

- `openspec init --tools none --no-animation .` funciona sin preguntar nada y crea `openspec/{config.yaml,specs/,changes/archive/}` con `schema: spec-driven`.
- Se escribió `openspec/specs/refunds/spec.md` con cuatro requirements:
  - `ORD-F01 Refund is idempotent`
  - `ORD-S01 Fast refund answer`, un SLO de latencia
  - `ORD-I01 Ledger entry per refund`, con `**aval**: characterization`
  - `ORD-A01 Gateway confirms refunds at once`, una suposición
- `openspec validate --all --strict --json` devuelve `"valid": true, "issues": []`.
- `openspec show refunds --type spec --json` no incluye nombres: cada requirement trae solo `text` y `scenarios[].rawText`. La línea `**aval**: characterization` sigue en el fichero, pero se elimina del `text` (`"text": "The system SHALL write exactly one ledger entry…"`). `-r` selecciona por índice empezando en 1, no por nombre.

### 2. Change completo y archive

- `openspec new change add-refund-limits --json` crea solo `.openspec.yaml`. Después se escribieron:
  - `proposal.md`, con `## Why` y `## What Changes`;
  - `tasks.md`;
  - el delta `specs/refunds/spec.md`:
    - ADDED `ORD-N01 Refund never exceeds order total`;
    - MODIFIED `ORD-F01`, que conserva sus dos escenarios y añade uno;
    - MODIFIED `ORD-I01`, que conserva la marca;
    - REMOVED `ORD-A01`, con `**Reason**` y `**Migration**`, porque la suposición dejó de cumplirse;
    - RENAMED de `ORD-S01 Fast refund answer` a `ORD-S01 Refund latency under 300 ms`.
- `openspec validate add-refund-limits --type change --strict --json` devuelve `"valid": true, "issues": []`.
- `openspec show … --json --deltas-only`: solo RENAMED lleva nombres (`"rename": {"from": "ORD-S01 Fast refund answer", "to": "ORD-S01 Refund latency under 300 ms"}`). ADDED, MODIFIED y REMOVED traen solo el cuerpo.
- `openspec archive add-refund-limits -y` sale con 0 y muestra `+ 1 added`, `~ 2 modified`, `- 1 removed` y `→ 1 renamed`.
- La spec resultante (`refunds/spec-after.md`) queda así:
  1. `ORD-F01 Refund is idempotent`, en su sitio y con sus 3 escenarios;
  2. `ORD-S01 Refund latency under 300 ms`, en su sitio y con el mismo cuerpo;
  3. `ORD-I01 Ledger entry per refund`, en su sitio y conservando `**aval**: characterization`;
  4. `ORD-N01 Refund never exceeds order total`, añadido **al final**.

  `ORD-A01` desaparece. Todos los IDs siguen abriendo su nombre y `validate --all --strict` pasa.

### 3. Casos límite

Cada caso se aplicó sobre la spec resultante del paso 2. Los ficheros están en `negative/` y `positive/`, y la columna "aval" es lo que aval debe hacer con cada uno.

| Caso | `validate --strict --json` | `archive -y` | aval |
|---|---|---|---|
| MODIFIED con otra capitalización (`ORD-F01 refund is idempotent`) | **`valid: true`**, exit 0, issue `INFO`: *"Archive would refuse this delta: refunds MODIFIED failed for header … - not found"* | Rechazado, exit 1, *"Aborted. No files were changed."* | error |
| `### Notes` entre el cuerpo y los escenarios de un ADDED | **`valid: true`**, exit 0, issue `INFO`: *"Header "### Notes" in ADDED Requirements is not a "### Requirement:" header and is ignored by validation…"* | Rechazado, exit 1: *"Requirement must have at least one scenario"* en la spec reconstruida | error |
| RENAMED que cambia el ID (`ORD-S01 …` → `ORD-S09 …`) | `valid: true` | **Aplicado:** la spec pasa a tener `ORD-S09` | error |
| ID entre corchetes (`[ORD-F04] Refund reason required`) | `valid: true` | **Aplicado** tal cual | error |
| ADDED con un ID que ya existe (`ORD-F01 Refund needs approval above limit`) | `valid: true` | **Aplicado:** dos requirements `ORD-F01` en la misma spec | error |
| MODIFIED con `##` de cierre (`ORD-N01 … total ##`) | `valid: true` | Aplicado: casa con `ORD-N01`, pero **escribe la cabecera con ` ##`** | error |
| Cabecera laxa (`###requirement:ORD-F06 Refund currency matches order`) | `valid: true` | Aplicado y **escrito tal cual**. Después, `show` sobre la spec principal cuenta 4 requirements, no 5, y el escenario de ORD-F06 aparece dentro de ORD-N01 | error |
| MODIFIED de `ORD-I01` sin la línea `**aval**` | `valid: true` | Aplicado: **la marca se pierde sin aviso** | warn |
| MODIFIED que omite escenarios | `ERROR` *"omits scenario(s) the current spec still has"*, exit 1 | Rechazado | error |
| RENAMED y MODIFIED con el nombre viejo | `ERROR` *"MODIFIED references old name from RENAMED"*, exit 1 | Rechazado | error |
| RENAMED y MODIFIED con el nombre nuevo (control positivo) | `valid: true` | Aplicado | accept |
| `### Requirement: ORD-F99 …` dentro de un bloque de código (control positivo) | `valid: true` | Aplicado: la línea sigue siendo cuerpo y `show` cuenta 5 requirements | accept; ORD-F99 no es una obligación |
| `ORD-F07 Refund SDK for C#` (control positivo) | `valid: true` | Aplicado: el nombre conserva su `#` | accept |

OpenSpec no sabe nada de IDs: los trata como texto dentro del nombre. Protege la identidad por nombre exacto, pero acepta cambiar un ID, duplicarlo o escribirlo mal. Además, sus dos lectores no coinciden: el de deltas y archive reconoce `###requirement:`, y el de specs principales no.

### 4. Esquema propio `aval` (opción descartada)

- `openspec schema fork spec-driven aval --json` copia el esquema del paquete a `openspec/schemas/aval/`, con `schema.yaml` y `templates/`. Todos los comandos `schema` avisan por stderr: *"Schema commands are experimental and may change."*
- En el código de 1.13.1, el esquema de un artefacto solo admite `id`, `generates`, `description`, `template`, `instruction` y `requires`. **No existe el concepto de artefacto opcional:** "opcional" solo puede significar que el artefacto no está en `apply.requires` y que ningún artefacto requerido depende de él.
- Se añadieron dos artefactos:
  - `premortem`, que genera `premortem.md` y depende de `proposal`;
  - `trace`, que genera `trace.yaml` y depende de `specs`.

  `apply.requires` sigue siendo `[ tasks ]`. `openspec schema validate aval --json` devuelve `"valid": true`.
- `openspec new change x --schema aval` escribe `schema: aval` en `.openspec.yaml`. Con proposal, specs, design y tasks, pero sin premortem ni trace:
  - `status --change x --json` muestra `"applyRequires": ["tasks"]` y los seis artefactos, con `premortem` y `trace` en `ready`;
  - `instructions apply --change x --json` devuelve `"state": "ready"` sin `missingPrerequisites`;
  - **pero** `isPlanningComplete` e `isComplete` siguen en `false`, porque cuentan todos los artefactos, y `nextSteps` propone *"Run openspec instructions premortem …"*.
- El workflow `continue` de OpenSpec se detiene con `isPlanningComplete` y, si no, crea el primer artefacto en `ready`: empujaría a escribir premortem y trace. Los workflows `ff` y `propose` solo cubren `applyRequires` y su cierre transitivo por `requires` (*"Leave artifacts outside that set alone"*), así que no los tocan.
- El fork es una copia completa: unas 230 líneas de instrucciones de upstream que habría que volver a copiar y revisar en cada subida de OpenSpec.

### 5. Ficheros de aval en un change con el esquema `spec-driven`

Se repitió el change del paso 2 en un repo nuevo, con el esquema integrado `spec-driven`, añadiendo tres ficheros que OpenSpec no conoce:

- `aval.yaml`, el manifiesto del change de ADR-0004;
- `premortem.md`;
- `trace.yaml`.

Los ficheros están en `extra-files/`. Resultados:

- `validate add-refund-limits --type change --strict --json` sale con exit 0 y devuelve `"valid": true, "issues": []`.
- `status --change add-refund-limits --json` ignora los tres ficheros: no aparecen en `artifacts` ni en `artifactPaths`.
- `archive add-refund-limits -y` sale con 0 y muestra los mismos totales. La spec resultante es idéntica byte a byte a `refunds/spec-after.md`.
- Los tres ficheros se mueven a `changes/archive/<fecha>-add-refund-limits/`, y `validate --archived --json` pasa.

## Decisión

**GO.** El ID al principio del nombre del requirement sobrevive a `validate --strict` y a `archive` en las cuatro operaciones. Mantiene la posición y los escenarios, y la marca `**aval**` sobrevive siempre que el bloque MODIFIED la incluya.

Se descarta la alternativa de una línea `**ID**: ORD-F01`. Sería metadato, igual que `**aval**`, y el caso `modified-drops-marker` demuestra que un MODIFIED que la omite la borra sin aviso. La cabecera, en cambio, es la clave de casado de OpenSpec: un ID mal copiado no casa y archive rechaza el cambio, y cambiar un ID exige un RENAMED explícito que aval puede detectar.

**La v0 no usa un esquema propio de OpenSpec.** aval usa el esquema integrado `spec-driven` y gestiona sus propios ficheros dentro del change:

- `aval.yaml`, el manifiesto del change;
- `premortem.md`, solo si el tier del change lo exige (tier 2 o superior);
- `trace.yaml`, solo si el tier del change lo exige.

Hay dos motivos:

- OpenSpec no tiene artefactos opcionales. Con premortem y trace en el esquema, `isPlanningComplete` sigue en `false` y `nextSteps` empuja a los agentes a escribirlos, también en tier 0 (paso 4).
- El fork, de unas 230 líneas, habría que rehacerlo en cada subida de OpenSpec.

El paso 5 demuestra que OpenSpec tolera esos ficheros: no rompen `validate --strict` ni `archive`, y viajan con el change al archivo.

**El change se archiva en el mismo PR que el código.** Así `main` nunca tiene código sin su spec actualizada, ni al revés. Como `archive` mueve el directorio entero, esos ficheros viven en dos sitios según el momento:

- mientras el change está activo, en `openspec/changes/<id>/`;
- después del archive, en `openspec/changes/archive/<fecha>-<id>/`.

aval busca los ficheros de un change en los dos.

## Reglas que aval debe imponer

OpenSpec no aplica ninguna de estas reglas, o no lo hace de forma bloqueante.

1. **Formato.**
   - La cabecera es exactamente `### Requirement: ` (tres `#`, un espacio, `Requirement:` con mayúscula y un espacio). Cualquier otra forma que OpenSpec acepte es un error: `###requirement:ORD-F06 …` pasa `validate` y archive, pero el lector de specs principales no la ve y la obligación desaparece de `show`.
   - El nombre normalizado (regla 5) cumple la regex del nombre con un ID v1. Un requirement sin ID válido es un error: `[ORD-F04] …` no casa.
   - Sin backticks, porque RENAMED los usa como delimitador. Sin `#` de cierre separado por blancos, porque archive lo copia a la spec principal. Las dos cosas son error.
   - **Longitud:** si el nombre completo, con el ID incluido, tiene 50 caracteres Unicode o más, es un **aviso**, no un error. La guía de convenciones del propio OpenSpec (`openspec/specs/openspec-conventions/spec.md` en v1.13.1) pide *"keep requirement names descriptive and under 50 characters"*, pero su validador no lo comprueba.
2. **Definición y referencia.**
   - Un ID se **define** una sola vez: en una spec de `openspec/specs/` o en un bloque ADDED de un change activo. Una segunda definición es un error, también entre specs distintas y entre changes activos.
   - MODIFIED, REMOVED y el `FROM` de RENAMED **refieren** a un ID ya definido. Un ID que no existe es un error.
   - Los **IDs retirados** son los que aparecen en bloques REMOVED de `openspec/changes/archive/*/specs/`. Nunca se reutilizan: un ADDED con un ID retirado es un error.
3. **RENAMED conserva el ID.** Solo cambia el título. Si cambia el ID, aval lo trata como REMOVED del viejo más ADDED del nuevo y lo reporta como error: para cambiar un ID hay que escribir REMOVED y ADDED explícitos.
4. **Todo `INFO` de `validate --strict` es un fallo.** OpenSpec devuelve `valid: true` y exit 0 con issues que anuncian que archive fallará o que parte del delta se ignora, por ejemplo *"Archive would refuse this delta"* o *"… is not a "### Requirement:" header and is ignored by validation"*. Además, aval prohíbe cualquier `###` que no sea `### Requirement:` dentro de las secciones de requirements: `## Requirements` en las specs y las cuatro secciones de los deltas.
5. **El parser de aval replica el de OpenSpec 1.13.1** (lector de deltas y archive, `requirement-blocks.js`):
   - reconoce las cabeceras con `/^###\s*Requirement:\s*(.+)\s*$/i`, así que también ve `###requirement:ORD-F06 …`, igual que archive, y la regla 1 la reporta;
   - normaliza el nombre con `name.replace(/[ \t]+#+[ \t]*$/, '').trim()`: solo se quitan los `#` de cierre separados por blancos, así que `ORD-N01 … total ##` queda en `ORD-N01 … total` y `ORD-F07 Refund SDK for C#` conserva su `#`;
   - compara nombres distinguiendo mayúsculas;
   - ignora las líneas dentro de bloques de código: `### Requirement: ORD-F99 …` dentro de un bloque no es un requirement ni una obligación.

   Hay que normalizar antes de aplicar la regex del nombre, porque `ORD-N01 … total ##` también casa sin normalizar, con ` ##` dentro del título.
6. **La marca de caracterización no se pierde en silencio.** Si un MODIFIED toca un requirement con `**aval**: characterization` y el bloque no la trae, aval avisa (warn): puede ser intencionado, porque el comportamiento ya no es el heredado, pero tiene que verse.
7. **La identidad sale del markdown, nunca de `show --json`,** porque solo RENAMED expone nombres. El orden no significa nada, ya que ADDED añade al final.
8. **Test ↔ obligación por ID.** El segmento del subtest debe casar con `^([A-Z][A-Z0-9]{1,9}-[FNISAO][0-9]{2,4})(?:_|$|#[0-9]+$)`. El título del test puede diferir del de la spec.

## Consecuencias

- Los ficheros de `internal/openspec/testdata/spike/` son los fixtures de las pruebas diferenciales del parser de M1 y un corpus semántico pequeño. Para cada spec y delta, aval debe extraer los mismos IDs, nombres y escenarios que OpenSpec valida y archiva, y dar el veredicto de la columna "aval".
- **Sin esquema propio:**
  - `aval feat new` crea el change con `openspec new change <id>`, que usa `spec-driven`, y escribe al lado `aval.yaml` y, según el tier, `premortem.md` y `trace.yaml`;
  - aval comprueba por su cuenta que existen los ficheros que exige el tier, en `openspec/changes/<id>/` o en `openspec/changes/archive/<fecha>-<id>/`, porque OpenSpec ni los ve;
  - `schemas/aval/` y `changes/x/` se quedan en los fixtures solo como referencia de la opción descartada.
- aval **nunca** decide con `isPlanningComplete` ni con `isComplete`. Usa `applyRequires` y su cierre por `requires`, más el `state` de `instructions apply`.
- `openspec status` solo mira si los ficheros existen: `tasks` sale en `done` aunque falte `design`. aval no debe leer `done` como que las dependencias están satisfechas.
