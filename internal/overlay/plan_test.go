package overlay

import (
	"errors"
	"reflect"
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
		// Validation comes first: this worktree was never prepared.
		if _, err := new(Worktree).Run(t.Context(), targets, RunOptions{}); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: err = %v, want ErrInvalidTarget", name, err)
		}
	}
	if res, err := new(Worktree).Run(t.Context(), nil, RunOptions{}); err != nil || !reflect.DeepEqual(res, Result{}) {
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
