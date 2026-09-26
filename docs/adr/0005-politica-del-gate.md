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

Los formatos base están en ADR-0004. Este ADR fija el **comportamiento** y los cambios de contrato que ese comportamiento exige (bundle v3 y baseline v1).

## Modelo de amenazas

El gate defiende de un **agente** (o una persona) que quiere fusionar un cambio sin la evidencia que la política exige. Puede editar cualquier fichero del PR y ejecuta código en el job de CI a través de los tests.

**Qué no puede hacer, porque aval no confía en nada del head para decidir:**
- cambiar la política;
- elegir qué política se le aplica: la base de confianza es el tip de la rama por defecto, no la rama base del PR ni el merge-base (§1);
- cambiar el tier por debajo del mínimo;
- falsificar aprobaciones;
- alterar lo que aval lee de las dos bases.

**Qué queda fuera de aval y exige configuración de GitHub:** un workflow de `pull_request` ejecuta la definición del workflow **del propio PR**. Un PR podría, por tanto, editar el job del gate. La integridad del gate depende de proteger `.github/**` con CODEOWNERS y **revisión obligatoria de code owner** en el ruleset.

**El montaje tiene que proteger la rama por defecto** con un ruleset: sin escritura directa y con PR obligatorio. De ahí sale toda la política (§1), y aval no lo comprueba ni lo supone. Si la rama por defecto acepta pushes directos, la base de confianza no es de fiar y con ella se cae el resto.

**Ese ruleset tiene que apuntar a `~DEFAULT_BRANCH`, no a una rama por su nombre.** Uno que nombre `main` deja de cubrir la base de confianza en cuanto alguien cambia cuál es la rama por defecto: la base se mueve a una rama sin proteger y aval no ve `tamper`, porque no se editó ningún fichero. Cambiar cuál es la rama por defecto **es** un cambio de política; con `~DEFAULT_BRANCH` la protección lo sigue.

aval informa además como `tamper` (§4) de cualquier edición de `.github/**`, un `CODEOWNERS` de cualquiera de las tres ubicaciones que reconoce GitHub, `.golangci.yml`, cualquier `.gitattributes`, el `aval.yaml` raíz y `.aval/baseline.json`. La protección fuerte, sin embargo, es la de GitHub.

**Límites conocidos de la v0:**
- **La excepción es global:** una concedida por una regresión inestable también rebaja un `skip_added` del mismo PR. Los motivos quedan en el bundle y en el resumen; una excepción acotada por código (`aval:override tamper …`) queda para después de la v0.
- **Credenciales:** un agente que opera con las credenciales de una persona actúa como esa persona. Los agentes deben usar una cuenta o token propio, sin permisos de revisión.
- **`weak` se puede provocar:** un test que referencia cualquier símbolo nuevo no compila en la base del cambio. Por eso `weak` es un aviso y el resumen lo muestra al revisor humano, nunca un pase silencioso.
- **Fixtures fuera del paquete:** un test que lee un fichero nuevo de otro directorio (`../fixtures/in.txt`) falla en la base del cambio y da `strong`. Solo se degrada a `weak` lo que cae dentro de un paquete de la obligación; la convención es `testdata/`.
- **`openspec validate` sin política:** la versión de OpenSpec se fija en el `aval.yaml` de la base de confianza y la del head no es de fiar, así que sin política no se valida y el bundle lo declara en `notCollected`. El veredicto ya es `observe` por `no_base_policy`.
- **Git LFS:** el árbol de la base del cambio se materializa desde los objetos, así que de un fichero LFS recibe el puntero y no el contenido, y un test que lo lea fallaría en la base. Los repos del piloto no usan LFS.
- **Runner comprometido:** el código del PR se ejecuta en el mismo runner que el gate, y en los runners de GitHub tiene sudo sin contraseña. Código malicioso que ataque al propio runner (sustituir el binario de aval, alterar ficheros entre pasos) queda fuera de lo que aval puede defender en la v0. Por eso el gate verifica en el mismo proceso, y la mitigación de fondo es ejecutar los tests en un entorno aislado del gate.

## Decisión

### 1. Entradas

El gate trabaja con **dos bases distintas**, y confundirlas es un agujero, porque el autor del PR controla las dos referencias que tiene a mano:
- **elige dónde corta su rama,** así que el merge-base es influenciable. Cortando de un commit anterior a la adopción de aval, el merge-base no tiene `aval.yaml` y el gate concluiría que no hay política, sin editar nada y sin disparar `tamper`;
- **elige la rama base del PR,** que en un PR apilado es una rama sin proteger donde escribe él. Su tip tampoco puede sostener la política: el autor la escribiría.

Por eso la política no sale de ninguna de las dos, sino de la rama por defecto del repositorio.

| Base | Qué es | Para qué |
|---|---|---|
| **Base de confianza** | El tip de la **rama por defecto del repositorio**, sea cual sea la rama base del PR: `github.event.repository.default_branch` del payload del evento —resuelto con `GET /repos/{o}/{r}` solo si falta en el payload— y su tip en `origin/<default_branch>`; en local, el destino de `origin/HEAD`, luego `origin/main`, luego `main` | Todo lo que es política o confianza: `aval.yaml` (modo, `tierDefault`, `paths`, versión de OpenSpec), `CODEOWNERS`, `.aval/baseline.json`, `.golangci.yml`, el tier de un change que ya exista ahí, y una de las dos lecturas de specs y de declaraciones |
| **Base del cambio** | El merge-base de head con el `base.sha` del PR (`github.event.pull_request.base.sha`) | Todo lo que describe qué hizo **este** PR: diff, changes, scope, la otra lectura de specs y de declaraciones, la superposición de la falla-antes y `--new-from-merge-base` |

Usar la base del cambio para lo segundo es lo correcto: con un tip, los cambios de otras personas se atribuirían a este PR. Usar la rama por defecto para lo primero es lo correcto: es la única referencia del PR que el autor no puede elegir ni escribir sin pasar por otro gate.

**PRs apilados:** aval **no** exige que la rama base del PR descienda de la rama por defecto —exigirlo bloquearía apilamientos legítimos—. Siguen siendo seguros por dos razones: fusionar en una rama que no es la por defecto no llega a la rama por defecto sin otro PR, que pasa por el gate con esta misma base de confianza; y el lavado de hallazgos por la rama intermedia lo cierra la resta de `spec_rule` (§4), que solo descuenta lo que existe en **las dos** bases.

- **head** es `github.event.pull_request.head.sha`, y el checkout usa `ref:` head, no el merge commit.
- **Dentro de Actions, `--trust-base`, `--change-base` y `--head` se rechazan siempre** (exit 2): basta que `GITHUB_ACTIONS` esté puesto, haya o no un PR en el evento. No vale limitarlo a los eventos con PR: un job de `push` podría publicar un check run del mismo nombre sobre el SHA head con la base de confianza que eligiera, y el check obligatorio no distinguiría. En Actions manda el evento; sin PR en el evento, no hay nada que verificar. El límite de fondo sigue siendo que la definición del workflow viaja en el propio PR (ver el modelo de amenazas): eso lo cierra CODEOWNERS sobre `.github/**`, no aval.
- **Los flags son dos,** `--trust-base` y `--change-base`: `--base` ya no existe, porque no se sabría cuál de las dos nombra.
- **Rango degenerado:** si el merge-base no se puede calcular, o coincide con head, exit 2 nombrando lo que se intentó (§6). Nunca un `pass` silencioso. Lo mismo si no se puede resolver la rama por defecto o traer el `base.sha` (§8).
- **`no_base_policy` mira la base de confianza:** si ahí hay un `aval.yaml` raíz, no se dispara, sea cual sea la base del cambio.
- **El tier de un change** es el máximo entre el que declara head, el que tiene en la base del cambio y el que tiene en la base de confianza. Cortar la rama antes de una subida de tier no la deshace.
- **La base de confianza se mueve, y el veredicto es reproducible respecto a los SHAs que el bundle registra.** Es el tip en el momento de la ejecución: reejecutar el gate sobre el mismo head puede dar otro veredicto si la rama por defecto avanzó, y eso es lo que se quiere —la política que manda es siempre la vigente—. Por eso el bundle lleva `trustBase`, `changeBase`, `baseRef` y `head` (§7): con esos cuatro, la misma ejecución se repite igual, y son el rastro de auditoría de qué política se aplicó.
- **En local**, sin evento, `--trust-base` y `--change-base` sí valen; por defecto la base de confianza sale de `origin/HEAD` y la del cambio, del merge-base de head con ella. **La base de confianza local es consultiva:** en local no hay nada protegido —el remoto puede ser cualquiera y el árbol de trabajo es del propio agente—, así que el veredicto que manda es el del CI.

**Orden obligatorio:** todo lo que aval **lee** con `git` se lee **antes** de ejecutar cualquier código del PR, porque un test puede reescribir `.git/config` o `.git/info/*`. Incluye:
- de la base de confianza: política, `CODEOWNERS`, baseline, `.golangci.yml`, specs y declaraciones;
- de la base del cambio: specs, declaraciones y `.gitattributes`;
- del rango: diff, changes y scope (§3b).

Materializar el árbol de la base del cambio (§2.1) no es una lectura más: escribe ficheros y ocurre después de la ejecución de head, por eso se hace desde los objetos y nunca por el checkout de git.

**Git endurecido:** todo `git` corre con:
- `GIT_NO_REPLACE_OBJECTS=1` y `GIT_GRAFT_FILE` apuntando a `/dev/null`, para anular replace refs y grafts;
- `--attr-source` al árbol vacío, para que el `.gitattributes` del head no cambie diffs ni merges;
- `--ignore-submodules=none`, para que el `.gitmodules` del head no oculte cambios de submódulos;
- `-z` y `--end-of-options`.

**Nunca con `git archive`:** respeta `export-ignore` y `export-subst` del `.gitattributes` del árbol, y `--attr-source` **no** lo evita. Un `.gitattributes` en cualquiera de las dos bases podría así ocultar el `aval.yaml`, el baseline o las specs, y dejar el gate en `observe` creyendo que no hay política. Los ficheros de un árbol se leen con `git ls-tree` y `git cat-file --batch`, que no consultan atributos.

Requiere git ≥ 2.40; con uno más antiguo, exit 3.

**Excepción: la clave del estado para hooks** (`hook.CurrentKey`, §7) se calcula sin `--attr-source`, que costaría un `git hash-object` en serie en cada invocación del hook (presupuesto p95 < 50 ms).
- Cubre **los dos lados**: el hook que lee la clave y `aval verify` que la escribe. Si no calcularan `git diff HEAD` igual, un `.gitattributes` con `eol`, `text` o LFS haría que nunca coincidieran.
- Es aceptable porque la clave es consultiva y se calcula sobre el árbol de trabajo del propio agente: quien puede plantar un `.gitattributes` también puede escribir a mano el fichero de estado.
- El gate y todo lo demás que ejecuta `verify` nunca usan esta excepción.

| Entrada | De dónde sale |
|---|---|
| Política | El `aval.yaml` **raíz de la base de confianza**. Si no existe ahí, el gate corre en `observe` (`no_base_policy`) |
| Changes del PR | Directorios de `openspec/changes/<id>/` o `openspec/changes/archive/<fecha>-<id>/` que toca el diff `base del cambio..head` |
| Tier | **max**(`tierDefault` si hay algún commit `feat` o `mixed` o algún change; el `tier` de cada change en head; el `tier` de ese mismo change en la **base del cambio** si ya existía ahí; el `tier` de ese mismo change en la **base de confianza** si ya existía ahí). Un PR solo `dx`/`seam`/`other` y sin changes es **Tier 0**. El head puede subir el tier, nunca bajarlo |
| Obligaciones del delta | IDs **ADDED ∪ MODIFIED ∪ REMOVED** de esos changes. Solo las ADDED y MODIFIED (`added`/`modified`) exigen falla-antes; el resto son `unchanged`. RENAMED **no** forma parte del delta: solo cambia el título y conserva el ID, y el test no necesita cambiar |
| Specs y reglas | `openspec.Load` y `Check` en head, y el mismo `Check` en la base de confianza **y** en la base del cambio (una ejecución por base, cada una con su `CheckOptions.Base`). Un hallazgo solo se resta si está en **las dos** (§4) |
| Validación | `openspec.Validate` con la versión que fija la política de la base de confianza, si el PR toca `openspec/` |
| Declaraciones | `testsource.Scan` en head, en la base de confianza y en la base del cambio; `Compare` de head contra cada base con `changed` = ADDED ∪ MODIFIED ∪ REMOVED, y la **unión** de los hallazgos. Un ID solo renombrado sigue protegido contra la manipulación |
| Ejecución | Ejecución completa de `gotest` en head, falla-antes (§2) y regresiones aisladas (§3) |
| Scope | §3b |
| Aprobaciones | Reviews del PR (§5), leídas de la API de GitHub |

**Las declaraciones se leen de las dos bases** porque un test que existe en la base de confianza pero no en la base del cambio se podría, si no, volver a añadir debilitado o con `skip` y leerse como una adición, que `Compare` no mira. Con la unión, quitarle las aserciones a un test que la rama por defecto ya tiene es `tamper` aunque el merge-base no lo tuviera. La unión puede dar un falso positivo cuando el que cambió el test fue otro PR ya fusionado en la rama por defecto; el remedio es **rebasar** —así las dos bases coinciden en ese test—, no una excepción. **La unión está sesgada a propósito hacia el falso positivo,** porque la alternativa es una vía de lavado: el falso positivo cuesta un rebase, el falso negativo deja pasar un test debilitado.

**Sin código de motivo nuevo:** el `detail` del hallazgo de `tamper` dice **de qué base viene la declaración** y, cuando viene de la de confianza, que si lo causó el cambio de otra persona el remedio es rebasar. El resumen (§7) muestra ese `detail`, para que un revisor humano distinga "otro movió este test" de "este PR lo debilitó".

### 2. Falla-antes por superposición

Aplica a las obligaciones **F, N e I** con delta `added` o `modified`:
1. **Árbol de la base del cambio:** se materializa en un directorio temporal **desde los objetos de git** (`git ls-tree -r -z` más `git cat-file --batch`, respetando modos y enlaces simbólicos), **después** de la ejecución completa de head y justo antes de las ejecuciones en la base. La falla-antes es siempre contra la base del cambio: la de confianza no interviene aquí, porque mediría el comportamiento de otros PRs.
   - **Después,** porque prepararlo antes permitiría que un test de head alterara el árbol de la base —romper su código para que falle— y fabricara un `strong`.
   - **Sin `git worktree add` ni `git checkout`,** porque el checkout aplica `core.autocrlf`, `core.eol` y los filtros `smudge` que declaren `.git/config` y `.git/info/attributes`, y esos dos los puede escribir un test de head. `--attr-source` **no** cubre `.git/info/attributes`, ni `core.attributesFile` lo evita: por ese camino un test reescribe el contenido de la base a voluntad.
   - Tampoco `git archive` ni `cat-file --filters`, por lo mismo (§1). La prohibición cubre cualquier operación que escriba ficheros pasando por el checkout de git: `checkout-index`, `restore`, `stash` y las que vengan.
2. **Superposición:** en **todo el diff** `base del cambio..head`, no solo en los paquetes de la obligación:
   - **primero se borran** las rutas que head eliminó o renombró (un renombre cuenta como baja más alta);
   - después se copian los `*_test.go` y los ficheros de `testdata/` añadidos o modificados, **desde los objetos de git de head, nunca desde el árbol de trabajo**: la superposición ocurre después de la ejecución de head, y copiar del árbol permitiría la misma falsificación que cierra el paso 1.

   Limitarlo a los paquetes daría un `strong` falso si un test nuevo lee `testdata/` compartido.
3. **Selección exacta:**
   - a partir de los **nombres completos** de sus tests en la ejecución de head (por ejemplo `TestSuite/TestX/ORD-F01_…`) se construye un patrón anclado por nivel: `-run '^TestSuite$/^TestX$/^ORD-F01([_#]|$)'`, con cada nivel escapado;
   - se ejecuta **un proceso de test por (Test de primer nivel, ID)**, para que un hermano con un bug en la base no cambie el estado de otro ID;
   - para abaratarlo, se puede compilar el binario de test de cada paquete una vez (`go test -c`) y ejecutarlo por ID a través de `go tool test2json`.
4. **Estado:** el de cada ID sale de `gotest.Report.Status(id, <paquetes de sus tests en head>)`.
5. **Limpieza:** el worktree se elimina siempre.
6. **Entorno:** git y `go test` se lanzan sin las variables de repositorio que exporta un hook de git (`GIT_DIR`, `GIT_INDEX_FILE`, `GIT_WORK_TREE`…). Si no, un test de la base que use git escribiría en el índice de quien ejecuta aval.

La fuerza (ADR-0004) exige **además** que el estado en head sea `pass`:

| Estado en la base del cambio | Fuerza | Efecto |
|---|---|---|
| `fail` | `strong` | Válido |
| `fail`, y head añade o modifica ficheros que no son Go ni están en `testdata/` dentro de un paquete de la obligación | `weak`, con `note` | Válido; motivo `weak_evidence`. El fallo puede deberse a esos ficheros, no a la falta del comportamiento. Los datos de test van en `testdata/` |
| `build_fail` del propio paquete de test de la obligación | `weak` | Válido; motivo `weak_evidence` |
| `build_fail` de un paquete que head **añade** (código de producción nuevo del propio PR) | `weak`, con `note` | Válido; motivo `weak_evidence` |
| `build_fail` de un paquete que ya existe en la base del cambio, o por un módulo ausente | `none`, con `note` | Bloquea (`fail_before_missing`). Las dependencias nuevas entran antes, en un PR `dx` |
| `pass` con `**aval**: characterization` | `characterization` | Válido |
| `pass` sin la marca | `none` | Bloquea (`fail_before_missing`) |
| `not_run` / `skipped` | `none` | Bloquea (`fail_before_missing`) |

### 3. Regresiones aisladas

Las obligaciones F, N e I **fuera del delta** que tienen tests vinculados se ejecutan en head **aisladas**, con la selección exacta de §2.3 y un proceso por (Test de primer nivel, ID), igual que la falla-antes. Así ni los subtests hermanos ni los Tests anteriores pueden alterar su estado. Tienen que pasar.

### 3b. Clasificación de commits (scope)

Cada commit de `base del cambio..head` se clasifica por sus rutas con los globs `paths.dx`, `paths.feat` y `paths.seam` de la política **de la base de confianza**: los commits los delimita la base del cambio, los globs con que se clasifican salen de la base de confianza. Si una ruta encaja en varias familias, gana `seam`, después el glob más largo y, a igual longitud, `feat`: es la familia más estricta, y clasificar como `dx` algo que es `feat` podría bajar el tier. Un glob de directorio (`tools` o `tools/`) no encaja con lo que hay dentro: hace falta `tools/**`.

| Rutas del commit | Familia |
|---|---|
| `dx`, más cualquier `seam` u otras | `dx` |
| `feat`, más cualquier `seam` u otras | `feat` |
| `dx` **y** `feat` | `mixed`, con `families: [dx, feat]` → bloquea (`mixed_commit`) |
| Solo `seam` | `seam` |
| Ninguna familia | `other` |

`seam` y `other` **nunca** hacen `mixed` a un commit. Esto enmienda la frase de ADR-0004 "mixed cuando toca dos o más familias": `mixed` significa que toca `dx` y `feat`.

**Commits de merge:** se clasifican con `git show --remerge-diff --name-only --format=`, con el git endurecido de §1.

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
| `spec_rule` | Un hallazgo de severidad error de `openspec.Check` en head **que no esté en las dos bases** —en la de confianza y en la del cambio—, emparejando por regla, ruta y mensaje, sin número de línea. Uno que solo esté en una de las dos es nuevo: reintroducir un error que la rama por defecto ya arregló es nuevo. La ruta de un change archivado en el PR se normaliza a la del change activo en la base contra la que se compara, para que moverlo no convierta sus hallazgos en nuevos | 0–3 | block |
| `openspec_invalid` | `Validate` falla (todo INFO es fallo salvo la allowlist de ADR-0002) | 0–3, si el PR toca `openspec/` | block |
| `open_question` | Una obligación **O** ADDED o MODIFIED. Eliminar una O para resolverla no bloquea | 1–3 | block |
| `unverified` | Una obligación F/N/I ADDED o MODIFIED sin test vinculado | 1–3 | block |
| `fail_before_missing` | Falla-antes no válido (§2) | 1–3 | block |
| `after_not_passing` | Un test **vinculado** (del delta o no) no pasa en head | 0–3 | block |
| `regression` | Un test **no vinculado** falla en head y no figura en el baseline de la base de confianza | 0–3 | block |
| `build_failed` | Algún paquete no compila o `go test` falla al preparar la ejecución, en head | 0–3 | block |
| `tamper` | `testsource.Compare` devuelve un hallazgo contra cualquiera de las dos bases (§1), o el PR edita el `aval.yaml` raíz, `.aval/baseline.json`, `.github/**`, un `CODEOWNERS` de cualquiera de las tres ubicaciones que reconoce GitHub, `.golangci.yml` o cualquier `.gitattributes`. Esas ediciones se registran como hallazgos `policy_edited` (baseline: `baseline_edited`) | 0–3 | block |
| `undeclared_runtime` | Un test con ID se ejecuta sin declaración estática que lo empareje | 0–3 | block |
| `mixed_commit` | Un commit `mixed` (§3b) | 0–3 | block |
| `premortem_missing` | Un change de tier ≥ 2 sin `premortem.md` | según cada change | block |
| `premortem_unmapped` | Un `premortem.md` sin ítems, o con un ítem que no cita ningún ID de **su** change. Ítem = elemento de lista de primer nivel, fuera de bloques de código | según cada change | block |
| `approval_missing` | Tier 3 sin una aprobación válida (§5) | 3 | block |
| `lint_new_issues` | golangci-lint, con **el `.golangci.yml` de la base de confianza**, informa de problemas nuevos (`issues.new-from-merge-base` apuntado a **la base del cambio**) | 0–3, si la base de confianza tiene `.golangci.yml` | block |
| `assumption` | Una obligación **A** ADDED o MODIFIED | 1–3 | warn |
| `slo_unverified` | Una obligación **S** ADDED o MODIFIED (la v0 no mide SLOs) | 1–3 | warn |
| `spec_warning` | Un hallazgo de severidad warn de `openspec.Check` en head, **nuevo y restado igual que `spec_rule`: solo se descuenta si está en las dos bases** | 0–3 | warn |
| `seam_touched` | Un commit `feat` toca rutas `seam` | 0–3 | warn |
| `weak_evidence` | Alguna obligación con fuerza `weak` | 1–3 | warn |
| `no_base_policy` | La base de confianza no tiene `aval.yaml` raíz (PR de adopción) | — | warn; el gate corre en `observe` |

Códigos **reservados** para hitos posteriores: `contract_breaking` y `contract_lint` (M4), y `change_not_archived` (M3).

**El resultado del veredicto** es `block` si alguna regla bloquea, `warn` si solo hay avisos, y `pass` si no hay motivos. Una excepción válida (§5) rebaja `block` a `warn`.

### 5. Aprobaciones y excepciones: reviews ligados al SHA

Las etiquetas no sirven: no se atan a un commit, y la hora de GitHub que podría anclarlas (la check suite) se puede adelantar empujando el SHA a otra rama. aval usa **reviews del PR**, que GitHub liga al commit revisado.

Una aprobación es **válida** si cumple tres condiciones:
1. es el **último review que no sea `COMMENTED`** de ese revisor (como hace GitHub), tiene `state: APPROVED` y **`commit_id` igual al SHA head**. Un `CHANGES_REQUESTED` o `DISMISSED` posterior la anula;
2. su autor es un **CODEOWNER**, por una de dos vías (`GET /repos/{o}/{r}/collaborators/{user}/permission`):
   - listado individualmente en el `CODEOWNERS` de la **base de confianza** (§1) para el `aval.yaml` raíz —de las tres ubicaciones que reconoce GitHub gana la primera que exista: `.github/CODEOWNERS`, la raíz, `docs/CODEOWNERS`— **y** con permiso de escritura (`permission` ∈ {`write`, `admin`}; `maintain` se reporta como `write`). Es lo que exige GitHub para asignar un code owner, y evita que alguien registre el login de una cuenta renombrada o borrada que siga en el fichero. Un 404, `read` o `none` significa que no es owner;
   - o con `role_name` ∈ {`admin`, `maintain`}. Aquí `permission` no sirve, porque reporta `maintain` como `write`;
3. GitHub ya impide que el autor del PR apruebe su propio PR.

Hay dos tipos:

| Tipo | Requisito extra | Efecto |
|---|---|---|
| **Aprobación humana** | Ninguno | Satisface `approval_missing`; nada más |
| **Excepción (override)** | Una línea que empieza, sin sangría, por `aval:override <motivo>` en el cuerpo del review, fuera de bloques de código cercados (```` ``` ```` o `~~~`), con motivo no vacío. Si hay varias, cuenta la primera con motivo | Rebaja el veredicto de `block` a `warn`, sea cual sea el motivo (`tamper` incluido). Los motivos se conservan, `approval_missing` también, como aviso. Es la vía para cambiar la política: editar el `aval.yaml` raíz siempre es `tamper` |

- **Todas las aprobaciones se registran en el bundle,** también las inválidas, con su `rejection`. Un `aval:override` sin motivo es una excepción inválida, no una aprobación.
- **Un review de un commit anterior no cuenta.** Cuando llega un commit nuevo, hace falta volver a aprobar.
- **Los equipos de GitHub** exigen un token con `read:org` y quedan fuera de la v0.

### 6. Modos y códigos de salida

- **El modo sale de la política de la base de confianza.** Cambiarlo en el PR dispara `tamper` y no tiene efecto hasta fusionarse en la rama por defecto, lo que en `enforce` exige una excepción válida (§5).
- **`observe`:** el gate calcula e informa el veredicto y sale con 0.
- **`enforce`:** sale con 1 si el veredicto es `block`.

| Código | Cuándo |
|---|---|
| 0 | `pass` o `warn`; o cualquier veredicto en `observe` |
| 1 | `block` en `enforce` |
| 2 | Uso incorrecto, política inválida (ADR-0004), o **no se pudo resolver la base**: sin rama por defecto, sin `base.sha` en el repositorio local (§8), o merge-base incalculable o igual a head (§1) |
| 3 | Falta una herramienta (`go`, `git` ≥ 2.40, `node`/`npm`) o su versión no es la fijada |

### 7. Evidencia, baseline y resumen

- **El estado para hooks exime exactamente un código, `approval_missing`:** un agente no puede aprobar su propio PR, y si el estado lo exigiera, el hook de Stop lo bloquearía para siempre. Así que el estado es `passed` cuando el único motivo que bloquea es ese. **Ningún otro motivo está exento**, tampoco los que solo puede resolver una persona: `tamper` incluido, cuya vía es la excepción de §5, no el estado. La exención es del estado, no del veredicto: en el bundle el veredicto sigue siendo `block`, y el estado registra qué código se eximió. Los códigos de salida de §6 no cambian: `aval verify` sigue saliendo con 1 en `enforce`, para que una comprobación previa al push no mienta.
- **El estado para hooks (`internal/hook`) sube a `schemaVersion: 2`** y gana un campo con la **lista de códigos eximidos**: es un cambio de forma, y la regla de ADR-0004 vale también aquí. Hoy esa lista solo puede contener `approval_missing`. **Un estado cuya lista traiga cualquier otro código es inválido y se trata como "no pasado"**, igual que un estado ausente: así nadie amplía la exención escribiendo el fichero, y ampliarla de verdad exige otro ADR y otra versión.
- **`aval verify`** escribe el bundle en `.aval/evidence/<head>.json` y el estado para hooks en `.aval/cache/verify-status.json` (formato de `internal/hook`). La clave es `HEAD` más un hash de `git diff HEAD` **y de los ficheros sin seguimiento** (ruta y contenido), salvo los de `.aval/cache/` y `.aval/evidence/`, donde `verify` escribe después de calcular la clave. Sin estado, el hook de Stop solo deja terminar si el árbol está limpio y HEAD ya está en una rama remota; un commit local sin verificar no escapa. Así, un fichero nuevo tras `verify` invalida el estado.
- **`aval gate` ejecuta la verificación en el mismo proceso y nunca reutiliza un bundle del disco,** tampoco en local: `CheckHead` solo compara un SHA público, así que reutilizarlo sería confiar en un fichero, veredicto incluido. Recalcular cuesta poco.
- **La configuración de lint de la base de confianza se materializa en la raíz del repo justo antes de invocar golangci-lint,** nunca antes de la ejecución de head: golangci-lint ancla las rutas de su config en el directorio donde está, así que desde un temporal las exclusiones por ruta no encajarían. Se escribe con **los bytes que se guardaron en la fase de git**, sobrescribiendo el fichero aunque un test de head lo haya cambiado: si se materializara antes, un test reescribiría el `.golangci.yml` del árbol de trabajo y golangci-lint correría con la config del autor mientras §4 afirma que son los bytes de la base de confianza, silenciando `lint_new_issues`. Es la única escritura del gate fuera de `.aval/`, y se deshace antes de cerrar —se borra, o se restauran los bytes previos si ya existía—, también cuando la ejecución falla. No la ven `tamper`, scope ni `testsource`, que se calculan desde git, y como el estado para hooks se escribe después de deshacerla, tampoco entra en su clave.
- **golangci-lint ejecuta su propio `git`,** fuera del endurecimiento de §1 y sin sus variables de entorno, así que la revisión que se le pasa en `issues.new-from-merge-base` es **el SHA de la base del cambio**, nunca un nombre de rama: un nombre lo resolvería su git con las refs del repositorio, que un test de head puede reescribir (`.git/config`, `.git/info/*`, ref propia). El SHA no depende de ninguna ref.
- **Lo que no se recogió** se declara en `notCollected` con un token estable: `mutation`, `rollback`, `slo`, y `openspec_validate` cuando no hubo política en la base de confianza. El schema no los enumera, así que añadir uno no sube la versión.
- **Cierre:** el gate decide, escribe el veredicto y, en GitHub Actions, un resumen en `$GITHUB_STEP_SUMMARY`. El workflow sube el bundle como artefacto.

**Bundle v3** (`schemaVersion: 3`, schema en `internal/evidence/schema/bundle.v3.json`). Sube dos veces desde v1 porque los schemas son cerrados y cualquier cambio de forma sube la versión (ADR-0004). Dos cambios:

- **`approvals` sustituye a `override`** (ya en v2), una lista de:

  ```
  {kind: "approval" | "override", actor, commitId, submittedAt, reason, valid, rejection}
  ```

  `reason` solo aplica a `override`, y `rejection` solo a las inválidas.
- **`baseRef`, `trustBase` y `changeBase` sustituyen a `base`** (v3). `trustBase` y `changeBase` son SHAs completos de 40 caracteres —el tip de la rama por defecto y el merge-base, §1—; `baseRef` es la rama base del PR (`github.event.pull_request.base.ref`) o, en local, la revisión que se pasó a `--change-base`. No queda ningún campo llamado `base`: quien lea el bundle tiene que decir cuál de las dos quiere.

El resto del bundle no cambia. Así un retargeting del PR (§8) es visible en la evidencia: cambia `baseRef` y, con él, `changeBase`; `trustBase` no se mueve.

**Baseline v1** (`.aval/baseline.json`, contrato nuevo en `internal/baseline`, con JSON Schema):

```
{version: 1, failing: [{package, test}]}
```

Lista los tests no vinculados que ya fallaban al adoptar aval. Lo escribe `aval dx adopt` (M4); en M2, si no existe, se trata como vacío.

### 8. Workflow

- **Evento `on: pull_request`** con los tipos `opened`, `synchronize`, `reopened`, `ready_for_review` y **`edited`**, más **`on: pull_request_review`** para que una aprobación nueva reevalúe el gate.
- **`edited` está porque retargetear un PR manda `edited`, no `synchronize`.** Cambiar la rama base cambia el `base.sha` y con él la base del cambio, así que sin ese tipo el gate no se reevaluaría y el veredicto vigente sería el de otra base. El bundle registra `baseRef`, `trustBase` y `changeBase` (§7), de modo que el retargeting queda visible en la evidencia. `edited` también llega al editar título o cuerpo: reevaluar de más es barato.
- **Checkout** con `fetch-depth: 0` y `ref: ${{ github.event.pull_request.head.sha }}`.
- **El workflow trae las dos bases explícitamente** en un paso posterior al checkout: `git fetch --no-tags origin <base.sha> +refs/heads/<default_branch>:refs/remotes/origin/<default_branch>`. El refspec completo es obligatorio: un `git fetch origin <rama>` a secas deja el tip en `FETCH_HEAD` y no actualiza `origin/<default_branch>`, que es de donde aval lee la base de confianza. En un PR desde un fork, `fetch-depth: 0` **no** garantiza el `base.sha` —es un commit del repositorio base que puede no estar entre los refs que trae el checkout—, y sin él no hay merge-base. Si el fetch falla, exit 2 (§6), nunca un `pass`.
- **Permisos de solo lectura:** `contents: read`, `pull-requests: read`, `checks: read`. Sin secretos.
- **Prohibido filtrar el job con `if:` a nivel de job.** Un job saltado cuenta como éxito para un check obligatorio.

## Consecuencias

- **Los códigos de motivo son un contrato:** añadir uno es compatible, cambiar el significado de uno existente exige un ADR nuevo.
- **La primera adopción no puede bloquear** (`no_base_policy`).
- **Bundle v3 y baseline v1** se implementan en `internal/evidence` e `internal/baseline` antes del motor del gate.
- **Las dos bases atraviesan el código:** quien resuelve las referencias (`internal/actions`), quien las lee (`internal/verify`), quien compara contra ellas (`internal/openspec`, `internal/testsource`), quien las registra (`internal/evidence`), quien las muestra (`internal/summary`) y quien las expone como flags (`internal/cli`). Ningún parámetro ni ningún campo puede seguir llamándose solo `base`: hay que nombrar cuál.
- **La aprobación exige una segunda persona.** En un repo de una sola persona (el sandbox), `approval_missing` y `override` solo se pueden probar de punta a punta con una segunda cuenta de GitHub; mientras tanto se prueban contra una API de GitHub simulada.
- **Huecos fuera de la v0:**
  - equipos en CODEOWNERS;
  - reglas de contrato (M4);
  - archivo obligatorio del change (M3);
  - ejecución del gate desde una definición de workflow de confianza (por ejemplo, workflows requeridos por ruleset).
