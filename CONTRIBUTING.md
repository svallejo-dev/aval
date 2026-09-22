# Contribuir a aval

## Entorno de desarrollo

Las versiones de las herramientas se fijan en `.tool-versions` y se gestionan con [asdf](https://asdf-vm.com):

```sh
brew install asdf
asdf plugin add golang https://github.com/asdf-community/asdf-golang.git
asdf plugin add golangci-lint
asdf install          # instala lo que dice .tool-versions
make verify           # build + tests con -race + lint
```

Si instalas herramientas con `go install`, ejecuta después `asdf reshim golang` para que queden en el PATH.

## Modelo de ramas: trunk-based con ramas cortas

`main` está siempre verde y en condiciones de hacer release. El ruleset `main-trunk` la protege:

- solo se entra por PR;
- el único método de merge es squash;
- el historial es lineal;
- no se permite force push ni borrarla.

### Ramas

- **Viven menos de 2 días.** Si una feature es grande, se parte en PRs pequeños; nunca se alarga la rama.
- **Nombre:** `<tipo>/<tema-corto>` en kebab-case, por ejemplo `feat/gate-evidencia` o `fix/trace-huerfanos`.
- **Tipos:** `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `ci`.
- **Antes de mergear:** rebase sobre `main` (`git pull --rebase origin main`) y CI en verde.
- **Sesiones de agentes en paralelo:** una rama y un worktree por sesión. Ninguna sesión trabaja directamente sobre `main`.

### Merge

Siempre squash. El título del PR se convierte en el mensaje del commit en `main`, así que tiene que seguir Conventional Commits. GitHub borra la rama al mergear.

## Commits y títulos de PR: Conventional Commits

```
<tipo>(<ámbito opcional>): <descripción en imperativo>
```

Ejemplo: `feat(gate): bloquear si falta evidencia de un MUST`. Un cambio incompatible se marca con `!` tras el tipo o con un pie `BREAKING CHANGE:`.

## Versiones y releases

- **SemVer** con tags `vX.Y.Z` sobre `main`. Mientras la versión sea `0.y.z`, un minor puede romper compatibilidad.
- **Una release es un tag anotado en `main`:** `git tag -a v0.1.0 -m "..."` y después `git push origin v0.1.0`. La publicación automática de binarios llegará con el plan de implementación.
- **Parchear una versión antigua:** solo en ese caso se crea `release/vX.Y` desde su tag. El fix entra primero en `main` y luego se hace cherry-pick.

## Pendiente

- Añadir checks obligatorios al ruleset `main-trunk` cuando exista el CI.
- Cuando aval pueda gatearse a sí mismo, su propio `aval gate` será uno de esos checks.
