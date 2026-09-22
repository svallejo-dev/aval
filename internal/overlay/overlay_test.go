package overlay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
)

// goEnv keeps the developer's workspace, flags, toolchain and proxy out of
// the runs in the fixture module.
var goEnv = []string{"GOWORK=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "GOPROXY=off"}

// testRepo is a git repository built for one test, out of the developer's
// git configuration.
type testRepo struct {
	t   *testing.T
	dir string
}

func (r testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", args...) //nolint:gosec // test helper, fixed arguments
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=aval", "GIT_AUTHOR_EMAIL=aval@example.com",
		"GIT_COMMITTER_NAME=aval", "GIT_COMMITTER_EMAIL=aval@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixtureRepo commits testdata/repo/base and then testdata/repo/head, and
// returns the repository and both commits. The index is rebuilt from the
// files each time, so a rename that only changes case is one on macOS too.
func fixtureRepo(t *testing.T) (r testRepo, base, head string) {
	t.Helper()
	r = testRepo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet")
	commit := func(tree string) string {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != ".git" {
				if err := os.RemoveAll(filepath.Join(r.dir, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.CopyFS(r.dir, os.DirFS(filepath.Join("testdata", "repo", tree))); err != nil {
			t.Fatal(err)
		}
		r.git("rm", "-r", "--quiet", "--cached", "--ignore-unmatch", ".")
		r.git("add", "--all")
		r.git("commit", "--quiet", "--message", tree)
		return r.git("rev-parse", "HEAD")
	}
	return r, commit("base"), commit("head")
}

// assertClean checks that no worktree and nothing in tmp is left behind.
func assertClean(t *testing.T, r testRepo, tmp string) {
	t.Helper()
	if n := strings.Count("\n"+r.git("worktree", "list", "--porcelain"), "\nworktree "); n != 1 {
		t.Errorf("git worktree list shows %d worktrees, want only the repository's own", n)
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) > 0 {
		t.Errorf("temporary directory holds %v (%v), want it empty", entries, err)
	}
}

func target(t *testing.T, s, test, pkg string, characterization bool) Target {
	t.Helper()
	return Target{
		ID: id(t, s), Tests: []string{test}, Packages: []string{"example.com/svc/" + pkg},
		After: evidence.Pass, Characterization: characterization,
	}
}

func TestRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r, base, head := fixtureRepo(t)
	// A replace ref must not change what the base is, and hooks must not
	// run: this one fails every checkout.
	r.git("replace", base, head)
	hook := filepath.Join(r.dir, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // a hook must be executable
		t.Fatal(err)
	}
	tmp := t.TempDir()

	w, err := Prepare(t.Context(), filepath.Join(r.dir, "svc"), "HEAD~1", "HEAD", PrepareOptions{TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	// Head's tests run now and may rewrite the working tree: Run must not care.
	if err := os.WriteFile(filepath.Join(r.dir, "svc", "calc", "calc_test.go"), []byte("package calc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := []Target{
		target(t, "ORD-F01", "TestSpec/ORD-F01_adds_two_numbers", "calc", false),
		target(t, "ORD-F02", "TestSpec/ORD-F02_doubles", "money", false),
		target(t, "ORD-F03", "TestGreet/ORD-F03_greets", "legacy", false),
		target(t, "ORD-F04", "TestGreet/ORD-F04_greets_by_name", "legacy", true),
		target(t, "ORD-F05", "TestTax/ORD-F05_takes_a_cut", "tax", false),
		target(t, "ORD-F06", "TestBanner/ORD-F06_matches_the_golden_file", "banner", true),
		target(t, "ORD-F08", "TestSuite/TestAdd/ORD-F08_adds_many", "calc", false),
		target(t, "ORD-F09", "TestGhost/ORD-F09_is_nowhere", "calc", false),
		target(t, "ORD-F10", "TestShout/ORD-F10_shouts_the_fixture", "reader", false),
		target(t, "ORD-F12", "TestLoud/ORD-F12_is_loud", "casing", false),
		target(t, "ORD-F13", "TestDep/ORD-F13_uses_the_new_module", "deps", false),
		target(t, "ORD-F14", "TestUses/ORD-F14_greets_through_the_new_package", "uses", false),
		target(t, "ORD-F18", "TestLevel/ORD-F18_starts_at_level_zero", "shared", false),
		target(t, "ORD-F19", "TestLevel/ORD-F19_raises_the_level_while_f_runs", "shared", false),
	}
	res, err := w.Run(t.Context(), targets, RunOptions{Env: goEnv})
	if cerr := w.Close(); err != nil || cerr != nil {
		t.Fatalf("Run: %v; Close: %v", err, cerr)
	}
	assertClean(t, r, tmp)
	if _, err := w.Run(t.Context(), targets, RunOptions{}); !errors.Is(err, ErrClosed) || w.Close() != nil {
		t.Errorf("Run after Close = %v, want ErrClosed; a second Close must do nothing", err)
	}

	if w.Base != base || w.Head != head {
		t.Errorf("Base, Head = %s, %s; want %s, %s", w.Base, w.Head, base, head)
	}
	var wantCopied []string
	for _, pkg := range []string{"banner", "banner/testdata/want.txt", "calc", "casing", "deps", "gitty", "hang", "legacy", "money", "reader", "shared", "tax", "uses"} {
		if !strings.Contains(pkg, ".") {
			pkg += "/" + pkg + "_test.go"
		}
		wantCopied = append(wantCopied, "svc/"+pkg)
	}
	if want := []string{"svc/casing/Casing_test.go", "svc/tax/old_test.go"}; !reflect.DeepEqual(w.Copied, wantCopied) || !reflect.DeepEqual(w.Removed, want) {
		t.Errorf("Copied = %q, Removed = %q; want %q and %q", w.Copied, w.Removed, wantCopied, want)
	}
	if want := []string{"svc/reader/fixtures/in.txt"}; !reflect.DeepEqual(res.Uncopied, want) {
		t.Errorf("Uncopied = %q, want %q", res.Uncopied, want)
	}

	want := map[string]struct {
		before   evidence.Status
		strength evidence.Strength
		note     string
	}{
		"ORD-F01": {evidence.Fail, evidence.Strong, ""},                                                  // head's fix is not copied
		"ORD-F02": {evidence.BuildFail, evidence.Weak, ""},                                               // its own test build fails
		"ORD-F03": {evidence.Pass, evidence.None, ""},                                                    // nothing new, and not declared so
		"ORD-F04": {evidence.Pass, evidence.Characterized, ""},                                           // nothing new, as declared
		"ORD-F05": {evidence.Fail, evidence.Strong, ""},                                                  // renamed away: old_test.go is gone
		"ORD-F06": {evidence.Pass, evidence.Characterized, ""},                                           // the testdata came along
		"ORD-F08": {evidence.Fail, evidence.Strong, ""},                                                  // selected two levels down
		"ORD-F09": {evidence.NotRun, evidence.None, ""},                                                  // selects nothing
		"ORD-F10": {evidence.Fail, evidence.Weak, "head-only files: svc/reader/fixtures/in.txt"},         // the fixture stayed behind
		"ORD-F12": {evidence.Fail, evidence.Strong, ""},                                                  // Casing_test.go became casing_test.go
		"ORD-F13": {evidence.BuildFail, evidence.None, "in example.com/dep: land new dependencies"},      // a module the base lacks
		"ORD-F14": {evidence.BuildFail, evidence.Weak, "in example.com/svc/helper, a package head adds"}, // new production code
		"ORD-F18": {evidence.Pass, evidence.None, ""},                                                    // ORD-F19's leak stays in its own run
		"ORD-F19": {evidence.Fail, evidence.Strong, ""},
	}
	if len(res.Obligations) != len(targets) || len(res.Runs) != len(targets) {
		t.Fatalf("got %d obligations and %d runs, want %d of each", len(res.Obligations), len(res.Runs), len(targets))
	}
	for i, o := range res.Obligations {
		w := want[o.ID.String()]
		if o.ID != targets[i].ID || o.Before != w.before || o.After != evidence.Pass || o.Strength != w.strength ||
			(w.note == "") != (o.Note == "") || !strings.Contains(o.Note, w.note) {
			t.Errorf("obligation %d = %+v, want %s before=%s strength=%s note ~%q", i, o, targets[i].ID, w.before, w.strength, w.note)
		}
	}
	for _, run := range res.Runs {
		for _, to := range run.Report.Tests {
			if to.Name == "TestSuite/TestOther" || strings.HasPrefix(to.Name, "TestLevel/") && !strings.Contains(to.Name, run.ID.String()) {
				t.Errorf("the run for %s ran %s", run.ID, to.Name)
			}
		}
	}
	if o := res.Runs[0].Options; o.Run != `^TestSpec$/^ORD-F01([_#]|$)` || o.Count != 1 || !reflect.DeepEqual(o.Packages, []string{"example.com/svc/calc"}) {
		t.Errorf("ORD-F01's run options = %+v", o)
	}
}

func TestRunCanceled(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r, _, _ := fixtureRepo(t)
	dir := filepath.Join(r.dir, "svc")
	targets := []Target{target(t, "ORD-F07", "TestHang/ORD-F07_never_ends", "hang", false)}

	t.Run("while the tests run", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		started, tmp := filepath.Join(t.TempDir(), "started"), t.TempDir()
		w, err := Prepare(ctx, dir, "HEAD~1", "HEAD", PrepareOptions{TempDir: tmp})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for ctx.Err() == nil {
				if _, err := os.Stat(started); err == nil {
					cancel()
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		opts := RunOptions{Env: append([]string{"AVAL_OVERLAY_STARTED=" + started}, goEnv...)}
		if _, err := w.Run(ctx, targets, opts); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		assertClean(t, r, tmp)
	})
	t.Run("before Prepare", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tmp := t.TempDir()
		if w, err := Prepare(ctx, dir, "HEAD~1", "HEAD", PrepareOptions{TempDir: tmp}); !errors.Is(err, context.Canceled) || w != nil {
			t.Errorf("Prepare = %v, %v; want nil and context.Canceled", w, err)
		}
		assertClean(t, r, tmp)
	})
}

func TestPrepareRevisions(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	r, _, _ := fixtureRepo(t)
	dir := filepath.Join(r.dir, "svc")
	for _, rev := range []string{"nope", "-h", "", "HEAD^{tree}"} {
		if _, err := Prepare(t.Context(), dir, rev, "HEAD", PrepareOptions{}); !errors.Is(err, ErrRevision) {
			t.Errorf("base %q: err = %v, want ErrRevision", rev, err)
		}
	}
	var gerr *GitError
	if _, err := Prepare(t.Context(), t.TempDir(), "HEAD~1", "HEAD", PrepareOptions{}); !errors.As(err, &gerr) || errors.Is(err, ErrRevision) {
		t.Errorf("outside a repository: err = %v, want a *GitError", err)
	}
}

// TestRunEnv checks that RUNNER_TEMP holds the worktree and that a git
// hook's repository variables reach neither aval's git nor the tests at the
// base: with the repository checked out at the base, the overlay would stage
// head's tests in its index, and ORD-F11's git add its own file.
func TestRunEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	r, base, head := fixtureRepo(t)
	r.git("checkout", "--quiet", "--detach", base)
	tmp := t.TempDir()
	t.Setenv("RUNNER_TEMP", tmp)
	t.Setenv("GIT_DIR", filepath.Join(r.dir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(r.dir, ".git", "index"))
	targets := []Target{
		target(t, "ORD-F01", "TestSpec/ORD-F01_adds_two_numbers", "calc", false),
		target(t, "ORD-F11", "TestGit/ORD-F11_stages_in_its_own_repository", "gitty", false),
	}

	w, err := Prepare(t.Context(), filepath.Join(r.dir, "svc"), base, head, PrepareOptions{})
	var res Result
	if err == nil {
		res, err = w.Run(t.Context(), targets, RunOptions{Env: goEnv})
		err = errors.Join(err, w.Close())
	}
	_ = os.Unsetenv("GIT_DIR")
	_ = os.Unsetenv("GIT_INDEX_FILE")
	if err != nil || len(res.Obligations) != 2 || res.Obligations[0].Strength != evidence.Strong || res.Obligations[1].Before != evidence.Pass {
		t.Errorf("Run = %+v, %v; want ORD-F01 strong and ORD-F11 passing", res.Obligations, err)
	}
	if st := r.git("status", "--porcelain"); st != "" {
		t.Errorf("the repository changed:\n%s", st)
	}
	assertClean(t, r, tmp)
}

func TestPrepareGitMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Prepare(t.Context(), t.TempDir(), "HEAD~1", "HEAD", PrepareOptions{})
	var gerr *GitError
	if !errors.Is(err, ErrToolMissing) || !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &gerr) {
		t.Errorf("err = %v, want ErrToolMissing wrapping a *GitError and exec.ErrNotFound", err)
	}
}
