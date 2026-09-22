package openspec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// registry is where aval downloads OpenSpec from, whatever any .npmrc
	// or npm_config_* variable says.
	registry       = "https://registry.npmjs.org/"
	installTimeout = 5 * time.Minute
)

// install returns the path of the CLI script of OpenSpec at version,
// installed in v.cacheDir/<version>, outside every repository. The first
// call for a version installs it: npm runs in a new directory of the cache,
// with the registry on its command line, scripts off and no inherited
// npm_config_* variables, and the result moves into place with a rename, so
// concurrent runs never see a half-written install. Any mismatch fails
// closed with ErrToolFailed, including an existing install that does not
// hold the pinned package.
func (v validator) install(ctx context.Context, version string) (string, error) {
	dir := filepath.Join(v.cacheDir, version)
	if _, err := os.Stat(dir); err == nil {
		return installed(dir, version)
	}
	if err := os.MkdirAll(v.cacheDir, 0o750); err != nil {
		return "", fmt.Errorf("%w: %w", ErrToolFailed, err)
	}
	tmp, err := os.MkdirTemp(v.cacheDir, ".install-"+version+"-")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrToolFailed, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // a no-op once renamed

	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	out, err := v.run.Run(ctx, tmp, cleanEnv(v.environ), "npm", "install", "--prefix", tmp,
		"--no-save", "--ignore-scripts", "--no-audit", "--no-fund", "--registry", registry, npmPackage+"@"+version)
	switch {
	case err != nil:
		return "", fmt.Errorf("%w: npm install: %w", ErrToolFailed, err)
	case out.code != 0:
		return "", fmt.Errorf("%w: npm install: exit status %d%s", ErrToolFailed, out.code, withStderr(out.stderr))
	}
	if _, err := cliScript(tmp, version); err != nil {
		return "", fmt.Errorf("%w: npm installed something else: %w", ErrToolFailed, err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another run may have installed the same version first.
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", fmt.Errorf("%w: %w", ErrToolFailed, err)
		}
	}
	return installed(dir, version)
}

func installed(dir, version string) (string, error) {
	cli, err := cliScript(dir, version)
	if err != nil {
		return "", fmt.Errorf("%w: %s holds a broken OpenSpec install, delete it to reinstall: %w", ErrToolFailed, dir, err)
	}
	return cli, nil
}

// cliScript checks that dir holds npmPackage at version and returns the
// path of its "openspec" bin, which must be a file inside the package.
func cliScript(dir, version string) (string, error) {
	pkgDir := filepath.Join(dir, "node_modules", filepath.FromSlash(npmPackage))
	pkgFS := os.DirFS(pkgDir)
	data, err := fs.ReadFile(pkgFS, "package.json")
	if err != nil {
		return "", fmt.Errorf("read package.json: %w", err)
	}
	var pkg struct {
		Name    string          `json:"name"`
		Version string          `json:"version"`
		Bin     json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", fmt.Errorf("decode package.json: %w", err)
	}
	if pkg.Name != npmPackage || pkg.Version != version {
		return "", fmt.Errorf("package.json names %s@%s, want %s@%s", pkg.Name, pkg.Version, npmPackage, version)
	}
	var bin string
	if err := json.Unmarshal(pkg.Bin, &bin); err != nil {
		var bins map[string]string
		if err := json.Unmarshal(pkg.Bin, &bins); err != nil {
			return "", fmt.Errorf("decode package.json bin: %w", err)
		}
		bin = bins["openspec"]
	}
	rel := path.Clean(bin)
	if bin == "" || !fs.ValidPath(rel) {
		return "", fmt.Errorf("bin %q is not a file of the package", bin)
	}
	info, err := fs.Stat(pkgFS, rel)
	if err != nil {
		return "", fmt.Errorf("bin: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("bin " + rel + " is not a regular file")
	}
	return filepath.Join(pkgDir, filepath.FromSlash(rel)), nil
}

// cleanEnv drops the npm_config_* variables, in any case, from environ:
// npm reads them as configuration, so they could redirect or disable the
// install.
func cleanEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(strings.ToLower(key), "npm_config_") {
			out = append(out, kv)
		}
	}
	return out
}
