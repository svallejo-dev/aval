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

- **Node.js y npm** para validar las specs con OpenSpec, en la versión exacta de `openspec.version` en `aval.yaml`, nunca un rango ni `latest`.
  - OpenSpec nunca sale del repositorio: aval no usa su `node_modules` ni su `.npmrc`.
  - La primera vez que se usa una versión, aval la instala fuera del repositorio, en `<caché del usuario>/aval/openspec/<versión>`, con `npm install --prefix <dir> --no-save --ignore-scripts --registry https://registry.npmjs.org/ @fission-ai/openspec@<versión>`. Esa instalación necesita red y va siempre al registro público de npm.
  - npm corre sin las variables `npm_config_*` del entorno. Después, aval comprueba el nombre y la versión del `package.json` instalado. La instalación se escribe en un directorio temporal y se mueve con un rename, así que dos ejecuciones a la vez no la corrompen.
  - Las siguientes ejecuciones reutilizan esa instalación: aval lanza su `bin` con `node` desde la raíz del repositorio, con la telemetría y la comprobación de actualizaciones de OpenSpec desactivadas.
  - Sin Node ni npm, aval sale con código 3. Si la instalación de la caché no es la esperada, falla sin ejecutarla; hay que borrar ese directorio.

## Contribuir

Modelo de ramas, commits y releases: [CONTRIBUTING.md](CONTRIBUTING.md).

## Licencia

[MIT](LICENSE)
