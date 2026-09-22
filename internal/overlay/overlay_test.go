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

// goEnv keeps the developer's workspace, flags and toolchain out of the
// runs in the fixture module.
var goEnv = []string{"GOWORK=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local"}

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
// returns the repository and both commits.
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
		r.git("add", "--all")
		r.git("commit", "--quiet", "--message", tree)
		return r.git("rev-parse", "HEAD")
	}
	return r, commit("base"), commit("head")
}

// assertClean checks that Run left no worktree and nothing in tmp behind.
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
	targets := []Target{
		target(t, "ORD-F01", "TestSpec/ORD-F01_adds_two_numbers", "calc", false),
		target(t, "ORD-F02", "TestSpec/ORD-F02_doubles", "money", false),
		target(t, "ORD-F03", "TestGreet/ORD-F03_greets", "legacy", false),
		target(t, "ORD-F04", "TestGreet/ORD-F04_greets_by_name", "legacy", true),
		target(t, "ORD-F05", "TestTax/ORD-F05_takes_a_cut", "tax", false),
		target(t, "ORD-F06", "TestBanner/ORD-F06_matches_the_golden_file", "banner", true),
		target(t, "ORD-F08", "TestSuite/TestAdd/ORD-F08_adds_many", "calc", false),
		target(t, "ORD-F09", "TestGhost/ORD-F09_is_nowhere", "calc", false),
	}
	tmp := t.TempDir()

	res, err := Run(t.Context(), filepath.Join(r.dir, "svc"), "HEAD~1", "HEAD", targets, Options{Env: goEnv, TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	assertClean(t, r, tmp)

	if res.Base != base || res.Head != head {
		t.Errorf("Base, Head = %s, %s; want %s, %s", res.Base, res.Head, base, head)
	}
	wantCopied := []string{
		"svc/banner/banner_test.go", "svc/banner/testdata/want.txt", "svc/calc/calc_test.go",
		"svc/hang/hang_test.go", "svc/legacy/legacy_test.go", "svc/money/money_test.go", "svc/tax/tax_test.go",
	}
	if !reflect.DeepEqual(res.Copied, wantCopied) || !reflect.DeepEqual(res.Removed, []string{"svc/tax/old_test.go"}) {
		t.Errorf("Copied = %q, Removed = %q; want %q and [svc/tax/old_test.go]", res.Copied, res.Removed, wantCopied)
	}

	want := map[string]struct {
		before   evidence.Status
		strength evidence.Strength
	}{
		"ORD-F01": {evidence.Fail, evidence.Strong},        // head's fix is not copied
		"ORD-F02": {evidence.BuildFail, evidence.Weak},     // Double does not exist at the base
		"ORD-F03": {evidence.Pass, evidence.None},          // nothing new, and not declared so
		"ORD-F04": {evidence.Pass, evidence.Characterized}, // nothing new, as declared
		"ORD-F05": {evidence.Fail, evidence.Strong},        // renamed away: old_test.go is gone
		"ORD-F06": {evidence.Pass, evidence.Characterized}, // the testdata came along
		"ORD-F08": {evidence.Fail, evidence.Strong},        // selected two levels down
		"ORD-F09": {evidence.NotRun, evidence.None},        // selects nothing
	}
	if len(res.Obligations) != len(targets) {
		t.Fatalf("got %d obligations, want %d", len(res.Obligations), len(targets))
	}
	for i, o := range res.Obligations {
		w := want[o.ID.String()]
		if o.ID != targets[i].ID || o.Before != w.before || o.After != evidence.Pass || o.Strength != w.strength {
			t.Errorf("obligation %d = %+v, want %s before=%s strength=%s", i, o, targets[i].ID, w.before, w.strength)
		}
	}

	var tests []string
	for _, run := range res.Runs {
		tests = append(tests, run.Test)
		for _, to := range run.Report.Tests {
			if to.Name == "TestSuite/TestOther" {
				t.Errorf("run %s ran %s", run.Test, to.Name)
			}
		}
	}
	if want := []string{"TestSpec", "TestGreet", "TestTax", "TestBanner", "TestSuite", "TestGhost"}; !reflect.DeepEqual(tests, want) {
		t.Fatalf("runs = %q, want %q", tests, want)
	}
	spec := res.Runs[0].Options
	if spec.Run != `^TestSpec$/^(ORD-F01|ORD-F02)([_#]|$)` || spec.Count != 1 ||
		!reflect.DeepEqual(spec.Packages, []string{"example.com/svc/calc", "example.com/svc/money"}) {
		t.Errorf("TestSpec run options = %+v", spec)
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
		go func() {
			for ctx.Err() == nil {
				if _, err := os.Stat(started); err == nil {
					cancel()
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		opts := Options{Env: append([]string{"AVAL_OVERLAY_STARTED=" + started}, goEnv...), TempDir: tmp}
		if _, err := Run(ctx, dir, "HEAD~1", "HEAD", targets, opts); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		assertClean(t, r, tmp)
	})
	t.Run("before Run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tmp := t.TempDir()
		if _, err := Run(ctx, dir, "HEAD~1", "HEAD", targets, Options{TempDir: tmp}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		assertClean(t, r, tmp)
	})
}

func TestRunRevisions(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	r, _, _ := fixtureRepo(t)
	targets := []Target{target(t, "ORD-F01", "TestSpec/ORD-F01_adds_two_numbers", "calc", false)}
	for _, rev := range []string{"nope", "-h", "", "HEAD^{tree}"} {
		if _, err := Run(t.Context(), r.dir, rev, "HEAD", targets, Options{}); !errors.Is(err, ErrRevision) {
			t.Errorf("base %q: err = %v, want ErrRevision", rev, err)
		}
	}
	var gerr *GitError
	if _, err := Run(t.Context(), t.TempDir(), "HEAD~1", "HEAD", targets, Options{}); !errors.As(err, &gerr) || errors.Is(err, ErrRevision) {
		t.Errorf("outside a repository: err = %v, want a *GitError", err)
	}
}

// TestRunEnv checks that RUNNER_TEMP holds the worktree and that a git
// hook's repository variables do not leak into the worktree's commands:
// with the repository checked out at the base, they would stage head's
// tests in its index.
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
	targets := []Target{target(t, "ORD-F01", "TestSpec/ORD-F01_adds_two_numbers", "calc", false)}

	res, err := Run(t.Context(), filepath.Join(r.dir, "svc"), base, head, targets, Options{Env: goEnv})
	_ = os.Unsetenv("GIT_DIR")
	_ = os.Unsetenv("GIT_INDEX_FILE")
	if err != nil || len(res.Obligations) != 1 || res.Obligations[0].Strength != evidence.Strong {
		t.Errorf("Run = %+v, %v; want ORD-F01 strong", res.Obligations, err)
	}
	if st := r.git("status", "--porcelain"); st != "" {
		t.Errorf("the repository changed:\n%s", st)
	}
	assertClean(t, r, tmp)
}

func TestRunGitMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	targets := []Target{target(t, "ORD-F01", "TestSpec/ORD-F01_x", "calc", false)}
	_, err := Run(t.Context(), t.TempDir(), "HEAD~1", "HEAD", targets, Options{})
	var gerr *GitError
	if !errors.Is(err, ErrToolMissing) || !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &gerr) {
		t.Errorf("err = %v, want ErrToolMissing wrapping a *GitError and exec.ErrNotFound", err)
	}
}
