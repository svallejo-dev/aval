package overlay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/platform/gitenv"
)

// GitError reports a git command that failed or could not run.
type GitError struct {
	Args     []string // arguments to git
	ExitCode int      // -1 when git did not start or did not exit on its own
	Stderr   string
	Err      error // the cause, if any, e.g. wrapping exec.ErrNotFound
}

func (e *GitError) Error() string {
	msg := "git " + strings.Join(e.Args, " ")
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	} else {
		msg += fmt.Sprintf(": exit status %d", e.ExitCode)
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(e.Stderr), "\n"); line != "" {
		msg += ": " + line
	}
	return msg
}

func (e *GitError) Unwrap() error { return e.Err }

const (
	// waitDelay bounds how long git may keep its output open once it has
	// exited or been stopped.
	waitDelay = 5 * time.Second
	// settleTimeout bounds the git steps that run to completion although
	// ctx ended: adding the worktree, laying the overlay, cleaning up.
	settleTimeout = 5 * time.Minute
	// attrSource makes git read attributes from the empty tree, so head's
	// .gitattributes cannot change diffs or checkouts (ADR-0005 §1). It is
	// the SHA-1 empty tree: SHA-256 repositories are not supported.
	attrSource = "--attr-source=4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// repo runs git in one directory.
type repo struct{ dir string }

// git runs git with args, without hooks, replace refs, grafts or the
// caller's repository variables, and returns its standard output.
func (r repo) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)...) //nolint:gosec // no shell: fixed subcommands, full SHAs and paths git listed
	cmd.Dir = r.dir
	cmd.Env = append(gitenv.Clean(os.Environ()), "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE="+os.DevNull)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), context.Cause(ctx))
	case errors.As(err, &exitErr) && exitErr.Exited():
		return nil, &GitError{Args: args, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
	}
	return nil, &GitError{Args: args, ExitCode: -1, Stderr: stderr.String(), Err: err}
}

// checkVersion refuses a git older than 2.40, the first with --attr-source.
func (r repo) checkVersion(ctx context.Context) error {
	out, err := r.git(ctx, nil, "version")
	if err != nil {
		return err
	}
	if v := strings.TrimSpace(string(out)); !supported(v) {
		return fmt.Errorf("%w: found %q", ErrToolMissing, v)
	}
	return nil
}

// supported reports whether version, as `git version` prints it, is 2.40
// or later.
func supported(version string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(version, "git version %d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 2 || major == 2 && minor >= 40
}

// commit resolves rev to a full commit SHA.
func (r repo) commit(ctx context.Context, rev string) (string, error) {
	if rev == "" {
		return "", fmt.Errorf("%w: %q", ErrRevision, rev)
	}
	out, err := r.git(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	var gerr *GitError
	if errors.As(err, &gerr) && gerr.ExitCode == 1 { // --quiet: exit 1 and no message
		return "", fmt.Errorf("%w: %q", ErrRevision, rev)
	}
	return strings.TrimSpace(string(out)), err
}

// prefix returns the path of dir inside its work tree: "" at the top, or
// "sub/dir".
func (r repo) prefix(ctx context.Context) (string, error) {
	out, err := r.git(ctx, nil, "rev-parse", "--show-prefix")
	return strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "/"), err
}

// modulePath reads the module path from the go.mod in root at commit.
func (r repo) modulePath(ctx context.Context, commit, root string) (string, error) {
	out, err := r.git(ctx, nil, "cat-file", "blob", "--end-of-options", commit+":"+path.Join(root, "go.mod"))
	if err != nil {
		return "", err
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
	out, err := r.git(ctx, nil, attrSource, "diff-tree", "-r", "-z", "--no-renames", "--name-status",
		"--ignore-submodules=none", "--end-of-options", base, head)
	if err != nil {
		return changeSet{}, err
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
	_, err := r.git(ctx, nil, attrSource, "worktree", "add", "--detach", "--end-of-options", dir, commit)
	return err
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
	_, err = r.git(ctx, paths, attrSource, "--literal-pathspecs", "checkout",
		"--pathspec-from-file=-", "--pathspec-file-nul", "--end-of-options", commit)
	return err
}

// cleanup removes the worktree at dir, if it was added, and tmp, which holds
// it. When git could not remove the worktree, prune drops what it left in
// the repository once tmp is gone. Pruning only then leaves the admin
// entries of the caller's own missing worktrees alone.
func (r repo) cleanup(tmp, dir string, added bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	removed := false
	if added {
		_, err := r.git(ctx, nil, "worktree", "remove", "--force", "--force", "--end-of-options", dir)
		removed = err == nil
	}
	var errs []error
	if err := os.RemoveAll(tmp); err != nil {
		errs = append(errs, err)
	}
	if !removed {
		if _, err := r.git(ctx, nil, "worktree", "prune"); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s: %w", ErrCleanup, dir, errors.Join(errs...))
	}
	return nil
}
