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
	const (
		pass, fail, buildFail = evidence.Pass, evidence.Fail, evidence.BuildFail
	)
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
	f01, f02, f03 := id(t, "ORD-F01"), id(t, "ORD-F02"), id(t, "ORD-F03")
	tests := []struct {
		name   string
		owners []owner
		want   string
	}{
		{"one ID, as gotest selects it", []owner{{f01, "TestOrder/ORD-F01_rejects"}}, gotest.RunPattern("TestOrder", f01)},
		{
			"IDs under one path share it, duplicates once",
			[]owner{{f01, "TestOrder/ORD-F01_x"}, {f01, "TestOrder/ORD-F01_x#01"}, {f02, "TestOrder/ORD-F02"}},
			`^TestOrder$/^(ORD-F01|ORD-F02)([_#]|$)`,
		},
		{
			"deeper levels, suites and fake levels from a / in a name",
			[]owner{{f01, "TestSuite/TestX/ORD-F01_x"}, {f02, "TestSuite/happy_path/ORD-F02_in/out"}},
			`^TestSuite$/^TestX$/^ORD-F01([_#]|$)|^TestSuite$/^happy_path$/^ORD-F02([_#]|$)`,
		},
		{
			"bound by attr: the exact name, quoted",
			[]owner{{f03, "TestOrder/dup_(sku)#01"}, {f03, "TestOrder/ORD-F01_carries_another_ID"}},
			`^TestOrder$/^dup_\(sku\)#01$|^TestOrder$/^ORD-F01_carries_another_ID$`,
		},
	}
	for _, tt := range tests {
		if got := (plannedRun{owners: tt.owners}).pattern(); got != tt.want {
			t.Errorf("%s: pattern = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNewPlan(t *testing.T) {
	t.Parallel()
	f01, f02 := id(t, "ORD-F01"), id(t, "ORD-F02")
	p, err := newPlan([]Target{
		{ID: f01, Tests: []string{"TestA/ORD-F01", "TestB/x/ORD-F01"}, Packages: []string{"m/a"}},
		{ID: f02, Tests: []string{"TestA/ORD-F02"}, Packages: []string{"m/b", "m/a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []plannedRun
	for _, r := range p.runs {
		got = append(got, plannedRun{test: r.test, pkgs: r.pkgs})
	}
	want := []plannedRun{{test: "TestA", pkgs: []string{"m/a", "m/b"}}, {test: "TestB", pkgs: []string{"m/a"}}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(p.targets[0].tops, []string{"TestA", "TestB"}) {
		t.Errorf("runs = %+v, tops = %q; want %+v and [TestA TestB]", got, p.targets[0].tops, want)
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
		// No git runs: the directory does not exist.
		if _, err := Run(t.Context(), "missing", "main", "HEAD", targets, Options{}); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: err = %v, want ErrInvalidTarget", name, err)
		}
	}
	if res, err := Run(t.Context(), "missing", "main", "HEAD", nil, Options{}); err != nil || !reflect.DeepEqual(res, Result{}) {
		t.Errorf("no targets: Run = %+v, %v; want a zero Result", res, err)
	}
}

func TestParseChanges(t *testing.T) {
	t.Parallel()
	out := "M\x00a/b_test.go\x00A\x00a/testdata/x y.txt\x00D\x00a/old_test.go\x00M\x00a/b.go\x00" +
		"T\x00testdata/link\x00A\x00a/testdata.go\x00D\x00c/testdata/d/e\x00"
	copied, removed, err := parseChanges([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b_test.go", "a/testdata/x y.txt", "testdata/link"}; !reflect.DeepEqual(copied, want) {
		t.Errorf("copied = %q, want %q", copied, want)
	}
	if want := []string{"a/old_test.go", "c/testdata/d/e"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed = %q, want %q", removed, want)
	}
	if c, r, err := parseChanges(nil); c != nil || r != nil || err != nil {
		t.Errorf("no changes: %q, %q, %v", c, r, err)
	}
	for _, bad := range []string{"M\x00a_test.go", "R100\x00a_test.go\x00"} {
		if _, _, err := parseChanges([]byte(bad)); err == nil {
			t.Errorf("parseChanges(%q) = nil error", bad)
		}
	}
}
