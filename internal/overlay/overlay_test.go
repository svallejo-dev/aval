package overlay

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/platform/git"
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
// returns the repository, made with the extra git init args, and both
// commits. The index is rebuilt from the files each time, so a rename that
// only changes case is one on macOS too.
func fixtureRepo(t *testing.T, initArgs ...string) (r testRepo, base, head string) {
	t.Helper()
	r = testRepo{t: t, dir: t.TempDir()}
	r.git(append([]string{"init", "--quiet"}, initArgs...)...)
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

// assertClean checks that nothing in tmp, and no git worktree, is left
// behind: Prepare must add none.
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
	var gerr *git.Error
	if _, err := Prepare(t.Context(), t.TempDir(), "HEAD~1", "HEAD", PrepareOptions{}); !errors.As(err, &gerr) || errors.Is(err, ErrRevision) {
		t.Errorf("outside a repository: err = %v, want a *git.Error", err)
	}
}

// TestRunEnv checks that RUNNER_TEMP holds the tree and that a git
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
	var gerr *git.Error
	if !errors.Is(err, ErrToolMissing) || !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &gerr) {
		t.Errorf("err = %v, want ErrToolMissing wrapping a *git.Error and exec.ErrNotFound", err)
	}
}

// TestPrepareSHA256 checks that attributes come from the repository's own
// empty tree: git refuses the SHA-1 one in a SHA-256 repository.
func TestPrepareSHA256(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	probe := exec.CommandContext(t.Context(), "git", "init", "--quiet", "--object-format=sha256", t.TempDir()) //nolint:gosec // a temporary path
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("this git cannot create a SHA-256 repository: %v\n%s", err, out)
	}
	r, base, head := fixtureRepo(t, "--object-format=sha256")
	tmp := t.TempDir()
	w, err := Prepare(t.Context(), filepath.Join(r.dir, "svc"), base, head, PrepareOptions{TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	for tree, file := range map[string]string{"head": "calc/calc_test.go", "base": "calc/calc.go"} {
		want, err := os.ReadFile(filepath.Join("testdata", "repo", tree, "svc", file)) //nolint:gosec // a fixture
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(w.moduleDir, file)); err != nil || !bytes.Equal(got, want) { //nolint:gosec // the tree
			t.Errorf("%s in the tree = %q, %v; want %s's", file, got, err, tree)
		}
	}
	if len(w.Base) != 64 || w.Base != base {
		t.Errorf("Base = %s, want %s", w.Base, base)
	}
	if err := w.Close(); err != nil {
		t.Error(err)
	}
	assertClean(t, r, tmp)
}

// TestCloseWithoutDir checks that Close removes the tree once the directory
// Prepare ran in is gone: it only has a temporary directory to remove.
func TestCloseWithoutDir(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	r, base, head := fixtureRepo(t)
	dir, tmp := filepath.Join(r.dir, "svc"), t.TempDir()
	w, err := Prepare(t.Context(), dir, base, head, PrepareOptions{TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close without %s: %v", dir, err)
	}
	assertClean(t, r, tmp)
}

// TestPrepareOldGit puts a git 2.39 first on PATH, so it cannot run in
// parallel.
func TestPrepareOldGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho 'git version 2.39.5'\n"), 0o700); err != nil { //nolint:gosec // it must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, err := Prepare(t.Context(), t.TempDir(), "HEAD~1", "HEAD", PrepareOptions{})
	if !errors.Is(err, ErrToolMissing) || !errors.Is(err, git.ErrToolMissing) {
		t.Errorf("err = %v, want ErrToolMissing wrapping git.ErrToolMissing", err)
	}
}

// writeFiles writes files, named with "/" and relative to dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// planted builds a repository whose base holds the module svc with package
// a, and whose head adds files, and returns it and both commits. plant and
// change, when not nil, run on the working tree just before the base and the
// head commit, and whatever plant leaves is in both trees.
func planted(t *testing.T, plant func(testRepo), files map[string]string, change func(testRepo)) (r testRepo, base, head string) {
	t.Helper()
	r = testRepo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet")
	writeFiles(t, r.dir, map[string]string{"svc/go.mod": "module example.com/svc\n", "svc/a/a.go": "package a\n"})
	if plant != nil {
		plant(r)
	}
	r.git("add", "--all")
	r.git("commit", "--quiet", "--message", "base")
	base = r.git("rev-parse", "HEAD")
	writeFiles(t, r.dir, files)
	if change != nil {
		change(r)
	}
	r.git("add", "--all")
	r.git("commit", "--quiet", "--message", "head")
	return r, base, r.git("rev-parse", "HEAD")
}

// readFile reads a file of a fixture or of a materialized tree.
func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name) //nolint:gosec // a path the test built
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// prepareClean runs Prepare in r's svc, hands the Worktree to check and
// then checks that Close leaves nothing behind.
func prepareClean(t *testing.T, r testRepo, base, head string, check func(*Worktree)) {
	t.Helper()
	tmp := t.TempDir()
	w, err := Prepare(t.Context(), filepath.Join(r.dir, "svc"), base, head, PrepareOptions{TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	check(w)
	if err := w.Close(); err != nil {
		t.Error(err)
	}
	assertClean(t, r, tmp)
}

// TestPrepareIgnoresHeadAttributes checks that a .gitattributes head adds
// under testdata cannot filter what travels: with ident, git would write
// $Id: <blob> $ into f.txt.
func TestPrepareIgnoresHeadAttributes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	r, base, head := planted(t, nil, map[string]string{
		"svc/a/testdata/.gitattributes": "* ident\n",
		"svc/a/testdata/f.txt":          "$Id$\n",
	}, nil)
	prepareClean(t, r, base, head, func(w *Tree) {
		got, err := os.ReadFile(filepath.Join(w.moduleDir, "a", "testdata", "f.txt"))
		if err != nil || string(got) != "$Id$\n" {
			t.Errorf("f.txt in the worktree = %q, %v; want head's $Id$, unexpanded", got, err)
		}
	})
}

// TestPrepareLiteralPathspecs checks that a copied path is never read as a
// pathspec: as magic, ":!x_test.go" alone would exclude itself and check
// out everything else from head, production code included. Glob characters
// alone, as in svc/[a]/x_test.go, can only reach test files that travel
// anyway.
func TestPrepareLiteralPathspecs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	r, base, head := planted(t, nil, map[string]string{
		":!x_test.go": "package x\n",
		"svc/a/a.go":  "package a // head's fix\n",
	}, nil)
	prepareClean(t, r, base, head, func(w *Tree) {
		if want := []string{":!x_test.go"}; !reflect.DeepEqual(w.Copied, want) {
			t.Fatalf("Copied = %q, want %q", w.Copied, want)
		}
		if got, err := os.ReadFile(filepath.Join(w.moduleDir, "a", "a.go")); err != nil || string(got) != "package a\n" {
			t.Errorf("a.go in the worktree = %q, %v; want the base's", got, err)
		}
	})
}

// TestPrepareSeesIgnoredSubmodules checks that a head .gitmodules with
// ignore = all cannot hide a gitlink head adds from the files that stay
// behind.
func TestPrepareSeesIgnoredSubmodules(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	r, base, head := planted(t, nil, map[string]string{
		".gitmodules": "[submodule \"sub\"]\n\tpath = svc/a/sub\n\turl = ./sub\n\tignore = all\n",
	}, func(r testRepo) {
		// git add --all keeps a gitlink whose directory exists, even empty.
		if err := os.MkdirAll(filepath.Join(r.dir, "svc", "a", "sub"), 0o750); err != nil {
			t.Fatal(err)
		}
		r.git("update-index", "--add", "--cacheinfo", "160000,"+r.git("rev-parse", "HEAD")+",svc/a/sub")
	})
	prepareClean(t, r, base, head, func(w *Tree) {
		if want := []string{"svc/a/sub"}; !reflect.DeepEqual(w.uncopied([]string{"example.com/svc/a"}), want) {
			t.Errorf("uncopied = %q, want %q: the gitlink stays behind", w.uncopied([]string{"example.com/svc/a"}), want)
		}
	})
}

// TestPrepareIgnoresCheckoutFilters plants in .git everything git's checkout
// would apply to what it writes, as a test at head can, checks with plain
// git that the plants really do mangle the base, and then that the tree
// holds every file byte for byte. --attr-source does not cover
// .git/info/attributes, so only staying out of the checkout path does.
func TestPrepareIgnoresCheckoutFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the smudge filter is a shell command")
	}
	t.Parallel()
	r, base, head := planted(t, nil, map[string]string{"svc/a/a_test.go": "package a\n"}, nil)
	r.git("config", "core.autocrlf", "true")
	r.git("config", "filter.poison.smudge", "sed s/package/POISON/")
	writeFiles(t, filepath.Join(r.dir, ".git"), map[string]string{"info/attributes": "*.go filter=poison text eol=crlf\n"})

	plain := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "--quiet", "--detach", plain, base)
	if got := readFile(t, filepath.Join(plain, "svc", "a", "a.go")); got != "POISON a\r\n" {
		t.Fatalf("plain git checkout wrote %q, want the filter and eol=crlf to mangle it", got)
	}
	r.git("worktree", "remove", "--force", plain)

	prepareClean(t, r, base, head, func(w *Tree) {
		for _, name := range []string{"a/a.go", "a/a_test.go"} {
			if got := readFile(t, filepath.Join(w.moduleDir, filepath.FromSlash(name))); got != "package a\n" {
				t.Errorf("%s in the tree = %q, want the blob %q", name, got, "package a\n")
			}
		}
	})
}

// TestPrepareMaterializesModes checks what a directory can hold of a tree:
// the executable bit, a symlink inside the tree, and what it cannot, a
// symlink out of it and a submodule's gitlink.
func TestPrepareMaterializesModes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlinks and the executable bit need a POSIX file system")
	}
	t.Parallel()
	const gitlink = "0123456789abcdef0123456789abcdef01234567" // no submodule is cloned
	r, base, head := planted(t, func(r testRepo) {
		writeFiles(t, r.dir, map[string]string{"svc/a/run.sh": "#!/bin/sh\necho hi\n", "svc/a/testdata/in.txt": "in\n"})
		if err := os.Chmod(filepath.Join(r.dir, "svc", "a", "run.sh"), 0o700); err != nil { //nolint:gosec // git records 100755 for a file the owner may execute
			t.Fatal(err)
		}
		for name, target := range map[string]string{"svc/a/link.txt": "testdata/in.txt", "svc/a/out.txt": "../../../etc/passwd"} {
			if err := os.Symlink(target, filepath.Join(r.dir, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(r.dir, "svc", "a", "sub"), 0o750); err != nil {
			t.Fatal(err)
		}
		r.git("add", "--all")
		r.git("update-index", "--add", "--cacheinfo", "160000,"+gitlink+",svc/a/sub")
	}, map[string]string{"svc/a/a_test.go": "package a\n"}, nil)

	prepareClean(t, r, base, head, func(w *Tree) {
		run, err := os.Lstat(filepath.Join(w.moduleDir, "a", "run.sh"))
		if err != nil || run.Mode().Perm()&0o100 == 0 {
			t.Errorf("run.sh = %v, %v; want it executable", run, err)
		}
		plain, err := os.Lstat(filepath.Join(w.moduleDir, "a", "a.go"))
		if err != nil || plain.Mode().Perm()&0o111 != 0 {
			t.Errorf("a.go = %v, %v; want it not executable", plain, err)
		}
		link, err := os.Readlink(filepath.Join(w.moduleDir, "a", "link.txt"))
		if err != nil || link != "testdata/in.txt" {
			t.Errorf("link.txt points to %q, %v; want testdata/in.txt", link, err)
		}
		if got := readFile(t, filepath.Join(w.moduleDir, "a", "link.txt")); got != "in\n" {
			t.Errorf("reading through link.txt gave %q, want the file it points to", got)
		}
		for _, name := range []string{"a/out.txt", "a/sub"} {
			if _, err := os.Lstat(filepath.Join(w.moduleDir, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s in the tree: %v, want it left out", name, err)
			}
		}
		if want := []string{"svc/a/out.txt", "svc/a/sub"}; !reflect.DeepEqual(w.Skipped, want) {
			t.Errorf("Skipped = %q, want %q", w.Skipped, want)
		}
	})
}

// TestPrepareStreamsLargeBlob checks the blob that does not fit in the
// memory one git cat-file --batch may hold, and travels on its own.
func TestPrepareStreamsLargeBlob(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository")
	}
	t.Parallel()
	want := bytes.Repeat([]byte("aval\n"), blobBudget/5+1)
	r, base, head := planted(t, func(r testRepo) {
		if err := os.WriteFile(filepath.Join(r.dir, "svc", "a", "big.bin"), want, 0o600); err != nil {
			t.Fatal(err)
		}
	}, map[string]string{"svc/a/a_test.go": "package a\n"}, nil)

	prepareClean(t, r, base, head, func(w *Tree) {
		got, err := os.ReadFile(filepath.Join(w.moduleDir, "a", "big.bin")) //nolint:gosec // the tree
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("big.bin = %d bytes, %v; want %d bytes, equal", len(got), err, len(want))
		}
	})
}
