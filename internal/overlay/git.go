package overlay

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

// settleTimeout bounds the git steps that run to completion although ctx
// ended: adding the worktree, laying the overlay, cleaning up.
const settleTimeout = 5 * time.Minute

// repo runs hardened git in one directory.
type repo struct {
	*git.Runner
	dir string
}

func newRepo(dir string) repo {
	return repo{Runner: git.New(dir), dir: dir}
}

// commonDir returns the repository's common directory, which outlives the
// directory r is in and every linked worktree.
func (r repo) commonDir(ctx context.Context) (repo, error) {
	out, err := r.Run(ctx, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repo{}, fmt.Errorf("overlay: %w", err)
	}
	return newRepo(strings.TrimSuffix(string(out), "\n")), nil
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

// settled returns a context that outlives ctx's cancellation for a bounded
// time, for git steps that must not stop half-way.
func settled(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
}

// addWorktree checks commit out, detached, in a new worktree at dir. It runs
// to completion even if ctx ends, so git never leaves it half-created.
func (r repo) addWorktree(ctx context.Context, dir, commit string) error {
	ctx, cancel := settled(ctx)
	defer cancel()
	if _, err := r.Run(ctx, nil, "worktree", "add", "--detach", "--end-of-options", dir, commit); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	return nil
}

// overlay deletes the removed paths from the worktree r is in and then
// copies the copied ones from commit. Deleting first keeps a rename that
// only changes case, on a case-insensitive file system, from deleting the
// file it just copied. It runs to completion even if ctx ends.
func (r repo) overlay(ctx context.Context, commit string, copied, removed []string) error {
	ctx, cancel := settled(ctx)
	defer cancel()
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	defer root.Close()
	for _, name := range removed {
		if err := root.Remove(filepath.FromSlash(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("overlay: %w", err)
		}
	}
	if len(copied) == 0 {
		return nil
	}
	paths := []byte(strings.Join(copied, "\x00"))
	if _, err := r.Run(ctx, paths, "--literal-pathspecs", "checkout",
		"--pathspec-from-file=-", "--pathspec-file-nul", "--end-of-options", commit); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	return nil
}

// cleanup removes the worktree at dir, if it was added, and tmp, which holds
// it; r is the repository's common directory, which is still there when the
// caller's directory is gone. When git could not remove the worktree, prune
// drops what it left in the repository once tmp is gone. Pruning only then
// leaves the admin entries of the caller's own missing worktrees alone.
func (r repo) cleanup(tmp, dir string, added bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	removed := false
	if added {
		_, err := r.Run(ctx, nil, "worktree", "remove", "--force", "--force", "--end-of-options", dir)
		removed = err == nil
	}
	var errs []error
	if err := os.RemoveAll(tmp); err != nil {
		errs = append(errs, err)
	}
	if !removed {
		if _, err := r.Run(ctx, nil, "worktree", "prune"); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s: %w", ErrCleanup, dir, errors.Join(errs...))
	}
	return nil
}
