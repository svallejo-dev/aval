# ADR-0001 — Go, cobra y Charm v2

- **Estado:** aceptada
- **Fecha:** 2026-09-21

## Contexto

aval orquesta verificadores (`go test`, golangci-lint, oasdiff, vacuum, openspec) y decide un veredicto con su evidencia. El tiempo total lo dominan esos subprocesos, no el cómputo de aval. Los servicios que gobierna están escritos en Go, y el equipo del piloto también programa en Go.

Además, la CLI tiene que guiar a quien la usa, al estilo de Claude Code: pantalla de inicio, paleta de comandos, asistentes y vistas en vivo. Al mismo tiempo debe servir a agentes y al CI sin preguntar nada.

## Decisión

- **Lenguaje: Go 1.27, fijado con asdf en `.tool-versions`.** Se descartó Rust. Su ventaja en CPU no pesa en un orquestador limitado por I/O. En cambio, Go da:
  - integración nativa con el ecosistema que aval gobierna (`go/ast`, `go/packages`, el formato de `go test -json`, kin-openapi);
  - compilación rápida para el bucle de los agentes;
  - un código que el equipo puede mantener.
- **Árbol de comandos: cobra**, el estándar para subcomandos anidados, flags persistentes y completions.
- **Interfaz: Charm v2** (Bubble Tea, Bubbles, Lip Gloss, Huh, Glamour, con rutas `charm.land/*/v2`).
- **Tres modos de salida,** que se detallan en ADR-0003: `tui` para personas, `plain` para logs de CI y `json` para agentes y hooks. Nunca se pregunta sin TTY.

## Consecuencias

- La velocidad sale de la arquitectura:
  - verificadores en paralelo;
  - caché por hash de contenido;
  - Node solo donde hace falta;
  - hooks sin dependencias de Charm.
- Las dependencias previstas se fijan pronto en `go.mod` (`cmd/aval/tools.go`), para que varias ramas puedan avanzar en paralelo sin conflictos en `go.mod`.
