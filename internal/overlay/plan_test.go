package overlay

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
)

func id(t *testing.T, s string) obligation.ID {
	t.Helper()
	v, err := obligation.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestStrength(t *testing.T) {
	t.Parallel()
	const pass, fail, buildFail = evidence.Pass, evidence.Fail, evidence.BuildFail
	tests := []struct {
		before, after evidence.Status
		char          bool
		want          evidence.Strength
	}{
		{fail, pass, false, evidence.Strong},
		{fail, pass, true, evidence.Strong},
		{buildFail, pass, false, evidence.Weak},
		{pass, pass, true, evidence.Characterized},
		{pass, pass, false, evidence.None},
		{evidence.NotRun, pass, true, evidence.None},
		{evidence.Skipped, pass, true, evidence.None},
		{fail, fail, false, evidence.None}, // the tests must pass at head
		{buildFail, evidence.Skipped, false, evidence.None},
		{pass, evidence.NotRun, true, evidence.None},
		{fail, "", false, evidence.None},
	}
	for _, tt := range tests {
		if got := Strength(tt.before, tt.after, tt.char); got != tt.want {
			t.Errorf("Strength(%s, %s, %v) = %s, want %s", tt.before, tt.after, tt.char, got, tt.want)
		}
	}
}

func TestPattern(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"as gotest selects it", []string{"TestOrder/ORD-F01_rejects"}, gotest.RunPattern("TestOrder", f01)},
		{"duplicates once", []string{"TestOrder/ORD-F01_x", "TestOrder/ORD-F01_x#01", "TestOrder/ORD-F01"}, `^TestOrder$/^ORD-F01([_#]|$)`},
		{
			"deeper levels, suites and fake levels from a / in a name",
			[]string{"TestSuite/TestX/ORD-F01_x", "TestSuite/happy_path/ORD-F01_in/out"},
			`^TestSuite$/^TestX$/^ORD-F01([_#]|$)|^TestSuite$/^happy_path$/^ORD-F01([_#]|$)`,
		},
		{
			"bound by attr: the exact name, quoted",
			[]string{"TestOrder/dup_(sku)#01", "TestOrder/ORD-F02_carries_another_ID"},
			`^TestOrder$/^dup_\(sku\)#01$|^TestOrder$/^ORD-F02_carries_another_ID$`,
		},
	}
	for _, tt := range tests {
		if got := pattern(f01, tt.names); got != tt.want {
			t.Errorf("%s: pattern = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNewPlan(t *testing.T) {
	t.Parallel()
	f01, f02 := id(t, "ORD-F01"), id(t, "ORD-F02")
	plan, err := newPlan([]Target{
		{ID: f01, Tests: []string{"TestA/ORD-F01", "TestB/x/ORD-F01", "TestA/ORD-F01_y"}, Packages: []string{"m/a"}},
		{ID: f02, Tests: []string{"TestA/ORD-F02"}, Packages: []string{"m/b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]plannedRun{
		{{"TestA", `^TestA$/^ORD-F01([_#]|$)`}, {"TestB", `^TestB$/^x$/^ORD-F01([_#]|$)`}},
		{{"TestA", `^TestA$/^ORD-F02([_#]|$)`}}, // a run of its own: siblings stay apart
	}
	if len(plan) != 2 || !reflect.DeepEqual(plan[0].runs, want[0]) || !reflect.DeepEqual(plan[1].runs, want[1]) {
		t.Errorf("plan = %+v, want runs %+v", plan, want)
	}
}

func TestRunRejectsTargets(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	valid := Target{ID: f01, Tests: []string{"TestA/ORD-F01"}, Packages: []string{"m/a"}}
	tests := map[string][]Target{
		"no ID":            {{Tests: valid.Tests, Packages: valid.Packages}},
		"no tests":         {{ID: f01, Packages: valid.Packages}},
		"no packages":      {{ID: f01, Tests: valid.Tests}},
		"ID as top level":  {{ID: f01, Tests: []string{"ORD-F01"}, Packages: valid.Packages}},
		"empty level":      {{ID: f01, Tests: []string{"TestA//ORD-F01"}, Packages: valid.Packages}},
		"package as flag":  {{ID: f01, Tests: valid.Tests, Packages: []string{"-exec=evil.sh"}}},
		"ID in two places": {valid, valid},
	}
	for name, targets := range tests {
		// Validation comes first: this tree was never prepared.
		if _, err := new(Tree).Run(t.Context(), targets, RunOptions{}); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: err = %v, want ErrInvalidTarget", name, err)
		}
	}
	if res, err := new(Tree).Run(t.Context(), nil, RunOptions{}); err != nil || !reflect.DeepEqual(res, Result{}) {
		t.Errorf("no targets: Run = %+v, %v; want a zero Result", res, err)
	}
}

func TestParseChanges(t *testing.T) {
	t.Parallel()
	out := "M\x00a/b_test.go\x00A\x00a/testdata/x y.txt\x00D\x00a/old_test.go\x00M\x00a/b.go\x00" +
		"T\x00testdata/link\x00A\x00a/testdata.go\x00D\x00c/testdata/d/e\x00A\x00a/fixtures/in.txt\x00" +
		"D\x00a/gone.txt\x00M\x00go.mod\x00A\x00n/n.go\x00"
	got, err := parseChanges([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := changeSet{
		copy:   []string{"a/b_test.go", "a/testdata/x y.txt", "testdata/link"},
		remove: []string{"a/old_test.go", "c/testdata/d/e"},
		others: []string{"a/fixtures/in.txt", "go.mod"},
		newGo:  []string{"a/testdata.go", "n/n.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseChanges = %+v, want %+v", got, want)
	}
	if got, err := parseChanges(nil); !reflect.DeepEqual(got, changeSet{}) || err != nil {
		t.Errorf("no changes: %+v, %v", got, err)
	}
	for _, bad := range []string{"M\x00a_test.go", "R100\x00a_test.go\x00"} {
		if _, err := parseChanges([]byte(bad)); err == nil {
			t.Errorf("parseChanges(%q) = nil error", bad)
		}
	}
}

func TestOwnBuild(t *testing.T) {
	t.Parallel()
	for failed, want := range map[string]bool{
		"m/p [m/p.test]":      true, // its test files do not compile
		"m/p_test [m/p.test]": true, // nor its external tests
		"m/p":                 true,
		"":                    true,
		"example.com/dep":     false, // a missing module
		"m/q":                 false, // another package
		"m/q [m/p.test]":      false,
		"m/p [m/q.test]":      false,
	} {
		if got := ownBuild(gotest.Package{Name: "m/p", FailedBuild: failed}); got != want {
			t.Errorf("ownBuild(FailedBuild %q) = %v, want %v", failed, got, want)
		}
	}
}

// TestGradeCapsStrength checks that a failure at the base is only strong
// when nothing in the obligation's packages could have caused it by itself.
func TestGradeCapsStrength(t *testing.T) {
	t.Parallel()
	f01 := id(t, "ORD-F01")
	const pkg = "example.com/svc/a"
	failed := gotest.Report{Tests: []gotest.TestOutcome{{
		Package: pkg, Name: "TestA/ORD-F01_x", Status: evidence.Fail,
		Bindings: []gotest.Binding{{ID: f01, Owner: "TestA/ORD-F01_x"}},
	}}}
	target := Target{ID: f01, Tests: []string{"TestA/ORD-F01_x"}, Packages: []string{pkg}, After: evidence.Pass}
	tests := []struct {
		name            string
		others, skipped []string
		want            evidence.Strength
		note            string
	}{
		{name: "nothing in the way", want: evidence.Strong},
		{
			name: "a fixture head adds outside testdata", others: []string{"svc/a/fixtures/in.txt"},
			want: evidence.Weak, note: "head-only files: svc/a/fixtures/in.txt",
		},
		{
			name: "a path the tree cannot hold", skipped: []string{"svc/a/sub"},
			want: evidence.Weak, note: "cannot hold: svc/a/sub",
		},
		{
			name: "both", others: []string{"svc/a/fixtures/in.txt"}, skipped: []string{"svc/a/out.txt"},
			want: evidence.Weak, note: "in.txt; and from paths the base tree cannot hold: svc/a/out.txt",
		},
		{
			name: "both, but in another package", others: []string{"svc/b/in.txt"}, skipped: []string{"svc/b/sub"},
			want: evidence.Strong,
		},
	}
	for _, tt := range tests {
		w := &Tree{module: "example.com/svc", root: "svc", others: tt.others, Skipped: tt.skipped}
		got := w.grade(target, failed)
		if got.Strength != tt.want || (tt.note == "") != (got.Note == "") || !strings.Contains(got.Note, tt.note) {
			t.Errorf("%s: grade = %+v, want %s with a note ~%q", tt.name, got, tt.want, tt.note)
		}
	}
}

// TestMerge checks which entries the tree is made of: not the ones head
// deleted, head's own where both trees hold the path, and not a base file
// whose path head turned into a directory. A rename that only changes case
// is two paths here, and only head's is materialized, so on a
// case-insensitive file system the file head added is what survives.
func TestMerge(t *testing.T) {
	t.Parallel()
	base := []treeEntry{
		{path: "a/Case_test.go", oid: "b1"}, {path: "a/keep.go", oid: "b2"},
		{path: "a/gone_test.go", oid: "b3"}, {path: "a/over_test.go", oid: "b4"},
		{path: "a/x", oid: "b5"}, // head turns it into a directory
	}
	head := []treeEntry{
		{path: "a/case_test.go", oid: "h1"}, {path: "a/keep.go", oid: "h2"},
		{path: "a/over_test.go", oid: "h3"}, {path: "a/x/y_test.go", oid: "h4"},
	}
	copied := []string{"a/case_test.go", "a/over_test.go", "a/x/y_test.go"}
	got, err := merge(base, head, copied, []string{"a/gone_test.go", "a/Case_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []treeEntry{
		{path: "a/keep.go", oid: "b2"}, // head's production code stays behind
		{path: "a/case_test.go", oid: "h1"}, {path: "a/over_test.go", oid: "h3"}, {path: "a/x/y_test.go", oid: "h4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merge = %+v, want %+v", got, want)
	}
	if _, err := merge(base, head, []string{"a/nowhere_test.go"}, nil); err == nil {
		t.Error("merge with a path head does not have = nil error")
	}
}
