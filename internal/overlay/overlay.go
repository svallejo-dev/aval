// Package overlay gathers fail-before evidence (ADR-0005 §2): it checks the
// base of a change out in a temporary git worktree, lays the change's test
// files over it and runs there the tests that own each obligation at head.
package overlay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// Target is an obligation to check at the base.
type Target struct {
	ID obligation.ID
	// Tests are the full names of the tests that own ID in the run at head,
	// as go test reports them: "TestOrder/ORD-F01_rejects_duplicates",
	// "TestSuite/TestX/ORD-F01", or "TestOrder/dup_sku" when an aval.req
	// attr binds it. Their first levels group the runs at the base.
	Tests []string
	// Packages are the import paths of the packages Tests live in at head.
	// ID is build_fail at the base only when one of them fails to build.
	Packages []string
	// After is ID's status at head. Any strength but none needs pass.
	After evidence.Status
	// Characterization is set when the requirement is marked
	// "**aval**: characterization", so passing at the base is expected.
	Characterization bool
}

// Options configures Run.
type Options struct {
	Timeout time.Duration // go test -timeout of each run; zero keeps go test's default
	Env     []string      // KEY=VALUE pairs added to go test's environment
	// TempDir holds the worktree. Empty means $RUNNER_TEMP when set, which
	// GitHub Actions empties after every job, else os.TempDir().
	TempDir string
}

// Result is what the base says about each target.
type Result struct {
	Base, Head string // the full commit SHAs compared
	// Copied lists the files laid over the base from head and Removed the
	// ones head deleted, repository-relative with "/" separators.
	Copied, Removed []string
	Obligations     []Obligation // one per target, in the same order
	Runs            []BaseRun    // one per top-level test, in order of first appearance
}

// Obligation is the fail-before evidence for one obligation.
type Obligation struct {
	ID obligation.ID
	// Before is ID's status at the base: gotest's Report.Status over the
	// runs of its tests, given its target's Packages.
	Before   evidence.Status
	After    evidence.Status   // the target's After
	Strength evidence.Strength // Strength(Before, After, Characterization)
}

// BaseRun is one go test run at the base.
type BaseRun struct {
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
	// with the *GitError or *gotest.ToolError that found out. Callers map it
	// to exit code 3.
	ErrToolMissing = errors.New("overlay: git and go are required")
	// ErrCleanup is wrapped when the worktree or its temporary directory
	// could not be removed. If nothing else failed, the Result is complete.
	ErrCleanup = errors.New("overlay: worktree not removed")
)

// Strength grades a base status as fail-before evidence (ADR-0005 §2): it
// is none unless the tests pass at head, and then strong when they failed
// at the base, weak when they did not build there, and characterization
// when they passed there on a requirement marked as such.
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

// Run checks base out in a temporary worktree of the repository dir is in,
// copies over it from head the added and modified *_test.go files and
// testdata/ files, removes those head deleted, and runs each target's tests
// there with `go test -json -count=1`, one run per top-level test. go test
// runs in the directory of the worktree that matches dir, so dir is the
// root of the Go module. base should be the merge-base of the change.
//
// Test files and testdata are copied from the whole diff, not only from
// the targets' packages: other packages' test files never build into the
// runs, and a test may read testdata shared across packages.
//
// The worktree is always removed, also when ctx ends: git steps that write
// it run to completion, and go test is stopped. Failing tests and packages
// that do not build are results, not errors. With no targets Run does
// nothing and returns a zero Result.
func Run(ctx context.Context, dir, base, head string, targets []Target, opts Options) (Result, error) {
	p, err := newPlan(targets)
	if err != nil || len(p.runs) == 0 {
		return Result{}, err
	}
	res, err := p.execute(ctx, repo{dir: dir}, base, head, opts)
	if errors.Is(err, exec.ErrNotFound) {
		err = fmt.Errorf("%w: %w", ErrToolMissing, err)
	}
	return res, err
}

func (p plan) execute(ctx context.Context, r repo, base, head string, opts Options) (res Result, err error) {
	if res.Base, err = r.commit(ctx, base); err != nil {
		return Result{}, err
	}
	if res.Head, err = r.commit(ctx, head); err != nil {
		return Result{}, err
	}
	prefix, err := r.prefix(ctx)
	if err != nil {
		return Result{}, err
	}
	if res.Copied, res.Removed, err = r.changes(ctx, res.Base, res.Head); err != nil {
		return Result{}, err
	}

	tmp, err := os.MkdirTemp(tempRoot(opts.TempDir), "aval-overlay-")
	if err != nil {
		return Result{}, fmt.Errorf("overlay: %w", err)
	}
	wt := repo{dir: filepath.Join(tmp, "base")}
	added := false
	defer func() {
		if cerr := r.cleanup(ctx, tmp, wt.dir, added); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	if err = r.addWorktree(ctx, wt.dir, res.Base); err != nil {
		return Result{}, err
	}
	added = true
	if err = wt.overlay(ctx, res.Head, res.Copied, res.Removed); err != nil {
		return Result{}, err
	}

	moduleDir := filepath.Join(wt.dir, filepath.FromSlash(prefix))
	reports := make(map[string]gotest.Report, len(p.runs))
	for _, pr := range p.runs {
		o := gotest.Options{Packages: pr.pkgs, Run: pr.pattern(), Count: 1, Timeout: opts.Timeout, Env: opts.Env}
		start := time.Now()
		rep, err := gotest.Run(ctx, moduleDir, o)
		if err != nil {
			return Result{}, fmt.Errorf("overlay: run %s at the base: %w", pr.test, err)
		}
		res.Runs = append(res.Runs, BaseRun{Test: pr.test, Options: o, Report: rep, Duration: time.Since(start)})
		reports[pr.test] = rep
	}
	for _, t := range p.targets {
		var merged gotest.Report
		for _, test := range t.tops {
			merged.Packages = append(merged.Packages, reports[test].Packages...)
			merged.Tests = append(merged.Tests, reports[test].Tests...)
		}
		before := merged.Status(t.ID, t.Packages...)
		res.Obligations = append(res.Obligations, Obligation{
			ID: t.ID, Before: before, After: t.After, Strength: Strength(before, t.After, t.Characterization),
		})
	}
	return res, nil
}

// tempRoot returns the directory the worktree goes in; "" means os.TempDir.
func tempRoot(dir string) string {
	if dir != "" {
		return dir
	}
	return os.Getenv("RUNNER_TEMP")
}
