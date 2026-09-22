# aval

CLI que gobierna el desarrollo agéntico: **SDD como capa de intención y un harness que decide con evidencia.** Nada avanza sin aval.

> **Estado:** pre-v0. Diseño cerrado, implementación sin empezar. Piloto interno para microservicios Go.

## La idea

Spec-driven development (SDD) ordena la entrada del ciclo, pero no prueba la salida. `aval` convierte cada obligación de la spec en una fila `obligación → verificador → evidencia → gate`. Un cambio solo se promueve cuando la evidencia, ejecutada y trazable, lo respalda.

- **La CLI gobierna; no es un agente.** Claude Code, Codex, Copilot o Cursor la invocan a través de skills.
- **El CI es el gate real.** Los hooks del agente solo dan feedback rápido.
- **Funcional y DX, separados:** `aval feat` gobierna qué hace el servicio; `aval dx`, cómo se construye y se entrega.
- **Un solo modelo** para servicios existentes y nuevos.

## Comandos previstos

```
aval dx adopt      servicio existente: detecta, toma baseline, activa modo observación
aval dx plan       diferencia entre el repo y el perfil DX
aval dx apply      acerca el repo al perfil; en un repo vacío equivale al scaffold
aval feat new      change de OpenSpec + premortem + obligaciones con ID
aval feat trace    matriz obligación → test; huérfanos en ambos sentidos
aval verify        ejecuta verificadores y genera la evidencia
aval gate          decide y devuelve el exit code para CI
```

## Requisitos

- **Node.js y npx** para validar las specs. aval ejecuta `npx -y @fission-ai/openspec@<versión>` con la versión exacta de `openspec.version` en `aval.yaml`, nunca un rango ni `latest`. La primera vez, npx descarga ese paquete de la red, siempre del registro público de npm (`https://registry.npmjs.org/`): aval lo fija en el entorno, que manda sobre cualquier `.npmrc` del repositorio o del usuario. La telemetría y la comprobación de actualizaciones de OpenSpec van desactivadas. Sin Node ni npx, aval sale con código 3.

## Contribuir

Modelo de ramas, commits y releases: [CONTRIBUTING.md](CONTRIBUTING.md).

## Licencia

[MIT](LICENSE)
