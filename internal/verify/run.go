package verify

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/baseline"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/overlay"
	"github.com/svallejo-dev/aval/internal/trace"
)

// state is one obligation's evidence while the runs of phase 2 fill it in.
type state struct {
	ob   evidence.Obligation
	id   obligation.ID
	pkgs []string // the packages its tests live in at head
	// isolated marks an obligation ADR-0005 §3 re-runs on its own at head:
	// one that needs a test and that fail-before does not cover.
	isolated bool
}

// runHead runs phase 2: head's own code. The full run comes first, because
// the fail-before and the isolated regressions select tests by the names it
// reports (ADR-0005 §2.3); then the base runs, the isolated runs and the lint
// ratchet, one process at a time.
func (c *collector) runHead(ctx context.Context) error {
	if err := c.headRun(ctx); err != nil {
		return err
	}
	c.matrix = trace.Build(c.headSpecs, c.headDecls, &trace.Runtime{Report: c.report, Module: c.module})
	obs, targets := c.plan()
	if err := c.failBefore(ctx, obs, targets); err != nil {
		return err
	}
	if err := c.regressions(ctx, obs); err != nil {
		return err
	}
	c.ev.Input.Obligations = make([]evidence.Obligation, 0, len(obs))
	for _, s := range obs {
		c.ev.Input.Obligations = append(c.ev.Input.Obligations, s.ob)
	}
	lint, err := c.ratchet(ctx)
	if err != nil {
		return err
	}
	c.ev.Input.Lint = lint
	return nil
}

// headRun runs every test of head once.
func (c *collector) headRun(ctx context.Context) error {
	o := gotest.Options{Count: 1, Timeout: c.o.TestTimeout, Env: c.o.Env, Environ: c.testEnv()}
	start := time.Now()
	rep, err := gotest.Run(ctx, c.ev.Root, o)
	if err != nil {
		return fmt.Errorf("verify: run the tests at head: %w", err)
	}
	c.report = rep
	c.check("go test", command(o), rep.ExitCode, time.Since(start), reportStatus(rep))
	return nil
}

// plan turns the traceability matrix and the run at head into the obligations
// the gate judges — those of the delta, and every other one a test is bound
// to — and into the fail-before targets among them (ADR-0005 §2).
func (c *collector) plan() ([]*state, []overlay.Target) {
	owners := c.report.Obligations()
	var obs []*state
	seen := make(map[obligation.ID]bool, len(c.deltas))
	for _, row := range c.matrix.Rows {
		id, err := obligation.Parse(row.ID)
		if err != nil {
			continue // openspec.Check reports what is not an ID
		}
		d, inDelta := c.deltas[id]
		tests, pkgs := namesOf(owners[id])
		if !inDelta && len(tests) == 0 {
			continue // nothing ran and nothing changed: the matrix says it all
		}
		seen[id] = true
		characterization := row.Characterization
		if inDelta && d.delta != evidence.Unchanged {
			characterization = d.req.Characterization // head's own wording wins
		}
		obs = append(obs, newState(id, d, source(row.Path, row.Line, row.ID+" "+row.Title),
			characterization, tests, pkgs, row.Runtime))
	}
	// An obligation of the delta that no spec at head defines still has to be
	// judged: openspec.Check reports why it is not there, and the gate needs
	// its delta to ask for the evidence.
	for _, id := range slices.SortedFunc(maps.Keys(c.deltas), byID) {
		d := c.deltas[id]
		if seen[d.id] {
			continue
		}
		tests, pkgs := namesOf(owners[d.id])
		obs = append(obs, newState(d.id, d, source(d.req.Path, d.req.Line, cmp.Or(d.req.Name, d.id.String())),
			d.req.Characterization, tests, pkgs, c.report.Status(d.id, pkgs...)))
	}

	var targets []overlay.Target
	for _, s := range obs {
		switch {
		case len(s.ob.Tests) == 0 || s.id.Kind().Policy() != obligation.RequireTest:
		case needsFailBefore(deltaOf{id: s.id, delta: s.ob.Delta}):
			targets = append(targets, overlay.Target{
				ID: s.id, Tests: s.ob.Tests, Packages: s.pkgs,
				After: s.ob.After, Characterization: s.ob.Characterization,
			})
		default:
			s.isolated = true
		}
	}
	return obs, targets
}

// newState starts an obligation's evidence: what the specs and the run at
// head say. Before is n/a until a fail-before run says otherwise, and the
// strength stays none, which is the absence of acceptable evidence.
func newState(id obligation.ID, d deltaOf, src string, characterization bool, tests, pkgs []string, after evidence.Status) *state {
	return &state{
		id: id, pkgs: pkgs,
		ob: evidence.Obligation{
			ID: id.String(), Kind: id.Kind().String(), Source: src,
			Delta: cmp.Or(d.delta, evidence.Unchanged), Characterization: characterization,
			Tests: tests, Before: evidence.NotApply, After: after, Strength: evidence.None,
		},
	}
}

// failBefore lays head's test files over the base in a temporary worktree and
// runs the targets' tests there (ADR-0005 §2).
//
// The worktree is built here, after head's own tests and not in phase 1,
// although overlay builds it from git objects either way: one standing while
// head's tests run is a tree they could rewrite, and a failure they planted
// there reads as strong evidence. Nothing about it is read from git later.
func (c *collector) failBefore(ctx context.Context, obs []*state, targets []overlay.Target) error {
	if len(targets) == 0 {
		return nil
	}
	w, err := overlay.Prepare(ctx, c.ev.Root, c.ev.Base, c.ev.Head, overlay.PrepareOptions{TempDir: c.tmp})
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	c.work = w
	res, err := w.Run(ctx, targets, overlay.RunOptions{Timeout: c.o.TestTimeout, Env: c.o.Env})
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	for _, run := range res.Runs {
		c.check("fail-before "+run.ID.String()+" "+run.Test, command(run.Options),
			run.Report.ExitCode, run.Duration, reportStatus(run.Report))
	}
	for _, o := range res.Obligations {
		for _, s := range obs {
			if s.id == o.ID {
				s.ob.Before, s.ob.Strength, s.ob.Note = o.Before, o.Strength, o.Note
			}
		}
	}
	return nil
}

// regressions runs at head, on their own, the tests of every obligation that
// needs one and that fail-before does not cover: one go test process per
// top-level test and ID, so that neither a sibling subtest nor an earlier
// Test can change its outcome (ADR-0005 §3). They have to pass, and the worse
// of the isolated and the full run is what the gate sees.
func (c *collector) regressions(ctx context.Context, obs []*state) error {
	for _, s := range obs {
		if !s.isolated {
			continue
		}
		for _, r := range runsOf(s.id, s.ob.Tests) {
			o := gotest.Options{
				Packages: s.pkgs, Run: r.pattern, Count: 1,
				Timeout: c.o.TestTimeout, Env: c.o.Env, Environ: c.testEnv(),
			}
			start := time.Now()
			rep, err := gotest.Run(ctx, c.ev.Root, o)
			if err != nil {
				return fmt.Errorf("verify: run %s at head for %s: %w", r.test, s.id, err)
			}
			c.check("regression "+s.id.String()+" "+r.test, command(o), rep.ExitCode, time.Since(start), reportStatus(rep))
			s.ob.After = worse(s.ob.After, rep.Status(s.id, s.pkgs...))
		}
	}
	return nil
}

// unboundFailures returns the leaf failures of tests bound to no obligation:
// a failing test with no failing test below it in the same run, which is what
// a baseline lists (ADR-0005 §7). go test fails every parent of a failing
// subtest, so only the leaves are judged, and the match is exact.
func unboundFailures(r gotest.Report) []baseline.Test {
	parent := make(map[[2]string]bool)
	for _, t := range r.Tests {
		if t.Status != evidence.Fail {
			continue
		}
		for name := t.Name; ; {
			i := strings.LastIndexByte(name, '/')
			if i < 0 {
				break
			}
			name = name[:i]
			parent[[2]string{t.Package, name}] = true
		}
	}
	var out []baseline.Test
	for _, t := range r.Tests {
		if t.Status == evidence.Fail && len(t.Bindings) == 0 && !parent[[2]string{t.Package, t.Name}] {
			out = append(out, baseline.Test{Package: t.Package, Test: t.Name})
		}
	}
	return out
}

// namesOf returns the full names of the tests that own an obligation at head
// and the packages they live in, each without repeats.
func namesOf(owners []gotest.TestOutcome) (tests, pkgs []string) {
	for _, t := range owners {
		if !slices.Contains(tests, t.Name) {
			tests = append(tests, t.Name)
		}
		if !slices.Contains(pkgs, t.Package) {
			pkgs = append(pkgs, t.Package)
		}
	}
	return tests, pkgs
}

// isolatedRun is one process of ADR-0005 §2.3: the tests of one obligation
// under one top-level test.
type isolatedRun struct {
	test    string // the top-level test
	pattern string // the -run pattern that selects the obligation's tests under it
}

// runsOf groups the test names of an obligation by their top-level test and
// builds each one's -run pattern. It mirrors what overlay plans for the base
// runs, which is not exported.
func runsOf(id obligation.ID, names []string) []isolatedRun {
	var runs []isolatedRun
	byTop := make(map[string][]string, len(names))
	for _, name := range names {
		top, _, _ := strings.Cut(name, "/")
		if _, ok := byTop[top]; !ok {
			runs = append(runs, isolatedRun{test: top})
		}
		byTop[top] = append(byTop[top], name)
	}
	for i, r := range runs {
		var alts []string
		for _, name := range byTop[r.test] {
			if alt := selector(id, name); !slices.Contains(alts, alt) {
				alts = append(alts, alt)
			}
		}
		runs[i].pattern = strings.Join(alts, "|")
	}
	return runs
}

// selector selects name down to the outermost level that carries id, each
// level quoted and anchored, and matches the duplicate and "_title" suffixes
// go test adds: ^TestSuite$/^TestX$/^ORD-F01([_#]|$).
func selector(id obligation.ID, name string) string {
	var levels []string
	for level := range strings.SplitSeq(name, "/") {
		if got, ok := obligation.FromTestSegment(level); ok && got == id {
			return strings.Join(append(levels, "^"+regexp.QuoteMeta(id.String())+"([_#]|$)"), "/")
		}
		levels = append(levels, "^"+regexp.QuoteMeta(level)+"$")
	}
	return strings.Join(levels, "/")
}

// rank orders statuses from best to worst. A package that did not build ran
// none of its tests.
var rank = map[evidence.Status]int{
	evidence.Pass:      1,
	evidence.Skipped:   2,
	evidence.NotRun:    3,
	evidence.Fail:      4,
	evidence.BuildFail: 5,
}

// worse returns the worse of a and b. A status aval does not know, the empty
// one included, ranks worst: it fails closed, never as a pass.
func worse(a, b evidence.Status) evidence.Status {
	if rankOf(b) > rankOf(a) {
		return b
	}
	return a
}

func rankOf(s evidence.Status) int {
	if r, ok := rank[s]; ok {
		return r
	}
	return len(rank) + 1
}

// reportStatus is what one go test run did as a whole: build_fail when a
// package did not build, else its exit status.
func reportStatus(r gotest.Report) evidence.Status {
	if slices.ContainsFunc(r.Packages, func(p gotest.Package) bool { return p.Status == evidence.BuildFail }) {
		return evidence.BuildFail
	}
	return statusOf(r.ExitCode == 0)
}

// byID orders obligation IDs, so that a collection is deterministic.
func byID(a, b obligation.ID) int { return strings.Compare(a.String(), b.String()) }
