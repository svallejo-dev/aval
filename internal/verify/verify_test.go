package verify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/svallejo-dev/aval/internal/baseline"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/hook"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
)

func TestMain(m *testing.M) {
	// A git hook exports these; they would point the fixtures' git commands
	// at the hooked repository instead of their temporary one.
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"} {
		if err := os.Unsetenv(v); err != nil {
			panic(err)
		}
	}
	goleak.VerifyTestMain(m)
}

// goEnv keeps the developer's workspace, flags, toolchain and proxy out of the
// runs in the fixture modules.
var goEnv = []string{"GOWORK=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "GOPROXY=off"}

// okValidate stands in for openspec.Validate, so that no test installs the
// OpenSpec CLI.
func okValidate(_ context.Context, root, _ string) (openspec.Report, error) {
	return openspec.Report{Root: root}, nil
}

// parseIDs parses obligation IDs a test names.
func parseIDs(t *testing.T, ids []string) []obligation.ID {
	t.Helper()
	out := make([]obligation.ID, 0, len(ids))
	for _, s := range ids {
		id, err := obligation.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// repo is a git repository built for one test, out of the developer's git
// configuration.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", args...) //nolint:gosec // a test helper with fixed arguments
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

// commit writes files, removes the paths of gone and commits everything.
func (r *repo) commit(msg string, files map[string]string, gone ...string) string {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	for _, name := range gone {
		if err := os.RemoveAll(filepath.Join(r.dir, filepath.FromSlash(name))); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "--all")
	r.git("commit", "--quiet", "--message", msg)
	return r.git("rev-parse", "HEAD")
}

// assertClean checks that Collect left no worktree and nothing in tmp.
func assertClean(t *testing.T, r *repo, tmp string) {
	t.Helper()
	if n := strings.Count("\n"+r.git("worktree", "list", "--porcelain"), "\nworktree "); n != 1 {
		t.Errorf("git worktree list shows %d worktrees, want only the repository's own", n)
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) > 0 {
		t.Errorf("temporary directory holds %v (%v), want it empty", entries, err)
	}
}

const policy = `version: 1
context: ORD
mode: enforce
tierDefault: 1
openspec:
  version: 1.13.1
paths:
  dx:
    - docs/**
    - .github/**
  feat:
    - orders/**
    - flaky/**
    - meddle/**
    - openspec/**
  seam:
    - go.mod
`

const mainSpec = `# orders Specification

## Purpose
Governs how the orders service totals an order and refunds against that total.

## Requirements

### Requirement: ORD-F01 Total adds the lines
The system SHALL total an order as the sum of its lines.

#### Scenario: Two lines
- **WHEN** an order has lines 2 and 3
- **THEN** its total is 5

### Requirement: ORD-F04 Rounding stays as it is
**aval**: characterization
The system SHALL round an amount down to the nearest ten.

#### Scenario: Seventeen
- **WHEN** the amount is 17
- **THEN** the rounded amount is 10
`

// baseFiles is the fixture repository at the base: two obligations, one of
// them a characterization, a test failing since before aval and a baseline
// that says so.
func baseFiles() map[string]string {
	return map[string]string{
		"go.mod":                        "module example.com/svc\n\ngo 1.27\n",
		"aval.yaml":                     policy,
		"openspec/specs/orders/spec.md": mainSpec,
		"docs/design.md":                "# design\n",
		".aval/baseline.json":           "{\n  \"version\": 1,\n  \"failing\": [{\"package\": \"example.com/svc/flaky\", \"test\": \"TestOld\"}]\n}\n",
		"orders/orders.go": `package orders

// Total sums the lines of an order.
func Total(lines []int) int {
	sum := 0
	for _, l := range lines {
		sum += l
	}
	return sum
}

// Refund lowers a total by amount. The base gets this wrong.
func Refund(total, amount int) int { return total }

// Round rounds down to the nearest ten.
func Round(n int) int { return n / 10 * 10 }
`,
		"orders/orders_test.go": `package orders

import "testing"

func TestTotal(t *testing.T) {
	t.Run("ORD-F01 adds the lines", func(t *testing.T) {
		if got := Total([]int{2, 3}); got != 5 {
			t.Fatalf("got %d, want 5", got)
		}
	})
	t.Run("ORD-F04 rounds down", func(t *testing.T) {
		if got := Round(17); got != 10 {
			t.Fatalf("got %d, want 10", got)
		}
	})
}
`,
		"flaky/flaky_test.go": `package flaky

import "testing"

func TestOld(t *testing.T) { t.Fatal("failing since before aval") }
`,
	}
}

// headFiles is the pull request: a change that adds two obligations, one that
// fails at the base and one that already passed there, a skip slipped into a
// bound test outside the delta, a new unbound failure, an edit of the policy,
// and a test that rewrites the repository while it runs.
func headFiles() map[string]string {
	return map[string]string{
		"aval.yaml":                              strings.Replace(policy, "mode: enforce", "mode: observe", 1),
		"openspec/changes/add-refunds/aval.yaml": "version: 1\ntier: 1\nowner: \"@orders\"\n",
		"openspec/changes/add-refunds/specs/orders/spec.md": `## ADDED Requirements

### Requirement: ORD-F02 Refund lowers the total
The system SHALL lower the order total by the refunded amount.

#### Scenario: Refund of four
- **WHEN** an order with total 10 is refunded 4
- **THEN** its total is 6

### Requirement: ORD-F03 Total of nothing is zero
The system SHALL total an order without lines as zero.

#### Scenario: No lines
- **WHEN** an order has no lines
- **THEN** its total is zero
`,
		"orders/orders.go": `package orders

// Total sums the lines of an order.
func Total(lines []int) int {
	sum := 0
	for _, l := range lines {
		sum += l
	}
	return sum
}

// Refund lowers a total by amount.
func Refund(total, amount int) int { return total - amount }

// Round rounds down to the nearest ten.
func Round(n int) int { return n / 10 * 10 }
`,
		"orders/orders_test.go": `package orders

import "testing"

func TestTotal(t *testing.T) {
	t.Run("ORD-F01 adds the lines", func(t *testing.T) {
		if got := Total([]int{2, 3}); got != 5 {
			t.Fatalf("got %d, want 5", got)
		}
	})
	t.Run("ORD-F04 rounds down", func(t *testing.T) {
		t.Skip("flaky, looking into it")
		if got := Round(17); got != 10 {
			t.Fatalf("got %d, want 10", got)
		}
	})
	t.Run("ORD-F02 refund lowers the total", func(t *testing.T) {
		if got := Refund(10, 4); got != 6 {
			t.Fatalf("got %d, want 6", got)
		}
	})
	t.Run("ORD-F03 total of nothing is zero", func(t *testing.T) {
		if got := Total(nil); got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})
}
`,
		"flaky/flaky_test.go": `package flaky

import "testing"

func TestOld(t *testing.T) { t.Fatal("failing since before aval") }

func TestNew(t *testing.T) { t.Fatal("failing since this pull request") }
`,
		// The pull request's code runs in the same job and rewrites what aval
		// reads from git (ADR-0005 §1). Every phase-1 answer must already be
		// in hand by the time this runs. It leaves the production code alone:
		// the isolated runs of §3 have to run after head's tests, so a test
		// that breaks the build breaks them too, which is the compromised
		// runner ADR-0005 leaves outside v0.
		"meddle/meddle_test.go": `package meddle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeddle(t *testing.T) {
	for name, content := range map[string]string{
		"openspec/specs/orders/spec.md": "### Requirement: not a requirement\n",
		"aval.yaml":                     "version: 9\n",
		".gitattributes":                "* -diff\n",
		".git/info/exclude":             "*\n",
	} {
		p := filepath.Join("..", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
`,
	}
}

// TestCollect gathers a tier-1 pull request whose evidence covers every shape
// ADR-0005 asks for, and checks that head's own code cannot change any of it.
func TestCollect(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r := newRepo(t)
	base := r.commit("base", baseFiles())
	head := r.commit("head", headFiles())
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Repo: "svallejo-dev/svc", AvalVersion: "test",
		Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)

	if ev.Base != base || ev.Head != head {
		t.Errorf("range = %s..%s, want %s..%s", ev.Base, ev.Head, base, head)
	}
	// The policy is the base's, although head edited it.
	if p := ev.Input.Policy; p == nil || p.Mode != manifest.Enforce {
		t.Fatalf("Policy = %+v, want the base's, with mode enforce", p)
	}
	if got, want := ev.Input.Tier(), manifest.Tier(1); got != want {
		t.Errorf("Tier = %d, want %d", got, want)
	}
	if want := []string{"add-refunds"}; !reflect.DeepEqual(ev.Changes, want) {
		t.Errorf("Changes = %q, want %q", ev.Changes, want)
	}
	if len(ev.Input.Scope) != 1 || ev.Input.Scope[0].Family != evidence.FamilyFeat {
		t.Errorf("Scope = %+v, want one feat commit", ev.Input.Scope)
	}
	if ev.Input.Validation == nil {
		t.Error("Validation = nil: the pull request touches openspec/, so validate must run")
	}

	// The specs aval read are the ones git holds, not the ones the tests left.
	if len(ev.Input.SpecFindings) != 0 {
		t.Errorf("SpecFindings = %v, want none", ev.Input.SpecFindings)
	}

	want := map[string]struct {
		delta            evidence.Delta
		before, after    evidence.Status
		strength         evidence.Strength
		characterization bool
	}{
		// Added, fails at the base because head's fix does not travel.
		"ORD-F02": {evidence.Added, evidence.Fail, evidence.Pass, evidence.Strong, false},
		// Added, but the behavior was already there: no evidence.
		"ORD-F03": {evidence.Added, evidence.Pass, evidence.Pass, evidence.None, false},
		// Outside the delta: no fail-before, and §3 re-ran it on its own.
		"ORD-F01": {evidence.Unchanged, evidence.NotApply, evidence.Pass, evidence.None, false},
		// Outside the delta and now skipped: the gate blocks on after.
		"ORD-F04": {evidence.Unchanged, evidence.NotApply, evidence.Skipped, evidence.None, true},
	}
	if len(ev.Input.Obligations) != len(want) {
		t.Errorf("Obligations = %+v, want %d of them", ev.Input.Obligations, len(want))
	}
	for _, o := range ev.Input.Obligations {
		w, ok := want[o.ID]
		if !ok {
			t.Errorf("unexpected obligation %+v", o)
			continue
		}
		if o.Delta != w.delta || o.Before != w.before || o.After != w.after ||
			o.Strength != w.strength || o.Characterization != w.characterization {
			t.Errorf("%s = %+v; want delta=%s %s→%s strength=%s characterization=%v",
				o.ID, o, w.delta, w.before, w.after, w.strength, w.characterization)
		}
		if len(o.Tests) == 0 || !strings.HasPrefix(o.Source, "openspec/") {
			t.Errorf("%s: tests = %q, source = %q; want both filled from the specs", o.ID, o.Tests, o.Source)
		}
	}

	// Tamper: the skip slipped into a bound test outside its delta, and the
	// edit of the policy.
	var kinds []string
	for _, f := range ev.Input.Tamper {
		kinds = append(kinds, string(f.Kind)+" "+f.ID)
	}
	slices.Sort(kinds)
	if w := []string{"policy_edited ", "skip_added ORD-F04"}; !reflect.DeepEqual(kinds, w) {
		t.Errorf("Tamper = %q, want %q", kinds, w)
	}

	// Unbound leaf failures, for the gate to compare with the base baseline.
	wantFailures := []baseline.Test{
		{Package: "example.com/svc/flaky", Test: "TestOld"},
		{Package: "example.com/svc/flaky", Test: "TestNew"},
	}
	got := slices.Clone(ev.Input.UnboundFailures)
	slices.SortFunc(got, func(a, b baseline.Test) int { return strings.Compare(a.Test, b.Test) })
	slices.SortFunc(wantFailures, func(a, b baseline.Test) int { return strings.Compare(a.Test, b.Test) })
	if !reflect.DeepEqual(got, wantFailures) {
		t.Errorf("UnboundFailures = %+v, want %+v", got, wantFailures)
	}
	if !ev.Input.Baseline.Contains("example.com/svc/flaky", "TestOld") ||
		ev.Input.Baseline.Contains("example.com/svc/flaky", "TestNew") {
		t.Errorf("Baseline = %+v, want it to cover TestOld and not TestNew", ev.Input.Baseline)
	}
	if ev.Input.Lint != (gate.Lint{}) {
		t.Errorf("Lint = %+v, want it off: the base has no %s", ev.Input.Lint, lintConfig)
	}

	// Every run is in the bundle, the isolated ones included.
	var names []string
	for _, ch := range ev.Checks {
		names = append(names, ch.Name)
	}
	for _, name := range []string{"go test", "openspec validate", "fail-before ORD-F02 TestTotal", "regression ORD-F01 TestTotal"} {
		if !slices.Contains(names, name) {
			t.Errorf("checks = %q, want one named %q", names, name)
		}
	}

	b := ev.Bundle(gate.Decide(ev.Input))
	if err := b.Validate(); err != nil {
		t.Errorf("bundle: %v", err)
	}
	if b.Verdict.Result != evidence.ResultBlock {
		t.Errorf("verdict = %s, want block", b.Verdict.Result)
	}
	var codes []string
	for _, reason := range b.Verdict.Reasons {
		codes = append(codes, reason.Code)
	}
	for _, code := range []string{gate.CodeTamper, gate.CodeRegression, gate.CodeAfterNotPassing, gate.CodeFailBeforeMissing} {
		if !slices.Contains(codes, code) {
			t.Errorf("reasons = %q, want one with code %q", codes, code)
		}
	}
}

// TestCollectTier0 gathers a pull request that only touches dx paths, checks
// that it passes, and then that Save leaves a bundle the gate can read and a
// status the agent hooks accept.
func TestCollectTier0(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r := newRepo(t)
	files := baseFiles()
	delete(files, "flaky/flaky_test.go") // nothing may fail in a clean pull request
	delete(files, ".aval/baseline.json")
	base := r.commit("base", files)
	head := r.commit("head", map[string]string{"docs/design.md": "# design\n\nA second paragraph.\n"})
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Repo: "svallejo-dev/svc",
		Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)

	if got := ev.Input.Tier(); got != 0 {
		t.Errorf("Tier = %d, want 0: the pull request only touches dx paths", got)
	}
	if len(ev.Input.Scope) != 1 || ev.Input.Scope[0].Family != evidence.FamilyDX {
		t.Errorf("Scope = %+v, want one dx commit", ev.Input.Scope)
	}
	if ev.Input.Validation != nil {
		t.Error("Validation: the pull request does not touch openspec/, so validate must not run")
	}
	for _, ch := range ev.Checks {
		if strings.HasPrefix(ch.Name, "fail-before") {
			t.Errorf("check %q: no obligation of the delta needs fail-before", ch.Name)
		}
	}
	verdict := gate.Decide(ev.Input)
	if verdict.Result != evidence.ResultPass {
		t.Fatalf("verdict = %+v, want pass", verdict)
	}

	b := ev.Bundle(verdict)
	if err := ev.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f, err := os.Open(filepath.Join(r.dir, filepath.FromSlash(EvidencePath(head))))
	if err != nil {
		t.Fatalf("the bundle is not where the gate looks for it: %v", err)
	}
	defer f.Close()
	saved, err := evidence.Parse(f)
	if err != nil || saved.Head != head || saved.AvalVersion != "devel" {
		t.Errorf("Parse(saved) = %+v, %v; want the bundle for %s", saved, err, head)
	}

	// The status is keyed by the working tree, which verify's own output does
	// not change: the hook must accept it as it is.
	_, key, err := hook.CurrentKey(t.Context(), r.dir)
	if err != nil {
		t.Fatal(err)
	}
	status, err := hook.ReadStatus(r.dir)
	if err != nil || status.Key != key || !status.Passed {
		t.Errorf("status = %+v, %v; want the key %+v and passed", status, err, key)
	}
}

// TestCollectSpecFindings checks that only new findings count, and that a
// change this pull request archived keeps the path the base knew it by
// (ADR-0005 §4).
func TestCollectSpecFindings(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	unknown := func(id string) string {
		return "## MODIFIED Requirements\n\n### Requirement: " + id + " Nothing defines it\nThe system SHALL do something.\n\n" +
			"#### Scenario: Any\n- **WHEN** it happens\n- **THEN** it holds\n"
	}
	r := newRepo(t)
	files := baseFiles()
	delete(files, "flaky/flaky_test.go")
	delete(files, ".aval/baseline.json")
	files["openspec/changes/add-x/specs/orders/spec.md"] = unknown("ORD-F50")
	base := r.commit("base", files)
	head := r.commit("head", map[string]string{
		"openspec/changes/archive/2026-01-01-add-x/specs/orders/spec.md": unknown("ORD-F50"),
		"openspec/changes/add-y/specs/orders/spec.md":                    unknown("ORD-F60"),
	}, "openspec/changes/add-x")
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)
	if len(ev.Input.SpecFindings) != 1 {
		t.Fatalf("SpecFindings = %v, want only the one add-y brings", ev.Input.SpecFindings)
	}
	f := ev.Input.SpecFindings[0]
	if f.Rule != openspec.RuleUnknownID || !strings.Contains(f.Path, "add-y") || !strings.Contains(f.Message, "ORD-F60") {
		t.Errorf("finding = %+v, want the unknown ID of add-y", f)
	}
	if ids := ev.Changes; !slices.Contains(ids, "add-x") || !slices.Contains(ids, "add-y") {
		t.Errorf("Changes = %q, want both changes the range touches", ids)
	}
}

func TestCollectUsageErrors(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	base := r.commit("base", map[string]string{"docs/x.md": "x\n"})
	head := r.commit("head", map[string]string{"docs/x.md": "y\n"})
	invalid := newRepo(t)
	badBase := invalid.commit("base", map[string]string{"aval.yaml": "version: 9\n"})
	badHead := invalid.commit("head", map[string]string{"docs/x.md": "y\n"})

	for _, tt := range []struct {
		name            string
		dir, base, head string
		wantErr         error
		wantMessagePart string
	}{
		{name: "no such revision", dir: r.dir, base: "nope", head: head, wantErr: ErrUsage, wantMessagePart: "names no commit"},
		{name: "a revision that is a flag", dir: r.dir, base: "-h", head: head, wantErr: ErrUsage, wantMessagePart: "not a revision"},
		{name: "no head", dir: r.dir, base: base, head: "", wantErr: ErrUsage, wantMessagePart: "not a revision"},
		{name: "invalid base policy", dir: invalid.dir, base: badBase, head: badHead, wantErr: ErrUsage, wantMessagePart: "aval.yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Collect(t.Context(), Options{Dir: tt.dir, Base: tt.base, Head: tt.head, TempDir: t.TempDir()})
			if !errors.Is(err, tt.wantErr) || !strings.Contains(err.Error(), tt.wantMessagePart) {
				t.Errorf("err = %v, want %v mentioning %q", err, tt.wantErr, tt.wantMessagePart)
			}
		})
	}
}

// TestCollectToolMissing checks that a missing git is a tool error, which the
// CLI maps to exit 3, and not a failed verification.
func TestCollectToolMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Collect(t.Context(), Options{Dir: t.TempDir(), Base: "HEAD~1", Head: "HEAD"})
	if !errors.Is(err, ErrTool) || !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want ErrTool wrapping exec.ErrNotFound", err)
	}
}

// TestRatchet runs the lint ratchet against a fake golangci-lint, so it checks
// the flags and the report without a real linter.
func TestRatchet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake golangci-lint is a shell script")
	}
	base := strings.Repeat("ab", 20)
	for _, tt := range []struct {
		name     string
		version  string
		json     string
		wantErr  error
		wantLint gate.Lint
	}{
		{
			name: "two new issues", version: "golangci-lint has version 2.13.2 built with go1.27.0",
			json:     `{"Issues":[{"FromLinter":"revive"},{"FromLinter":"gosec"}],"Report":{}}`,
			wantLint: gate.Lint{BaseConfig: true, Ran: true, NewIssues: 2},
		},
		{
			name: "none", version: "golangci-lint has version 2.13.2 built with go1.27.0",
			json:     `{"Issues":[],"Report":{}}`,
			wantLint: gate.Lint{BaseConfig: true, Ran: true},
		},
		{
			name: "version 1 cannot ratchet", version: "golangci-lint has version 1.64.8 built with go1.24.0",
			json: `{"Issues":[]}`, wantErr: ErrTool, wantLint: gate.Lint{BaseConfig: true},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := filepath.Join(dir, "args")
			// Only shell builtins: PATH holds nothing but the fake linter.
			script := "#!/bin/sh\ncase \"$1\" in version) echo '" + tt.version + "'; exit 0;; esac\n" +
				"printf '%s\\n' \"$*\" > " + args + "\nprintf '%s\\n' '" + tt.json + "'\n" +
				"echo 'stats, not JSON'\nexit 0\n"
			if err := os.WriteFile(filepath.Join(dir, lintTool), []byte(script), 0o700); err != nil { //nolint:gosec // it must be executable
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			c := &collector{
				o:        Options{Env: goEnv},
				ev:       &Evidence{Root: dir, Base: base},
				baseLint: filepath.Join(dir, lintConfig),
			}
			lint, err := c.ratchet(t.Context())
			if !errors.Is(err, tt.wantErr) || lint != tt.wantLint {
				t.Fatalf("ratchet = %+v, %v; want %+v, %v", lint, err, tt.wantLint, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			line, err := os.ReadFile(args) //nolint:gosec // written by the fake linter in a temporary directory
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"--config " + c.baseLint, "--new-from-merge-base " + base, "--issues-exit-code=0", "./..."} {
				if !strings.Contains(string(line), want) {
					t.Errorf("golangci-lint ran with %q, want it to carry %q", line, want)
				}
			}
			if len(c.ev.Checks) != 1 || c.ev.Checks[0].Name != "lint" {
				t.Errorf("checks = %+v, want one named lint", c.ev.Checks)
			}
		})
	}
}

func TestRatchetToolMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	c := &collector{ev: &Evidence{Root: dir}, baseLint: filepath.Join(dir, lintConfig)}
	if _, err := c.ratchet(t.Context()); !errors.Is(err, ErrTool) || !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want ErrTool wrapping exec.ErrNotFound", err)
	}
}

func TestPremortem(t *testing.T) {
	t.Parallel()
	const md = "# Premortem\n\n" +
		"- ORD-F01 is retried and refunds twice\n  with a second line\n" +
		"- nothing guards this one\n" +
		"1) ORD-F02 overflows\n" +
		"```\n- ORD-F03 inside a fence is not an item\n```\n" +
		"  - a nested item is not top level\n" +
		"| a table row | is not an item |\n"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "change"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change", "premortem.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := []string{"ORD-F01", "ORD-F02"}
	pm, err := premortem(dir, "change", parseIDs(t, ids))
	if err != nil {
		t.Fatal(err)
	}
	want := gate.Premortem{Present: true, Items: 3, Unmapped: []string{"- nothing guards this one"}}
	if !reflect.DeepEqual(pm, want) {
		t.Errorf("premortem = %+v, want %+v", pm, want)
	}
	if pm, err := premortem(dir, "nowhere", nil); err != nil || pm.Present || pm.Items != 0 {
		t.Errorf("premortem without a file = %+v, %v; want it absent", pm, err)
	}
}

func TestChangeDirs(t *testing.T) {
	t.Parallel()
	got := changeDirs([]string{
		"openspec/changes/add-x/specs/orders/spec.md",
		"openspec/changes/add-x/aval.yaml",
		"openspec/changes/archive/2026-01-01-add-y/aval.yaml",
		"openspec/changes/README.md",
		"openspec/specs/orders/spec.md",
		"orders/orders.go",
	})
	want := []string{"openspec/changes/add-x", "openspec/changes/archive/2026-01-01-add-y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changeDirs = %q, want %q", got, want)
	}
}

func TestRunsOf(t *testing.T) {
	t.Parallel()
	id := parseIDs(t, []string{"ORD-F01"})[0]
	got := runsOf(id, []string{"TestSuite/TestX/ORD-F01_one", "TestSuite/TestX/ORD-F01_one#01", "TestOther/ORD-F01"})
	want := []isolatedRun{
		{test: "TestSuite", pattern: `^TestSuite$/^TestX$/^ORD-F01([_#]|$)`},
		{test: "TestOther", pattern: `^TestOther$/^ORD-F01([_#]|$)`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runsOf = %+v, want %+v", got, want)
	}
}
