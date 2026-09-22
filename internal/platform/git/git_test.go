package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/svallejo-dev/aval/internal/platform/gitenv"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	sha1EmptyTree   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	sha256EmptyTree = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

// newRepo returns a new git repository in a temporary directory, created
// with the extra git init args.
func newRepo(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitT(t, dir, append([]string{"init", "-q", "-b", "main"}, args...)...)
	return dir
}

// gitT runs git in dir, unhardened and without the developer's
// configuration, and returns its trimmed standard output.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // no shell: fixed arguments from the tests
	cmd.Dir = dir
	cmd.Env = append(gitenv.Clean(os.Environ()), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=aval", "GIT_AUTHOR_EMAIL=aval@example.com",
		"GIT_COMMITTER_NAME=aval", "GIT_COMMITTER_EMAIL=aval@example.com", "GIT_OPTIONAL_LOCKS=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.Bytes())
	}
	return strings.TrimSpace(string(out))
}

// commit writes files into dir, commits everything, even when nothing
// changed, and returns the commit's ID.
func commit(t *testing.T, dir, msg string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return gitT(t, dir, "rev-parse", "HEAD")
}

func run(t *testing.T, r *Runner, args ...string) string {
	t.Helper()
	out, err := r.Run(t.Context(), nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestRunHardening plants in one repository everything Run must ignore, and
// checks each plant with plain git first, so that none passes vacuously. It
// changes the environment, so it cannot run in parallel.
func TestRunHardening(t *testing.T) {
	dir := newRepo(t)
	c1 := commit(t, dir, "c1", map[string]string{"a.txt": "one\n", ".gitattributes": "a.txt -diff\n"})
	c2 := commit(t, dir, "c2", map[string]string{"a.txt": "two\n"})
	gitT(t, dir, "switch", "-q", "--detach", c1)
	c3 := commit(t, dir, "c3", nil)
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "replace", c1, c3) // read c3 for c1
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "grafts"), []byte(c2+"\n"), 0o600); err != nil {
		t.Fatal(err) // c2 without parents
	}
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch hooked\nexit 1\n"), 0o700); err != nil { //nolint:gosec // a hook must be executable
		t.Fatal(err)
	}
	other := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	r := New(dir)

	diff := []string{"diff", "--no-ext-diff", "--no-color", c1, c2, "--", "a.txt"}
	if got := gitT(t, dir, diff...); !strings.Contains(got, "Binary files") {
		t.Fatalf("plain git diff = %q, want the .gitattributes to make it binary", got)
	}
	if got := run(t, r, diff...); !strings.Contains(got, "-one\n+two") {
		t.Errorf("diff = %q, want a text patch: attributes come from the empty tree", got)
	}

	logArgs := []string{"log", "-1", "--format=%s", c1}
	if got := gitT(t, dir, logArgs...); got != "c3" {
		t.Fatalf("plain git log = %q, want the replace ref to show c3", got)
	}
	if got := run(t, r, logArgs...); got != "c1" {
		t.Errorf("log = %q, want c1: replace refs are ignored", got)
	}

	if got := gitT(t, dir, "rev-list", c2); got != c2 {
		t.Fatalf("plain git rev-list = %q, want the graft to cut c2's parent", got)
	}
	if got, want := run(t, r, "rev-list", c2), c2+"\n"+c1; got != want {
		t.Errorf("rev-list = %q, want %q: grafts are ignored", got, want)
	}

	gitDir, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, r, "rev-parse", "--absolute-git-dir"); got != gitDir {
		t.Errorf("rev-parse --absolute-git-dir = %q, want %q: GIT_DIR must not leak in", got, gitDir)
	}

	// A file whose stat data changed makes git status refresh the index,
	// and write it when it may take the optional lock.
	index := filepath.Join(dir, ".git", "index")
	before := readFile(t, index)
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	run(t, r, "status", "--porcelain")
	if !bytes.Equal(readFile(t, index), before) {
		t.Error("git status wrote the index: GIT_OPTIONAL_LOCKS=0 is missing")
	}
	gitT(t, dir, "status", "--porcelain")
	if bytes.Equal(readFile(t, index), before) {
		t.Error("plain git status left the index alone: the check above proves nothing")
	}

	if runtime.GOOS == "windows" {
		return // the hook is a shell script
	}
	if _, err := r.Run(t.Context(), nil, "checkout", "-q", "--detach", c2); err != nil {
		t.Errorf("checkout with a failing post-checkout hook: %v, want hooks disabled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooked")); err == nil {
		t.Error("the post-checkout hook ran")
	}
}

// TestWithoutAttrSource checks that a default Runner reads attributes from
// the empty tree and one made WithoutAttrSource from the working tree, and
// that the latter keeps the other protections.
func TestWithoutAttrSource(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	c1 := commit(t, dir, "c1", map[string]string{"a.txt": "one\n", ".gitattributes": "a.txt -diff\n"})
	c2 := commit(t, dir, "c2", map[string]string{"a.txt": "two\n"})
	gitT(t, dir, "replace", c2, c1) // read c1 for c2
	diff := []string{"diff", "--no-ext-diff", "--no-color", c1, c2, "--", "a.txt"}

	if got := run(t, New(dir), diff...); !strings.Contains(got, "-one\n+two") {
		t.Errorf("default Runner: diff = %q, want a text patch: attributes from the empty tree", got)
	}
	without := New(dir, WithoutAttrSource())
	if got := run(t, without, diff...); !strings.Contains(got, "Binary files") {
		t.Errorf("WithoutAttrSource: diff = %q, want the working tree's -diff to make it binary", got)
	}
	if got := run(t, without, "log", "-1", "--format=%s", c2); got != "c2" {
		t.Errorf("WithoutAttrSource: log = %q, want c2: replace refs are still ignored", got)
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name) //nolint:gosec // a file of the test's repository
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEmptyTree(t *testing.T) {
	t.Parallel()
	sha256Repo := func(t *testing.T) string {
		t.Helper()
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}
		dir := t.TempDir()
		cmd := exec.CommandContext(t.Context(), "git", "init", "-q", "--object-format=sha256", dir) //nolint:gosec // a temporary path
		cmd.Env = gitenv.Clean(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("this git cannot create a SHA-256 repository: %v\n%s", err, out)
		}
		return dir
	}
	tests := []struct {
		name string
		dir  func(*testing.T) string
		want string
	}{
		{"SHA-1 repository", func(t *testing.T) string { return newRepo(t) }, sha1EmptyTree},
		{"SHA-256 repository", sha256Repo, sha256EmptyTree},
		{"outside a repository", func(t *testing.T) string { return t.TempDir() }, sha1EmptyTree},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := tt.dir(t)
			r := New(dir)
			if got, err := r.EmptyTree(t.Context()); err != nil || got != tt.want {
				t.Fatalf("EmptyTree = %q, %v; want %q", got, err, tt.want)
			}
			// Once known, it takes no git: this one could not even start.
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			if got, err := r.EmptyTree(t.Context()); err != nil || got != tt.want {
				t.Errorf("second EmptyTree = %q, %v; want %q from the first", got, err, tt.want)
			}
		})
	}
}

// TestEndOfOptions shows why a revision from outside must follow
// --end-of-options: a tag named like an option is otherwise read as one,
// and this one makes git log write a file.
func TestEndOfOptions(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	c1 := commit(t, dir, "c1", nil)
	commit(t, dir, "c2", nil)
	gitT(t, dir, "update-ref", "refs/tags/--output=pwned", c1)
	r := New(dir)
	pwned := filepath.Join(dir, "pwned")

	if got := run(t, r, "log", "--format=%H", "--end-of-options", "--output=pwned", "--"); got != c1 {
		t.Errorf("log after --end-of-options = %q, want %s, the tag's commit", got, c1)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("git log wrote pwned although --end-of-options came first")
	}
	if got := run(t, r, "log", "--format=%H", "--output=pwned", "--"); got != "" {
		t.Errorf("log without --end-of-options = %q, want nothing on stdout", got)
	}
	if _, err := os.Stat(pwned); err != nil {
		t.Errorf("without --end-of-options git log did not take the tag as an option: %v", err)
	}
}

func TestRunIO(t *testing.T) {
	t.Parallel()
	r := New(newRepo(t))
	const blob = "ce013625030ba8dba906f756967f9e9ca394464a" // "hello\n"
	out, err := r.Run(t.Context(), []byte("hello\n"), "hash-object", "--stdin")
	if err != nil || string(out) != blob+"\n" {
		t.Errorf("hash-object --stdin = %q, %v; want %s", out, err, blob)
	}
	var w bytes.Buffer
	if err := r.Stream(t.Context(), &w, "hash-object", "--stdin"); err != nil || w.String() != sha1EmptyBlob+"\n" {
		t.Errorf("Stream hash-object --stdin = %q, %v; want %s, of no input", w.String(), err, sha1EmptyBlob)
	}
}

const sha1EmptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"

func TestRunErrors(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	r := New(dir)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name     string
		ctx      context.Context
		stream   bool // Stream instead of Run
		args     []string
		exitCode int
		is       error
		msg      string
	}{
		{"exit status", t.Context(), false, []string{"rev-parse", "--verify", "--quiet", "nope"}, 1, nil,
			"git rev-parse --verify --quiet nope: exit status 1"},
		{"stderr", t.Context(), false, []string{"rev-parse", "--verify", "nope"}, 128, nil,
			"git rev-parse --verify nope: exit status 128: fatal: "},
		{"canceled", canceled, false, []string{"status"}, -1, context.Canceled, "context canceled"},
		{"Stream", t.Context(), true, []string{"rev-parse", "--verify", "nope"}, 128, nil,
			"git rev-parse --verify nope: exit status 128: fatal: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tt.stream {
				err = r.Stream(tt.ctx, io.Discard, tt.args...)
			} else {
				_, err = r.Run(tt.ctx, nil, tt.args...)
			}
			var gerr *Error
			if !errors.As(err, &gerr) {
				t.Fatalf("Run = %v, want an *Error", err)
			}
			// Only what the caller passed: not -c, --attr-source nor the environment.
			if !reflect.DeepEqual(gerr.Args, tt.args) || gerr.ExitCode != tt.exitCode {
				t.Errorf("Error = %+v, want args %q and exit code %d", gerr, tt.args, tt.exitCode)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("Run = %v, want it to wrap %v", err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.msg) || strings.Contains(err.Error(), "attr-source") {
				t.Errorf("Run = %q, want it to mention %q and no option Run added", err, tt.msg)
			}
		})
	}
}

// TestEnviron checks the variables that no git test here can observe: a
// credential prompt and a lazy fetch need a remote. The process sets the
// opposite values, and the last value of a key is the one git gets. It
// changes the environment, so it cannot run in parallel.
func TestEnviron(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	env := environ()
	for key, want := range map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_NO_LAZY_FETCH": "1"} {
		got, found := "", false
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, key+"="); ok {
				got, found = v, true
			}
		}
		if !found || got != want {
			t.Errorf("environ() gives git %s=%q (found %t), want %q", key, got, found, want)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		out string
		ok  bool
	}{
		{"git version 2.50.1 (Apple Git-155)\n", true},
		{"git version 2.40.0\n", true},
		{"git version 2.40.0", true},
		{"git version 2.45.1.windows.1\n", true},
		{"git version 3.0.0\n", true},
		{"git version 2.39.5\n", false},
		{"git version 1.99.0\n", false},
		{"hub version 2.14", false},
		{"git version\n", false},
		{"", false},
	}
	for _, tt := range tests {
		err := checkVersion(tt.out)
		if (err == nil) != tt.ok || err != nil && !errors.Is(err, ErrToolMissing) {
			t.Errorf("checkVersion(%q) = %v, want ok %t", tt.out, err, tt.ok)
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if err := New(t.TempDir()).CheckVersion(t.Context()); err != nil {
		t.Errorf("CheckVersion with the installed git: %v", err)
	}
}

// TestOldGit puts a git 2.39 first on PATH, so it cannot run in parallel.
func TestOldGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho 'git version 2.39.5'\n"), 0o700); err != nil { //nolint:gosec // it must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	err := New(t.TempDir()).CheckVersion(t.Context())
	var gerr *Error
	if !errors.Is(err, ErrToolMissing) || errors.As(err, &gerr) || !strings.Contains(err.Error(), "2.39.5") {
		t.Errorf("CheckVersion with git 2.39 = %v, want ErrToolMissing naming the version, and no *Error", err)
	}
}

// TestNoGit empties PATH, so it cannot run in parallel.
func TestNoGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := New(t.TempDir())
	_, runErr := r.Run(t.Context(), nil, "status")
	for name, err := range map[string]error{"Run": runErr, "CheckVersion": r.CheckVersion(t.Context())} {
		var gerr *Error
		if !errors.Is(err, ErrToolMissing) || !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &gerr) {
			t.Errorf("%s without git = %v, want an *Error wrapping ErrToolMissing and exec.ErrNotFound", name, err)
		}
	}
}
