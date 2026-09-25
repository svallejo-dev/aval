package verify

import (
	"cmp"
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
	r.write(files, gone...)
	return r.commitOnly(msg)
}

// commitOnly commits what is staged and returns the new commit.
func (r *repo) commitOnly(msg string) string {
	r.t.Helper()
	r.git("commit", "--quiet", "--message", msg)
	return r.git("rev-parse", "HEAD")
}

// write writes files, removes the paths of gone and stages everything.
func (r *repo) write(files map[string]string, gone ...string) {
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
}

// assertClean checks that Collect left no worktree, nothing in tmp and no
// configuration of its own inside the repository.
func assertClean(t *testing.T, r *repo, tmp string) {
	t.Helper()
	if n := strings.Count("\n"+r.git("worktree", "list", "--porcelain"), "\nworktree "); n != 1 {
		t.Errorf("git worktree list shows %d worktrees, want only the repository's own", n)
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) > 0 {
		t.Errorf("temporary directory holds %v (%v), want it empty", entries, err)
	}
	left, err := filepath.Glob(filepath.Join(r.dir, ".golangci.base-*"))
	if err != nil || len(left) > 0 {
		t.Errorf("the repository holds %v (%v), want the base lint configuration gone", left, err)
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
    - shared/**
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

### Requirement: ORD-F05 An order starts unused
The system SHALL start every order unused.

#### Scenario: A new order
- **WHEN** an order is new
- **THEN** it is unused
`

// changeSpec adds the obligations of the pull request: one that fails at the
// base, one the behavior already satisfied there, and the four shapes of
// fail-before evidence ADR-0005 §2 grades.
const changeSpec = `## ADDED Requirements

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

### Requirement: ORD-F06 Shouting reads the fixture
The system SHALL shout what the fixture says.

#### Scenario: The fixture says hi
- **WHEN** the fixture says hi
- **THEN** shouting gives HI

### Requirement: ORD-F07 One greeting for every caller
The system SHALL greet with one greeting.

#### Scenario: Two callers
- **WHEN** two callers greet
- **THEN** both use the same greeting

### Requirement: ORD-F08 The answer is fixed
The system SHALL answer 42.

#### Scenario: Asking
- **WHEN** the answer is asked for
- **THEN** it is 42

### Requirement: ORD-F09 The module gives the answer
The system SHALL take the answer from the shared module.

#### Scenario: Asking the module
- **WHEN** the module is asked
- **THEN** it answers 42

### Requirement: ORD-F10 The answer is 42 next door too
The system SHALL answer 42 beside the submodule.

#### Scenario: Asking next door
- **WHEN** the package beside the submodule is asked
- **THEN** it answers 42
`

// baseFiles is the fixture repository at the base. Its .gitattributes marks
// everything the gate reads as export-ignore: git archive would leave the
// policy, the baseline and the specs out of the extraction, so the extraction
// must not use it (ADR-0005 §1).
func baseFiles() map[string]string {
	return map[string]string{
		"go.mod":                        "module example.com/svc\n\ngo 1.27\n",
		"aval.yaml":                     policy,
		".gitattributes":                "aval.yaml export-ignore\n.aval/** export-ignore\nopenspec/** export-ignore\n*_test.go export-subst\n",
		"CODEOWNERS":                    "* @orders\n",
		"openspec/specs/orders/spec.md": mainSpec,
		"docs/design.md":                "# design\n",
		".aval/baseline.json": "{\n  \"version\": 1,\n  \"failing\": [\n" +
			"    {\"package\": \"example.com/svc/flaky\", \"test\": \"TestOld\"},\n" +
			"    {\"package\": \"example.com/svc/flaky\", \"test\": \"TestGroup\"}\n  ]\n}\n",
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
		// A sibling subtest decides ORD-F05's outcome, so the full run fails it
		// and the isolated run of §3 passes it.
		"shared/shared.go": `package shared

var used bool

// Use marks the order used.
func Use() { used = true }

// Used reports whether the order was used.
func Used() bool { return used }
`,
		"shared/shared_test.go": `package shared

import "testing"

func TestShared(t *testing.T) {
	t.Run("first uses the order", func(t *testing.T) { Use() })
	t.Run("ORD-F05 starts unused", func(t *testing.T) {
		if Used() {
			t.Fatal("something used the order first")
		}
	})
}
`,
		"flaky/flaky_test.go": `package flaky

import "testing"

func TestOld(t *testing.T) { t.Fatal("failing since before aval") }

func TestGroup(t *testing.T) {
	t.Run("case", func(t *testing.T) { t.Fatal("a failing case") })
}
`,
		// The packages the fail-before targets need at the base.
		"reader/reader.go": "package reader\n\nimport \"strings\"\n\n// Shout upper-cases s.\nfunc Shout(s string) string { return strings.ToUpper(s) }\n",
		"uses/uses.go":     "package uses\n\n// Greet answers a greeting.\nfunc Greet() string { return \"hi\" }\n",
		"fixed/fixed.go":   "package fixed\n\n// Wanted is the answer.\nfunc Wanted() int { return 42 }\n",
		"deps/deps.go":     "package deps\n\n// Wanted is the answer.\nfunc Wanted() int { return 42 }\n",
		// subs holds a submodule's gitlink, which the base tree cannot hold:
		// its failure there is never strong evidence, however honest it looks.
		"subs/subs.go": "package subs\n\n// Answer is wrong at the base.\nfunc Answer() int { return 0 }\n",
		// broken does not compile at the base, and head does not add it: a
		// build failure that names it is no evidence at all.
		"broken/broken.go": "package broken\n\n// Answer does not compile at the base.\nfunc Answer() int { return \"42\" }\n",
	}
}

// headFiles is the pull request: a change that adds six obligations, a skip
// slipped into a bound test outside the delta, a new unbound failure, edits of
// the policy, of CODEOWNERS and of a .gitattributes below the root, and a test that
// rewrites the repository and looks for a worktree while it runs.
func headFiles() map[string]string {
	return map[string]string{
		"aval.yaml":  strings.Replace(policy, "mode: enforce", "mode: observe", 1),
		"CODEOWNERS": "* @nobody\n",
		// A .gitattributes below the root is tamper too: it steers what git
		// reports. It goes in a package no fail-before target lives in, since
		// a file that does not travel weakens the evidence of the ones that do.
		"shared/.gitattributes":                  "* -diff\n",
		"go.mod":                                 "module example.com/svc\n\ngo 1.27\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n",
		"dep/go.mod":                             "module example.com/dep\n\ngo 1.27\n",
		"dep/dep.go":                             "package dep\n\n// Answer is 42.\nfunc Answer() int { return 42 }\n",
		"openspec/changes/add-refunds/aval.yaml": "version: 1\ntier: 1\nowner: \"@orders\"\n",
		"openspec/changes/add-refunds/specs/orders/spec.md": changeSpec,
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
		// A fixture outside testdata/ does not travel to the base, so the
		// failure there is only weak evidence, with a note.
		"reader/fixtures/in.txt": "hi\n",
		"reader/reader_test.go": `package reader

import (
	"os"
	"strings"
	"testing"
)

func TestShout(t *testing.T) {
	t.Run("ORD-F06 shouts the fixture", func(t *testing.T) {
		in, err := os.ReadFile("fixtures/in.txt")
		if err != nil {
			t.Fatal(err)
		}
		if got := Shout(strings.TrimSpace(string(in))); got != "HI" {
			t.Fatalf("got %q, want HI", got)
		}
	})
}
`,
		// A package head adds: the base cannot build the test, which is weak.
		"helper/helper.go": "package helper\n\n// Greeting is the one greeting.\nfunc Greeting() string { return \"hi\" }\n",
		"uses/uses_test.go": `package uses

import (
	"testing"

	"example.com/svc/helper"
)

func TestUses(t *testing.T) {
	t.Run("ORD-F07 greets through the new package", func(t *testing.T) {
		if helper.Greeting() != Greet() {
			t.Fatal("different greetings")
		}
	})
}
`,
		// A package the base already has and that does not build there: no
		// evidence, because the failure says nothing about the behavior.
		"broken/broken.go": "package broken\n\n// Answer is 42.\nfunc Answer() int { return 42 }\n",
		"fixed/fixed_test.go": `package fixed

import (
	"testing"

	"example.com/svc/broken"
)

func TestFixed(t *testing.T) {
	t.Run("ORD-F08 answers through the fixed package", func(t *testing.T) {
		if broken.Answer() != Wanted() {
			t.Fatal("wrong answer")
		}
	})
}
`,
		// A module the base does not require: no evidence either. New
		// dependencies land in an earlier dx pull request.
		"deps/deps_test.go": `package deps

import (
	"testing"

	"example.com/dep"
)

func TestDeps(t *testing.T) {
	t.Run("ORD-F09 answers through the module", func(t *testing.T) {
		if dep.Answer() != Wanted() {
			t.Fatal("wrong answer")
		}
	})
}
`,
		"subs/subs.go": "package subs\n\n// Answer is 42.\nfunc Answer() int { return 42 }\n",
		"subs/subs_test.go": `package subs

import "testing"

func TestSubs(t *testing.T) {
	t.Run("ORD-F10 answers 42", func(t *testing.T) {
		if got := Answer(); got != 42 {
			t.Fatalf("got %d, want 42", got)
		}
	})
}
`,
		"flaky/flaky_test.go": `package flaky

import "testing"

func TestOld(t *testing.T) { t.Fatal("failing since before aval") }

func TestNew(t *testing.T) { t.Fatal("failing since this pull request") }

func TestGroup(t *testing.T) {
	t.Run("case", func(t *testing.T) { t.Fatal("a failing case") })
}
`,
		"meddle/meddle_test.go": meddleTest,
	}
}

// meddleTest is the pull request's code, which runs in the same job: it
// rewrites what aval reads from git (ADR-0005 §1), rewrites every lint
// configuration it can find, and looks for the fail-before worktree, which
// must not exist while it runs (§2).
//
// It leaves the production code alone, because the isolated runs of §3 have to
// run after head's tests, so a test that breaks the build breaks them too:
// that is the compromised runner ADR-0005 leaves outside v0.
const meddleTest = `package meddle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const noLinters = "version: \"2\"\nlinters:\n  default: none\n"

func TestMeddle(t *testing.T) {
	for name, content := range map[string]string{
		"openspec/specs/orders/spec.md": "### Requirement: not a requirement\n",
		"aval.yaml":                     "version: 9\n",
		".golangci.yml":                 noLinters,
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
	found, err := filepath.Glob("../.golangci*.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range found {
		if err := os.WriteFile(name, []byte(noLinters), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// aval's own temporary directory must hold the tree it read the base
	// inputs from and nothing else: the tree the fail-before runs use comes
	// after these tests (ADR-0005 §2).
	if tmp := os.Getenv("AVAL_TEST_TMP"); tmp != "" {
		dirs, err := filepath.Glob(filepath.Join(tmp, "aval-verify-*"))
		if err != nil || len(dirs) != 1 {
			t.Fatalf("aval's temporary directories = %q, %v; want one", dirs, err)
		}
		entries, err := os.ReadDir(dirs[0])
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) != 1 || names[0] != "base-tree" {
			t.Errorf("aval's temporary directory holds %q while head's tests run, want only base-tree", names)
		}
	}
	out, err := exec.Command("git", "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Skipf("git worktree list: %v", err)
	}
	if n := strings.Count("\n"+string(out), "\nworktree "); n != 1 {
		t.Errorf("git lists %d worktrees while head's tests run, want only the repository's own:\n%s", n, out)
	}
}
`

// TestCollect gathers a tier-1 pull request whose evidence covers every shape
// ADR-0005 asks for, and checks that head's own code cannot change any of it.
func TestCollect(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r := newRepo(t)
	r.commit("first", baseFiles())
	// A submodule's gitlink in ORD-F10's package. git add --all keeps one whose
	// directory exists, even empty, so it is in the base and in head.
	r.write(nil)
	if err := os.MkdirAll(filepath.Join(r.dir, "subs", "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	r.git("update-index", "--add", "--cacheinfo", "160000,"+r.git("rev-parse", "HEAD")+",subs/sub")
	base := r.commitOnly("base")
	head := r.commit("head", headFiles())
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Repo: "svallejo-dev/svc", AvalVersion: "test",
		Env: append(slices.Clone(goEnv), "AVAL_TEST_TMP="+tmp), TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)

	if ev.Base != base || ev.Head != head {
		t.Errorf("range = %s..%s, want %s..%s", ev.Base, ev.Head, base, head)
	}
	// The policy is the base's, although head edited it and the base's
	// .gitattributes marks it export-ignore.
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
	// The specs and the baseline aval read are the ones git holds, not the ones
	// the tests left behind, and export-ignore did not hide them.
	if len(ev.Input.SpecFindings) != 0 {
		t.Errorf("SpecFindings = %v, want none", ev.Input.SpecFindings)
	}
	if len(ev.Input.Baseline.Failing) != 2 {
		t.Errorf("Baseline = %+v, want the base's two entries", ev.Input.Baseline)
	}

	want := map[string]struct {
		delta            evidence.Delta
		before, after    evidence.Status
		strength         evidence.Strength
		characterization bool
		note             string
	}{
		// Added, fails at the base because head's fix does not travel.
		"ORD-F02": {evidence.Added, evidence.Fail, evidence.Pass, evidence.Strong, false, ""},
		// Added, but the behavior was already there: no evidence.
		"ORD-F03": {evidence.Added, evidence.Pass, evidence.Pass, evidence.None, false, ""},
		// Added, and the base failed for want of a fixture outside testdata/.
		"ORD-F06": {evidence.Added, evidence.Fail, evidence.Pass, evidence.Weak, false, "reader/fixtures/in.txt"},
		// Added, and the base could not build a package head adds.
		"ORD-F07": {evidence.Added, evidence.BuildFail, evidence.Pass, evidence.Weak, false, "a package head adds"},
		// Added, and the base could not build a package it already had.
		"ORD-F08": {evidence.Added, evidence.BuildFail, evidence.Pass, evidence.None, false, "land new dependencies"},
		// Added, and the base does not require the module: no evidence.
		"ORD-F09": {evidence.Added, evidence.BuildFail, evidence.Pass, evidence.None, false, "land new dependencies"},
		// Added, and it fails at the base for what looks like the right
		// reason, but the base tree could not hold the submodule beside it.
		"ORD-F10": {evidence.Added, evidence.Fail, evidence.Pass, evidence.Weak, false, "subs/sub"},
		// Outside the delta: no fail-before, and §3 re-ran it on its own.
		"ORD-F01": {evidence.Unchanged, evidence.NotApply, evidence.Pass, evidence.None, false, ""},
		// Outside the delta and now skipped: the gate blocks on after.
		"ORD-F04": {evidence.Unchanged, evidence.NotApply, evidence.Skipped, evidence.None, true, ""},
		// Outside the delta, passes alone and fails in the full run: the worse
		// of the two is what the gate sees.
		"ORD-F05": {evidence.Unchanged, evidence.NotApply, evidence.Fail, evidence.None, false, ""},
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
			o.Strength != w.strength || o.Characterization != w.characterization ||
			!strings.Contains(o.Note, w.note) {
			t.Errorf("%s = %+v; want delta=%s %s→%s strength=%s characterization=%v note ~%q",
				o.ID, o, w.delta, w.before, w.after, w.strength, w.characterization, w.note)
		}
		if len(o.Tests) == 0 || !strings.HasPrefix(o.Source, "openspec/") {
			t.Errorf("%s: tests = %q, source = %q; want both filled from the specs", o.ID, o.Tests, o.Source)
		}
	}

	// Tamper: the skip slipped into a bound test outside its delta, and every
	// edit of a file the policy rests on.
	var edits []string
	for _, f := range ev.Input.Tamper {
		edits = append(edits, string(f.Kind)+" "+cmp.Or(f.ID, strings.TrimPrefix(f.Detail, "the pull request edits ")))
	}
	slices.Sort(edits)
	wantEdits := []string{
		"policy_edited CODEOWNERS", "policy_edited aval.yaml",
		"policy_edited shared/.gitattributes", "skip_added ORD-F04",
	}
	if !reflect.DeepEqual(edits, wantEdits) {
		t.Errorf("Tamper = %q, want %q", edits, wantEdits)
	}

	// Unbound leaf failures, for the gate to compare with the base baseline.
	// TestGroup is no leaf, although the baseline names it: the failing case
	// below it is, and nothing in the baseline covers that.
	wantFailures := []baseline.Test{
		{Package: "example.com/svc/flaky", Test: "TestGroup/case"},
		{Package: "example.com/svc/flaky", Test: "TestNew"},
		{Package: "example.com/svc/flaky", Test: "TestOld"},
	}
	got := slices.Clone(ev.Input.UnboundFailures)
	slices.SortFunc(got, func(a, b baseline.Test) int { return strings.Compare(a.Test, b.Test) })
	if !reflect.DeepEqual(got, wantFailures) {
		t.Errorf("UnboundFailures = %+v, want %+v", got, wantFailures)
	}
	if !ev.Input.Baseline.Contains("example.com/svc/flaky", "TestOld") ||
		ev.Input.Baseline.Contains("example.com/svc/flaky", "TestGroup/case") {
		t.Errorf("Baseline = %+v, want it to cover TestOld and not TestGroup/case", ev.Input.Baseline)
	}
	if ev.Input.Lint != (gate.Lint{}) {
		t.Errorf("Lint = %+v, want it off: the base has no %s", ev.Input.Lint, lintConfig)
	}

	// Every run is in the bundle, the isolated ones included.
	var names []string
	for _, ch := range ev.Checks {
		names = append(names, ch.Name)
	}
	for _, name := range []string{"go test", "openspec validate", "fail-before ORD-F02 TestTotal", "regression ORD-F05 TestShared"} {
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
	for _, code := range []string{
		gate.CodeTamper, gate.CodeRegression, gate.CodeAfterNotPassing,
		gate.CodeFailBeforeMissing, gate.CodeWeakEvidence,
	} {
		if !slices.Contains(codes, code) {
			t.Errorf("reasons = %q, want one with code %q", codes, code)
		}
	}
}

// cleanBase is the fixture repository with nothing failing and no change, for
// the tests that need a pull request to come out clean.
func cleanBase() map[string]string {
	files := baseFiles()
	for _, name := range []string{
		"flaky/flaky_test.go", "shared/shared_test.go", ".aval/baseline.json",
		"broken/broken.go", ".gitattributes",
	} {
		delete(files, name)
	}
	files["openspec/specs/orders/spec.md"] = strings.Split(mainSpec, "### Requirement: ORD-F05")[0]
	return files
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
	base := r.commit("base", cleanBase())
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
	if want := []string{"mutation", "rollback", "slo"}; !reflect.DeepEqual(saved.NotCollected, want) {
		t.Errorf("notCollected = %q, want %q", saved.NotCollected, want)
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

// TestCollectSpecFindings checks that only new findings count, that a change
// this pull request archived keeps the path the base knew it by (ADR-0005 §4),
// and that a RENAMED delta asks for no fail-before: it keeps the ID, so the
// test need not change (§1).
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
	files := cleanBase()
	files["openspec/changes/add-x/specs/orders/spec.md"] = unknown("ORD-F50")
	files["openspec/changes/dup-w/specs/orders/spec.md"] = unknown("ORD-F70")
	base := r.commit("base", files)
	head := r.commit("head", map[string]string{
		// The same finding twice where the base had it once: one of them is new.
		"openspec/changes/dup-w/specs/orders/spec.md":                    unknown("ORD-F70") + "\n" + unknown("ORD-F70"),
		"openspec/changes/archive/2026-01-01-add-x/specs/orders/spec.md": unknown("ORD-F50"),
		"openspec/changes/add-y/specs/orders/spec.md":                    unknown("ORD-F60"),
		"openspec/changes/rename-z/specs/orders/spec.md": "## RENAMED Requirements\n\n" +
			"- FROM: `### Requirement: ORD-F01 Total adds the lines`\n" +
			"- TO: `### Requirement: ORD-F01 Total is the sum of the lines`\n",
	}, "openspec/changes/add-x")
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)
	var found []string
	for _, f := range ev.Input.SpecFindings {
		if f.Rule != openspec.RuleUnknownID {
			t.Errorf("finding = %+v, want an unknown ID", f)
		}
		found = append(found, f.Path)
	}
	slices.Sort(found)
	want := []string{"openspec/changes/add-y/specs/orders/spec.md", "openspec/changes/dup-w/specs/orders/spec.md"}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("SpecFindings = %v, want the one add-y brings and the second of dup-w", ev.Input.SpecFindings)
	}
	for _, id := range []string{"add-x", "add-y", "dup-w", "rename-z"} {
		if !slices.Contains(ev.Changes, id) {
			t.Errorf("Changes = %q, want %s among them", ev.Changes, id)
		}
	}
	// ORD-F01 is only renamed, so it stays outside the delta and §3 runs it in
	// isolation instead of asking for fail-before evidence.
	for _, o := range ev.Input.Obligations {
		if o.ID == "ORD-F01" && (o.Delta != evidence.Unchanged || o.Before != evidence.NotApply) {
			t.Errorf("ORD-F01 = %+v, want it unchanged, with no fail-before", o)
		}
	}
	for _, ch := range ev.Checks {
		if strings.HasPrefix(ch.Name, "fail-before") {
			t.Errorf("check %q: a RENAMED delta asks for no fail-before", ch.Name)
		}
	}
}

// TestCollectDeletedChange checks that deleting a change does not lower the
// tier: head may raise it, never lower it (ADR-0005 §1).
func TestCollectDeletedChange(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r := newRepo(t)
	files := cleanBase()
	files["aval.yaml"] = strings.Replace(policy, "tierDefault: 1", "tierDefault: 0", 1)
	files["openspec/changes/add-x/aval.yaml"] = "version: 1\ntier: 3\n"
	base := r.commit("base", files)
	head := r.commit("head", map[string]string{"docs/design.md": "# gone\n"}, "openspec/changes/add-x")
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)
	want := []gate.Change{{ID: "add-x", BaseTier: 3}}
	if !reflect.DeepEqual(ev.Input.Changes, want) {
		t.Errorf("Changes = %+v, want %+v", ev.Input.Changes, want)
	}
	if got := ev.Input.Tier(); got != 3 {
		t.Errorf("Tier = %d, want 3: deleting the change cannot lower it", got)
	}
}

// TestCollectLintRatchet checks the ratchet end to end against a fake
// golangci-lint: the configuration it runs with is the base's, from the bytes
// phase 1 read out of git, materialized inside the repository so that the
// configuration's own path rules still resolve, and gone afterwards.
func TestCollectLintRatchet(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds a git repository, runs go test and a shell script")
	}
	const baseConfig = "version: \"2\"\nlinters:\n  default: standard\n"
	r := newRepo(t)
	files := cleanBase()
	files[lintConfig] = baseConfig
	files["meddle/meddle_test.go"] = meddleTest
	base := r.commit("base", files)
	// Head edits the configuration, and its tests rewrite every copy of it
	// they can find: neither may reach the ratchet.
	head := r.commit("head", map[string]string{lintConfig: "version: \"2\"\nlinters:\n  default: none\n"})

	bin := t.TempDir()
	seen := filepath.Join(bin, "config-seen")
	script := "#!/bin/sh\ncase \"$1\" in version) echo 'golangci-lint has version 2.13.2 built with go1.27.0'; exit 0;; esac\n" +
		"prev=''\nfor a in \"$@\"; do\n  if [ \"$prev\" = --config ]; then cat \"$a\" > " + seen + "; fi\n  prev=\"$a\"\ndone\n" +
		"echo 'level=warning msg=noise on standard error' >&2\n" +
		"printf '%s\\n' '{\"Issues\":[{\"FromLinter\":\"revive\"}]}'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, lintTool), []byte(script), 0o700); err != nil { //nolint:gosec // it must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)
	if want := (gate.Lint{BaseConfig: true, Ran: true, NewIssues: 1}); ev.Input.Lint != want {
		t.Errorf("Lint = %+v, want %+v", ev.Input.Lint, want)
	}
	if got, err := os.ReadFile(seen); err != nil || string(got) != baseConfig { //nolint:gosec // written by the fake linter
		t.Errorf("golangci-lint ran with %q, %v; want the base's %q", got, err, baseConfig)
	}
	// The copy is aval's, not the repository's: no scope and no tamper finding
	// mentions it, and the only tamper is head's edit of the real one.
	for _, c := range ev.Input.Scope {
		for _, p := range c.Paths {
			if strings.Contains(p, ".golangci.base-") {
				t.Errorf("scope names %q, want nothing about aval's own copy", p)
			}
		}
	}
	if len(ev.Input.Tamper) != 1 || ev.Input.Tamper[0].Kind != evidence.PolicyEdited ||
		!strings.HasSuffix(ev.Input.Tamper[0].Detail, lintConfig) {
		t.Errorf("Tamper = %+v, want only head's edit of %s", ev.Input.Tamper, lintConfig)
	}
}

// TestCollectNoBasePolicy checks the adoption pull request: with no policy at
// the base there is no pinned OpenSpec version to validate with, and the
// bundle says the validation is missing instead of letting its absence read as
// a pass (ADR-0005 §4, no_base_policy).
func TestCollectNoBasePolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git repository and runs go test")
	}
	t.Parallel()
	r := newRepo(t)
	files := cleanBase()
	delete(files, "aval.yaml")
	base := r.commit("base", files)
	head := r.commit("head", map[string]string{
		"aval.yaml": policy,
		// The pull request touches openspec/, so validate would run if there
		// were a version to run it with.
		"openspec/specs/orders/spec.md": files["openspec/specs/orders/spec.md"] + "\n",
	})
	tmp := t.TempDir()

	ev, err := Collect(t.Context(), Options{
		Dir: r.dir, Base: base, Head: head, Env: goEnv, TempDir: tmp, validate: okValidate,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertClean(t, r, tmp)
	if ev.Input.Policy != nil || ev.Input.Mode() != manifest.Observe {
		t.Errorf("Policy = %+v in mode %s, want none and observe", ev.Input.Policy, ev.Input.Mode())
	}
	if ev.Input.Validation != nil {
		t.Error("Validation: head's own pinned version is not trusted, so validate must not run")
	}
	// The token is a contract (ADR-0005 §7), so the test names it, not the
	// constant.
	b := ev.Bundle(gate.Decide(ev.Input))
	if !slices.Contains(b.NotCollected, "openspec_validate") || b.Validate() != nil {
		t.Errorf("notCollected = %q, want openspec_validate among them; Validate: %v", b.NotCollected, b.Validate())
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
		wantMessagePart string
	}{
		{name: "no such revision", dir: r.dir, base: "nope", head: head, wantMessagePart: "names no commit"},
		{name: "a revision that is a flag", dir: r.dir, base: "-h", head: head, wantMessagePart: "not a revision"},
		{name: "no head", dir: r.dir, base: base, head: "", wantMessagePart: "not a revision"},
		{name: "invalid base policy", dir: invalid.dir, base: badBase, head: badHead, wantMessagePart: "aval.yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Collect(t.Context(), Options{Dir: tt.dir, Base: tt.base, Head: tt.head, TempDir: t.TempDir()})
			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), tt.wantMessagePart) {
				t.Errorf("err = %v, want ErrUsage mentioning %q", err, tt.wantMessagePart)
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
// the flags, the report and the copy of the base configuration without a real
// linter.
func TestRatchet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake golangci-lint is a shell script")
	}
	base := strings.Repeat("ab", 20)
	const version2 = "golangci-lint has version 2.13.2 built with go1.27.0"
	for _, tt := range []struct {
		name       string
		version    string
		json       string
		exit       string
		wantErr    error
		wantLint   gate.Lint
		wantStatus evidence.Status
	}{
		{
			name: "two new issues", version: version2, exit: "0",
			json:     `{"Issues":[{"FromLinter":"revive"},{"FromLinter":"gosec"}],"Report":{}}`,
			wantLint: gate.Lint{BaseConfig: true, Ran: true, NewIssues: 2}, wantStatus: evidence.Fail,
		},
		{
			name: "none", version: version2, exit: "0", json: `{"Issues":[],"Report":{}}`,
			wantLint: gate.Lint{BaseConfig: true, Ran: true}, wantStatus: evidence.Pass,
		},
		{
			// A configuration golangci-lint refuses, say: the run says nothing,
			// so the gate blocks on a lint that did not run.
			name: "a run that fails", version: version2, exit: "7", json: `{"Issues":[]}`,
			wantLint: gate.Lint{BaseConfig: true}, wantStatus: evidence.NotRun,
		},
		{
			name: "version 1 cannot ratchet", version: "golangci-lint has version 1.64.8 built with go1.24.0",
			exit: "0", json: `{"Issues":[]}`, wantErr: ErrTool, wantLint: gate.Lint{BaseConfig: true},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := filepath.Join(dir, "args")
			// Only shell builtins: PATH holds nothing but the fake linter.
			script := "#!/bin/sh\ncase \"$1\" in version) echo '" + tt.version + "'; exit 0;; esac\n" +
				"printf '%s\\n' \"$*\" > " + args + "\nprintf '%s\\n' '" + tt.json + "'\n" +
				"echo 'level=warning msg=noise' >&2\necho 'stats, not JSON'\nexit " + tt.exit + "\n"
			if err := os.WriteFile(filepath.Join(dir, lintTool), []byte(script), 0o700); err != nil { //nolint:gosec // it must be executable
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			c := &collector{
				o:        Options{Env: goEnv},
				ev:       &Evidence{Root: dir, Base: base},
				baseLint: []byte("version: \"2\"\n"),
			}
			lint, err := c.ratchet(t.Context())
			if !errors.Is(err, tt.wantErr) || lint != tt.wantLint {
				t.Fatalf("ratchet = %+v, %v; want %+v, %v", lint, err, tt.wantLint, tt.wantErr)
			}
			if tt.wantErr != nil {
				if c.lintFile != "" {
					t.Error("the base configuration was written although the tool is unusable")
				}
				return
			}
			if len(c.ev.Checks) != 1 || c.ev.Checks[0].Name != "lint" || c.ev.Checks[0].Status != tt.wantStatus {
				t.Errorf("checks = %+v, want one named lint with status %s", c.ev.Checks, tt.wantStatus)
			}
			line, err := os.ReadFile(args) //nolint:gosec // written by the fake linter in a temporary directory
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"--config " + c.lintFile, "--new-from-merge-base " + base, "--issues-exit-code=0", "./..."} {
				if !strings.Contains(string(line), want) {
					t.Errorf("golangci-lint ran with %q, want it to carry %q", line, want)
				}
			}
			if got, err := os.ReadFile(c.lintFile); err != nil || string(got) != string(c.baseLint) { //nolint:gosec // aval's own copy
				t.Errorf("the copy holds %q, %v; want the base's %q", got, err, c.baseLint)
			}
			if err := c.close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(c.lintFile); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stat after close = %v, want the copy gone", err)
			}
		})
	}
}

func TestRatchetToolMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	c := &collector{ev: &Evidence{Root: dir}, baseLint: []byte("version: \"2\"\n")}
	if _, err := c.ratchet(t.Context()); !errors.Is(err, ErrTool) || !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want ErrTool wrapping exec.ErrNotFound", err)
	}
}

func TestWorse(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		a, b, want evidence.Status
	}{
		{evidence.Pass, evidence.Fail, evidence.Fail},
		{evidence.Fail, evidence.Pass, evidence.Fail},
		{evidence.Fail, evidence.BuildFail, evidence.BuildFail},
		{evidence.Skipped, evidence.NotRun, evidence.NotRun},
		// A status aval does not know must never read as a pass.
		{"", evidence.Pass, ""},
		{evidence.Pass, "", ""},
	} {
		if got := worse(tt.a, tt.b); got != tt.want {
			t.Errorf("worse(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
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
