// Package overlay gathers fail-before evidence (ADR-0005 §2): it checks the
// base of a change out in a temporary git worktree, lays the change's test
// files over it and runs there the tests that own each obligation at head,
// in one go test process per obligation and top-level test, so that a
// sibling's bug at the base cannot change another obligation's status.
//
// Prepare builds the worktree from git objects alone, and must run before
// any of head's tests: those could rewrite the working tree and .git. Only
// Run needs what the run at head found, the full names of the owners.
//
// Only *_test.go files and files under testdata/ travel from head. A test
// that reads data kept anywhere else fails at the base whether or not the
// behavior exists, so that failure is only weak evidence, with a Note
// naming the files. Test data goes in testdata/.
//
// Weak evidence can be gamed, which is why the gate only warns about it and
// shows it to humans: a sibling file that references a symbol head adds
// (var _ = NewThing) makes the package's test build fail at the base
// whatever its tests check.
package overlay

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/platform/git"
	"github.com/svallejo-dev/aval/internal/platform/gitenv"
)

// Target is an obligation to check at the base.
type Target struct {
	ID obligation.ID
	// Tests are the full names of the tests that own ID in the run at head,
	// as go test reports them: "TestOrder/ORD-F01_rejects_duplicates",
	// "TestSuite/TestX/ORD-F01", or "TestOrder/dup_sku" when an aval.req
	// attr binds it. Each of their top-level tests gets a run of its own.
	Tests []string
	// Packages are the import paths of the packages Tests live in at head.
	Packages []string
	// After is ID's status at head. Any strength but none needs pass.
	After evidence.Status
	// Characterization is set when the requirement is marked
	// "**aval**: characterization", so passing at the base is expected.
	Characterization bool
}

// PrepareOptions configures Prepare.
type PrepareOptions struct {
	// TempDir holds the worktree. Empty means $RUNNER_TEMP when set, which
	// GitHub Actions empties after every job, else os.TempDir().
	TempDir string
}

// RunOptions configures Run.
type RunOptions struct {
	Timeout time.Duration // go test -timeout of each run; zero keeps go test's default
	Env     []string      // KEY=VALUE pairs added to go test's environment
}

// Worktree is the base of a change with head's test files laid over it.
// It is not safe for concurrent use.
type Worktree struct {
	Base, Head string // the full commit SHAs
	// Copied lists the files laid over the base from head and Removed the
	// ones head deleted, repository-relative with "/" separators.
	Copied, Removed []string

	common    repo            // the repository's common directory, where Close runs git
	tmp       string          // the temporary directory that holds the worktree
	moduleDir string          // where go test runs: the worktree's counterpart of dir
	module    string          // the module path in head's go.mod
	root      string          // the module's directory in the repository, "" at the top
	others    []string        // non-Go files head adds or modifies outside testdata/
	added     map[string]bool // directories, like root, of the packages head adds
	closed    bool
}

// Result is what the base says about each target.
type Result struct {
	Obligations []Obligation // one per target, in the same order
	Runs        []BaseRun    // one per target and top-level test, in that order
	// Uncopied lists the files, neither Go nor testdata, that head adds or
	// modifies under the targets' package directories. They stayed behind.
	Uncopied []string
}

// Obligation is the fail-before evidence for one obligation.
type Obligation struct {
	ID obligation.ID
	// Before is ID's status at the base: gotest's Report.Status over its
	// runs, given its target's Packages.
	Before   evidence.Status
	After    evidence.Status // the target's After
	Strength evidence.Strength
	// Note says why Strength is not Strength(Before, After, Characterization),
	// or which package head adds failed to build at the base.
	Note string
}

// BaseRun is one go test run at the base.
type BaseRun struct {
	ID       obligation.ID
	Test     string         // the top-level test it selects from
	Options  gotest.Options // what gotest.Run got; Options.Args() is the command line
	Report   gotest.Report
	Duration time.Duration
}

var (
	// ErrInvalidTarget is wrapped by Run's error for targets it refuses.
	ErrInvalidTarget = errors.New("overlay: invalid target")
	// ErrRevision is wrapped when base or head does not name a commit.
	ErrRevision = errors.New("overlay: not a commit")
	// ErrToolMissing is wrapped when git or go is not on PATH, together
	// with the *git.Error or *gotest.ToolError that found out, or when git
	// is older than 2.40; git's errors wrap git.ErrToolMissing too. Callers
	// map it to exit code 3.
	ErrToolMissing = errors.New("overlay: git 2.40 or later and go are required")
	// ErrCleanup is wrapped when the worktree or its temporary directory
	// could not be removed.
	ErrCleanup = errors.New("overlay: worktree not removed")
	// ErrClosed is returned by Run after Close.
	ErrClosed = errors.New("overlay: worktree closed")
)

// Strength grades a base status as fail-before evidence by ADR-0005 §2's
// table alone: none unless the tests pass at head, and then strong when
// they failed at the base, weak when they did not build there, and
// characterization when they passed there on a requirement marked as such.
// Run refines strong and weak with what it knows about the change.
func Strength(before, after evidence.Status, characterization bool) evidence.Strength {
	switch {
	case after != evidence.Pass:
		return evidence.None
	case before == evidence.Fail:
		return evidence.Strong
	case before == evidence.BuildFail:
		return evidence.Weak
	case before == evidence.Pass && characterization:
		return evidence.Characterized
	}
	return evidence.None
}

// Prepare checks base out in a temporary worktree of the repository dir is
// in, deletes the test files and testdata head deleted, and copies over it
// from head's objects the added and modified *_test.go files and testdata/
// files. It reads everything else it needs from git now, so the caller may
// run head's tests afterwards. dir is the root of the Go module; base
// should be the merge-base of the change.
//
// Test files and testdata travel from the whole diff, not only from the
// targets' packages: other packages' test files never build into the runs,
// and a test may read testdata shared across packages.
//
// The caller must Close the Worktree, which works even once dir is gone.
// When Prepare fails, nothing is left to close. Git steps that write the
// worktree run to completion even if ctx ends, so git never leaves one
// half-written.
func Prepare(ctx context.Context, dir, base, head string, opts PrepareOptions) (*Worktree, error) {
	w, err := prepare(ctx, newRepo(dir), base, head, opts)
	return w, toolMissing(err)
}

func prepare(ctx context.Context, r repo, base, head string, opts PrepareOptions) (*Worktree, error) {
	if err := r.CheckVersion(ctx); err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	common, err := r.commonDir(ctx)
	if err != nil {
		return nil, err
	}
	w := &Worktree{common: common}
	if w.Base, err = r.commit(ctx, base); err != nil {
		return nil, err
	}
	if w.Head, err = r.commit(ctx, head); err != nil {
		return nil, err
	}
	if w.root, err = r.prefix(ctx); err != nil {
		return nil, err
	}
	if w.module, err = r.modulePath(ctx, w.Head, w.root); err != nil {
		return nil, err
	}
	c, err := r.changes(ctx, w.Base, w.Head)
	if err != nil {
		return nil, err
	}
	w.Copied, w.Removed, w.others = c.copy, c.remove, c.others

	tmpRoot, err := tempRoot(opts.TempDir)
	if err != nil {
		return nil, err
	}
	if w.tmp, err = os.MkdirTemp(tmpRoot, "aval-overlay-"); err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	wt := newRepo(filepath.Join(w.tmp, "base"))
	w.moduleDir = filepath.Join(wt.dir, filepath.FromSlash(w.root))
	if err := r.addWorktree(ctx, wt.dir, w.Base); err != nil {
		return nil, errors.Join(err, common.cleanup(w.tmp, wt.dir, false))
	}
	w.added = newPackages(wt.dir, c.newGo)
	if err := wt.overlay(ctx, w.Head, w.Copied, w.Removed); err != nil {
		return nil, errors.Join(err, w.Close())
	}
	return w, nil
}

// Run runs each target's tests at the base with `go test -json -count=1`,
// one run per top-level test of its Tests, without the git variables of the
// caller's repository in the environment, and grades what they show.
// Failing tests and packages that do not build are results, not errors.
// When ctx ends, go test is stopped and Run returns ctx's error. Each Run
// sees whatever earlier runs' tests left in the worktree.
func (w *Worktree) Run(ctx context.Context, targets []Target, opts RunOptions) (Result, error) {
	if w.closed {
		return Result{}, ErrClosed
	}
	plan, err := newPlan(targets)
	if err != nil {
		return Result{}, err
	}
	var res Result
	var all []string
	env := gitenv.Clean(os.Environ())
	for _, t := range plan {
		var merged gotest.Report
		for _, run := range t.runs {
			o := gotest.Options{
				Packages: t.Packages, Run: run.pattern, Count: 1,
				Timeout: opts.Timeout, Env: opts.Env, Environ: env,
			}
			start := time.Now()
			rep, err := gotest.Run(ctx, w.moduleDir, o)
			if err != nil {
				return Result{}, toolMissing(fmt.Errorf("overlay: run %s at the base for %s: %w", run.test, t.ID, err))
			}
			res.Runs = append(res.Runs, BaseRun{ID: t.ID, Test: run.test, Options: o, Report: rep, Duration: time.Since(start)})
			merged.Packages = append(merged.Packages, rep.Packages...)
			merged.Tests = append(merged.Tests, rep.Tests...)
		}
		res.Obligations = append(res.Obligations, w.grade(t.Target, merged))
		all = append(all, t.Packages...)
	}
	res.Uncopied = w.uncopied(all)
	return res, nil
}

// Close removes the worktree and its temporary directory. It is safe to
// call more than once; after the first, it does nothing.
func (w *Worktree) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.common.cleanup(w.tmp, filepath.Join(w.tmp, "base"), true)
}

// newPackages returns the repository-relative directories of files, the
// non-test Go files head adds, in which the base worktree wt holds no
// non-test Go file: head adds those packages.
func newPackages(wt string, files []string) map[string]bool {
	added, checked := make(map[string]bool), make(map[string]bool)
	for _, f := range files {
		dir := path.Dir(f)
		if dir == "." {
			dir = ""
		}
		if checked[dir] {
			continue
		}
		checked[dir] = true
		entries, _ := os.ReadDir(filepath.Join(wt, filepath.FromSlash(dir))) // an error means no such directory
		if !slices.ContainsFunc(entries, func(e os.DirEntry) bool { return goFile(e.Name()) }) {
			added[dir] = true
		}
	}
	return added
}

// toolMissing marks errors that come from a missing git or go, or an old git.
func toolMissing(err error) error {
	if (errors.Is(err, exec.ErrNotFound) || errors.Is(err, git.ErrToolMissing)) && !errors.Is(err, ErrToolMissing) {
		return fmt.Errorf("%w: %w", ErrToolMissing, err)
	}
	return err
}

// tempRoot returns the absolute directory the worktree goes in: dir, else
// $RUNNER_TEMP, else os.TempDir(). Absolute, because git runs elsewhere.
func tempRoot(dir string) (string, error) {
	abs, err := filepath.Abs(cmp.Or(dir, os.Getenv("RUNNER_TEMP"), os.TempDir()))
	if err != nil {
		return "", fmt.Errorf("overlay: %w", err)
	}
	return abs, nil
}
