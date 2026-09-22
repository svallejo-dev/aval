// Package git runs git hardened against what a change under review can plant
// in the repository (ADR-0005 §1). Every command runs:
//
//   - with GIT_NO_REPLACE_OBJECTS=1 and GIT_GRAFT_FILE=/dev/null, so replace
//     refs and grafts cannot rewrite the history;
//   - with --attr-source=<empty tree>, so no .gitattributes in the repository
//     can pick diff or merge drivers, filters or binary handling, unless the
//     Runner was made WithoutAttrSource;
//   - with -c core.hooksPath=/dev/null, so no hook runs;
//   - without the repository variables a git hook exports (gitenv.Clean), so
//     GIT_DIR or GIT_INDEX_FILE cannot point it at another repository;
//   - with GIT_OPTIONAL_LOCKS=0, so it never takes the index lock only to
//     refresh the index, a lock the user's own git commands need.
//
// The flags that belong to a subcommand stay with the caller:
// --ignore-submodules=none on diffs, -z on output that lists paths, and
// --end-of-options before any revision or path that did not come from aval.
//
// The repository's own .git/config and .git/info are still trusted, and a
// change's code could rewrite them once it runs: callers must run git before
// any of it executes, such as its tests.
package git

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/svallejo-dev/aval/internal/platform/gitenv"
)

// ErrToolMissing is wrapped by the error of a git that is not on PATH, and
// by CheckVersion's for a git older than 2.40, the first with --attr-source.
// Callers map it to exit code 3.
var ErrToolMissing = errors.New("git 2.40 or newer is required")

// waitDelay bounds how long git may keep its output open once it has exited
// or been stopped.
const waitDelay = 5 * time.Second

// hardening is what every git command gets in its environment.
var hardening = []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + os.DevNull, "GIT_OPTIONAL_LOCKS=0"}

// Error reports a git command that failed or could not run. It carries the
// arguments the caller passed, not the options Runner adds, and never the
// environment.
type Error struct {
	Args     []string // the caller's arguments to git, without the options Runner adds
	ExitCode int      // -1 when git did not start or did not exit on its own
	Stderr   string   // what git wrote to standard error
	// Err is the cause, if any: ctx's, the error of EmptyTree, or one that
	// wraps ErrToolMissing and exec.ErrNotFound.
	Err error
}

func (e *Error) Error() string {
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

func (e *Error) Unwrap() error { return e.Err }

// Runner runs hardened git commands in one directory. It is safe for
// concurrent use.
type Runner struct {
	dir          string
	noAttrSource bool // set by WithoutAttrSource

	mu        sync.Mutex
	emptyTree string // set by the first EmptyTree that succeeds
}

// Option configures a Runner.
type Option func(*Runner)

// WithoutAttrSource makes Run and Stream leave out --attr-source, so they
// read attributes from the working tree as plain git does, and need no git
// hash-object first. Every other protection stays.
//
// Only the agent hooks use it (package hook): they read the agent's own
// working tree to build an advisory cache key, within a 50 ms budget, and an
// agent that could plant a .gitattributes could as well write the status
// the key guards. The gate, which reads base and head in CI where head is
// untrusted, must not use it (ADR-0005 §1).
func WithoutAttrSource() Option {
	return func(r *Runner) { r.noAttrSource = true }
}

// New returns a Runner for dir; "" is the current directory.
func New(dir string, opts ...Option) *Runner {
	r := &Runner{dir: dir}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run runs git with args, and with stdin as its standard input when it is
// not nil, and returns its standard output. args follow the options Run
// adds: global options, if any, then the subcommand and its arguments. The
// first Run needs the empty tree, see EmptyTree, unless the Runner was made
// WithoutAttrSource.
//
// A failed command returns an *Error with args. When ctx ends first, git is
// stopped and Err is ctx's cause; when git is not on PATH, Err wraps
// ErrToolMissing and exec.ErrNotFound.
func (r *Runner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	if err := r.hardened(ctx, stdin, &stdout, args); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Stream runs git like Run, without input, and copies its standard output to
// w as it arrives instead of holding it: for output that may be large, such
// as a diff.
func (r *Runner) Stream(ctx context.Context, w io.Writer, args ...string) error {
	return r.hardened(ctx, nil, w, args)
}

// EmptyTree returns the ID of the empty tree in the repository, which
// differs between SHA-1 and SHA-256 repositories: git hash-object computes
// it, once per Runner. Outside a repository it is the SHA-1 one.
func (r *Runner) EmptyTree(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.emptyTree != "" {
		return r.emptyTree, nil
	}
	args := []string{"hash-object", "-t", "tree", "--stdin"}
	var out bytes.Buffer
	if err := r.run(ctx, nil, &out, "", args); err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out.String())
	if _, err := hex.DecodeString(tree); err != nil || tree == "" {
		return "", &Error{Args: args, Err: fmt.Errorf("not an object ID: %q", out.String())}
	}
	r.emptyTree = tree
	return tree, nil
}

// CheckVersion returns an error wrapping ErrToolMissing unless git is 2.40
// or newer. It runs git version without --attr-source, which an older git
// would refuse.
func (r *Runner) CheckVersion(ctx context.Context) error {
	var out bytes.Buffer
	if err := r.run(ctx, nil, &out, "", []string{"version"}); err != nil {
		return err
	}
	return checkVersion(out.String())
}

// checkVersion returns an error wrapping ErrToolMissing unless out, what git
// version prints, names git 2.40 or newer.
func checkVersion(out string) error {
	v := strings.TrimSpace(out)
	var major, minor int
	if _, err := fmt.Sscanf(v, "git version %d.%d", &major, &minor); err == nil && (major > 2 || major == 2 && minor >= 40) {
		return nil
	}
	return fmt.Errorf("%w: git version says %q", ErrToolMissing, v)
}

// hardened runs git with every protection, attributes from the empty tree
// included unless r is WithoutAttrSource. Without the empty tree, git does
// not start.
func (r *Runner) hardened(ctx context.Context, stdin []byte, stdout io.Writer, args []string) error {
	if r.noAttrSource {
		return r.run(ctx, stdin, stdout, "", args)
	}
	tree, err := r.EmptyTree(ctx)
	if err != nil {
		return &Error{Args: args, ExitCode: -1, Err: err}
	}
	return r.run(ctx, stdin, stdout, tree, args)
}

// run runs git in r's directory with the hardened environment, without
// hooks and, when attrSource is not empty, with --attr-source=attrSource.
func (r *Runner) run(ctx context.Context, stdin []byte, stdout io.Writer, attrSource string, args []string) error {
	argv := make([]string, 0, len(args)+3)
	argv = append(argv, "-c", "core.hooksPath="+os.DevNull)
	if attrSource != "" {
		argv = append(argv, "--attr-source="+attrSource)
	}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, "git", argv...) //nolint:gosec // no shell: callers put revisions and paths from outside after --end-of-options
	cmd.Dir = r.dir
	cmd.Env = append(gitenv.Clean(os.Environ()), hardening...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return &Error{Args: args, ExitCode: -1, Stderr: stderr.String(), Err: context.Cause(ctx)}
	case errors.As(err, &exitErr) && exitErr.Exited():
		return &Error{Args: args, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
	case errors.Is(err, exec.ErrNotFound):
		return &Error{Args: args, ExitCode: -1, Err: fmt.Errorf("%w: %w", ErrToolMissing, err)}
	}
	return &Error{Args: args, ExitCode: -1, Stderr: stderr.String(), Err: err}
}
