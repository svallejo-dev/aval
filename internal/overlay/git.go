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
	"strings"
	"time"
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
)

// repoEnv lists the variables that tie git to one repository (git
// rev-parse --local-env-vars). A git hook running aval exports some of
// them, and they would point the worktree's commands at the caller's index.
var repoEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX", "GIT_SHALLOW_FILE",
	"GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS",
}

// repo runs git in one directory.
type repo struct{ dir string }

// git runs git with args, without hooks, replace refs or the caller's
// repository variables, and returns its standard output.
func (r repo) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)...) //nolint:gosec // no shell: fixed subcommands, full SHAs and paths git listed
	cmd.Dir = r.dir
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(repoEnv, k)
	}), "GIT_NO_REPLACE_OBJECTS=1")
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
		return nil, fmt.Errorf("git %s: %w", args[0], context.Cause(ctx))
	case errors.As(err, &exitErr) && exitErr.Exited():
		return nil, &GitError{Args: args, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
	}
	return nil, &GitError{Args: args, ExitCode: -1, Stderr: stderr.String(), Err: err}
}

// commit resolves rev to a full commit SHA.
func (r repo) commit(ctx context.Context, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%w: %q", ErrRevision, rev)
	}
	out, err := r.git(ctx, nil, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	var gerr *GitError
	if errors.As(err, &gerr) && gerr.ExitCode == 1 { // --quiet: exit 1 and no message
		return "", fmt.Errorf("%w: %q", ErrRevision, rev)
	}
	return strings.TrimSpace(string(out)), err
}

// prefix returns the path of dir inside its work tree: "" at the top, or
// "sub/dir/".
func (r repo) prefix(ctx context.Context) (string, error) {
	out, err := r.git(ctx, nil, "rev-parse", "--show-prefix")
	return strings.TrimSuffix(string(out), "\n"), err
}

// changes lists the test files and testdata that differ between base and
// head: the ones to copy from head, and the ones head deleted. Renames
// count as a deletion and an addition, so no rename heuristics apply.
func (r repo) changes(ctx context.Context, base, head string) (copied, removed []string, err error) {
	out, err := r.git(ctx, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-status", base, head)
	if err != nil {
		return nil, nil, err
	}
	return parseChanges(out)
}

// parseChanges reads diff-tree -z --name-status output: a status, then a
// path, each NUL-terminated.
func parseChanges(out []byte) (copied, removed []string, err error) {
	fields := strings.Split(string(out), "\x00")
	if len(fields)%2 != 1 || fields[len(fields)-1] != "" {
		return nil, nil, fmt.Errorf("overlay: unexpected git diff-tree output %q", out)
	}
	for i := 0; i+1 < len(fields); i += 2 {
		status, name := fields[i], fields[i+1]
		if !testFile(name) {
			continue
		}
		switch status {
		case "A", "M", "T":
			copied = append(copied, name)
		case "D":
			removed = append(removed, name)
		default:
			return nil, nil, fmt.Errorf("overlay: unexpected status %q for %s in git diff-tree", status, name)
		}
	}
	return copied, removed, nil
}

// testFile reports whether name, a slash-separated path, is a Go test file
// or lives under a testdata directory.
func testFile(name string) bool {
	dir, file := path.Split(name)
	return strings.HasSuffix(file, "_test.go") || slices.Contains(strings.Split(dir, "/"), "testdata")
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
	_, err := r.git(ctx, nil, "worktree", "add", "--detach", dir, commit)
	return err
}

// overlay copies the copied paths from commit into the worktree r is in and
// deletes the removed ones. It runs to completion even if ctx ends.
func (r repo) overlay(ctx context.Context, commit string, copied, removed []string) error {
	ctx, cancel := settled(ctx)
	defer cancel()
	if len(copied) > 0 {
		paths := []byte(strings.Join(copied, "\x00"))
		if _, err := r.git(ctx, paths, "--literal-pathspecs", "checkout", commit, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
	}
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
	return nil
}

// cleanup removes the worktree at dir, if it was added, and tmp, which holds
// it, however Run ended. When git could not remove the worktree, prune drops
// what it left in the repository once tmp is gone. Pruning only then leaves
// the admin entries of the caller's own missing worktrees alone.
func (r repo) cleanup(ctx context.Context, tmp, dir string, added bool) error {
	ctx, cancel := settled(ctx)
	defer cancel()
	removed := false
	if added {
		_, err := r.git(ctx, nil, "worktree", "remove", "--force", "--force", dir)
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
