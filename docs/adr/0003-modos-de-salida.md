# ADR-0003 — Modos de salida: tui, plain y json

- **Estado:** aceptada
- **Fecha:** 2026-09-21

## Contexto

A aval lo usan tres tipos de cliente: una persona en una terminal, un log de CI y un agente o hook que parsea la salida. Cada uno necesita algo distinto. La persona quiere estilo y guía, el CI quiere texto estable y el agente quiere un contrato JSON. Además, aval nunca debe quedarse esperando una respuesta que nadie puede dar. El ADR-0001 dejó esta decisión para un ADR propio.

## Decisión

### Tres modos, resueltos en un solo sitio

`internal/ui` resuelve el modo con una función pura (`ui.Resolve`) sobre una entrada explícita: los flags, una función para leer el entorno, si la salida es una terminal y si stdin lo es. El orden es:

1. **`json`** si se pasa `--json`. Gana a todo lo demás, también a `--plain` y a `CI`.
2. **`plain`** si se pasa `--plain`, si stdout no es una terminal, si `CI` es verdadero según `strconv.ParseBool` o si `TERM=dumb`. Para saber si algo es una terminal, se consulta al driver con un ioctl (`github.com/charmbracelet/x/term`). Así, `/dev/null`, los pipes y los ficheros no cuentan como terminal.
3. **`tui`** en cualquier otro caso.

Una variable de entorno cuenta como definida si existe y no está vacía, igual que exige la convención de [NO_COLOR](https://no-color.org).

### Qué permite cada modo

| | Color | Animaciones | Preguntas |
|---|---|---|---|
| `tui` | sí, salvo `NO_COLOR` | sí, salvo `--no-animation` o `AVAL_REDUCED_MOTION` | solo si stdin es una terminal y no hay `--yes` |
| `plain` | no | no | no |
| `json` | no | no | no |

**Nunca se pregunta sin TTY.** Solo se pregunta en `tui`, que exige una terminal en stdout, y además stdin tiene que ser una terminal. Por eso un pipe, un fichero, el CI o un agente nunca se quedan bloqueados esperando una respuesta. `--yes` acepta los valores por defecto también en una terminal. Informar de un error nunca pregunta.

### Los comandos no saben cómo se muestran

Un comando entrega a `ui` un `ui.Result`: el nombre del comando, los datos del envelope JSON y su forma legible como líneas de texto con tono (título, éxito, aviso, error, apagado). El `ui.Printer` lo escribe según el modo:

- **`json`:** el envelope v1 (`schemaVersion`, `command`, `ok`, `data`, `errors`) de `internal/envelope`, por stdout. `errors` es siempre un array, nunca `null`.
- **`plain`:** el texto sin tonos y sin cortar líneas, para que los logs se puedan filtrar con grep.
- **`tui`:** el mismo texto con estilos de Lip Gloss v2. Por ahora la salida es estática; las vistas en vivo con Bubble Tea llegarán en hitos posteriores.

Los errores salen como envelope con `ok: false` por stdout en `json`. En los otros modos salen como `aval: <mensaje>` por stderr, con estilo en `tui`. Como el error va a stderr, su modo y su color se resuelven contra stderr, no contra stdout: `aval trace 2>err.log` en una terminal da la tabla con estilo y un log sin secuencias de escape.

Un comando puede producir su resultado y aun así fallar sus comprobaciones; por ejemplo, `aval trace` con obligaciones sin test. En ese caso el `ui.Result` lleva, además de los datos y las líneas, los `Issues` que explican el fallo:

- en `json`, el envelope sale con `ok: false`, el resultado completo en `data` y los motivos en `errors`;
- en `plain` y `tui`, las líneas van por stdout y los motivos, como `aval: <mensaje>`, por stderr.

`data` es `null` solo cuando el comando no llegó a producir un resultado. `--json` y `--plain` se leen directamente de los argumentos, porque el parseo de flags puede haber fallado. Los códigos de salida y su nombre en el envelope (`failed`, `usage`, `tool`) siguen en `internal/cli`.

### Color, animación y accesibilidad

- **`NO_COLOR`** quita el color, pero mantiene la negrita y el texto atenuado, como pide la convención.
- **La paleta usa los 16 colores ANSI de la terminal.** Así sigue el esquema del usuario, incluidos los de alto contraste, y no hace falta degradar colores.
- **El significado nunca depende solo del color.** Los comandos acompañan cada estado con un símbolo o una palabra.
- **`--no-animation` y `AVAL_REDUCED_MOTION`** detienen spinners y transiciones.
- **Formularios accesibles.** Cuando lleguen los formularios con Huh, usarán su modo accesible (preguntas en texto plano, legibles por lectores de pantalla). El disparador de ese modo se decidirá en ese hito.

### Hooks de agentes sin Charm

Los hooks que ejecutan los agentes corren en cada acción y tienen un presupuesto de latencia de **menos de 50 ms**. Por eso no pueden importar `internal/ui` ni ningún paquete de Charm, ni directa ni indirectamente.

- **Los hooks vivirán en `internal/hook`.** Escribirán su salida con la biblioteca estándar.
- **El envelope vive en `internal/envelope`**, que solo usa la biblioteca estándar. Así los hooks emiten el mismo contrato que `--json` sin cargar Charm. `internal/ui` lo importa, y no al revés.
- **Una regla de golangci-lint lo impone.** La regla `charm-free` de depguard, en `.golangci.yml`, prohíbe `charm.land/` y `github.com/charmbracelet/` en `internal/envelope` e `internal/hook`, tests incluidos. Un import prohibido rompe `make verify` y el CI.

## Consecuencias

- Cada modo tiene golden files en `internal/ui/testdata` (`go test ./internal/ui -update` los regenera). Los de `tui` se generan con un ancho fijo y el color apagado de forma explícita. En Lip Gloss v2, `Style.Render` no lee el entorno, así que el resultado es igual en cualquier máquina.
- No se importa `github.com/charmbracelet/colorprofile` directamente, porque eso cambiaría `go.mod`. El color lo decide el tema a partir de `Settings.Color`.
- `github.com/charmbracelet/x/term` pasa a ser una dependencia directa de `internal/ui`. Ya estaba en el grafo de módulos como dependencia indirecta.
- depguard no debe quedar activado sin reglas. En ese caso aplica su regla por defecto, que solo permite la biblioteca estándar en todo el repositorio.
- La salida estática todavía no conoce el ancho de la terminal. `tui` solo corta líneas cuando recibe un ancho (`ui.WithWidth`).
