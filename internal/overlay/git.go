package overlay

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

// repo runs hardened git in one directory.
type repo struct {
	*git.Runner
	dir string
}

func newRepo(dir string) repo {
	return repo{Runner: git.New(dir), dir: dir}
}

// commit resolves rev to a full commit SHA.
func (r repo) commit(ctx context.Context, rev string) (string, error) {
	if rev == "" {
		return "", fmt.Errorf("%w: %q", ErrRevision, rev)
	}
	out, err := r.Run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	var gerr *git.Error
	switch {
	case errors.As(err, &gerr) && gerr.ExitCode == 1: // --quiet: exit 1 and no message
		return "", fmt.Errorf("%w: %q", ErrRevision, rev)
	case err != nil:
		return "", fmt.Errorf("overlay: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// prefix returns the path of dir inside its work tree: "" at the top, or
// "sub/dir".
func (r repo) prefix(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, nil, "rev-parse", "--show-prefix")
	if err != nil {
		return "", fmt.Errorf("overlay: %w", err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "/"), nil
}

// modulePath reads the module path from the go.mod in root at commit.
func (r repo) modulePath(ctx context.Context, commit, root string) (string, error) {
	out, err := r.Run(ctx, nil, "cat-file", "blob", "--end-of-options", commit+":"+path.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("overlay: %w", err)
	}
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			if p, err := strconv.Unquote(f[1]); err == nil {
				return p, nil
			}
			return f[1], nil
		}
	}
	return "", fmt.Errorf("overlay: no module directive in %s at %s", path.Join(root, "go.mod"), commit)
}

// changeSet sorts the paths that differ between base and head.
type changeSet struct {
	copy, remove []string // test files and testdata head adds or modifies, or deletes
	others       []string // non-Go files head adds or modifies outside testdata
	newGo        []string // non-test Go files head adds
}

// changes lists the paths that differ between base and head. Renames count
// as a deletion and an addition, so no rename heuristics apply.
func (r repo) changes(ctx context.Context, base, head string) (changeSet, error) {
	out, err := r.Run(ctx, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-status",
		"--ignore-submodules=none", "--end-of-options", base, head)
	if err != nil {
		return changeSet{}, fmt.Errorf("overlay: %w", err)
	}
	return parseChanges(out)
}

// parseChanges reads diff-tree -z --name-status output: a status, then a
// path, each NUL-terminated.
func parseChanges(out []byte) (changeSet, error) {
	var c changeSet
	fields := strings.Split(string(out), "\x00")
	if len(fields)%2 != 1 || fields[len(fields)-1] != "" {
		return changeSet{}, fmt.Errorf("overlay: unexpected git diff-tree output %q", out)
	}
	for i := 0; i+1 < len(fields); i += 2 {
		status, name := fields[i], fields[i+1]
		switch {
		case status != "A" && status != "M" && status != "T" && status != "D":
			return changeSet{}, fmt.Errorf("overlay: unexpected status %q for %s in git diff-tree", status, name)
		case testFile(name) && status == "D":
			c.remove = append(c.remove, name)
		case testFile(name):
			c.copy = append(c.copy, name)
		case status == "D":
		case !strings.HasSuffix(name, ".go"):
			c.others = append(c.others, name)
		case status == "A" && goFile(path.Base(name)):
			c.newGo = append(c.newGo, name)
		}
	}
	return c, nil
}

// testFile reports whether name, a slash-separated path, is a Go test file
// or lives under a testdata directory.
func testFile(name string) bool {
	dir, file := path.Split(name)
	return strings.HasSuffix(file, "_test.go") || slices.Contains(strings.Split(dir, "/"), "testdata")
}

// goFile reports whether file names a non-test Go file.
func goFile(file string) bool {
	return strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go")
}
