package testsource

import (
	"fmt"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// findings renders each finding as "kind ID file:line", checking that the
// detail names the ID and ends with the location.
func findings(t *testing.T, got []evidence.Finding) []string {
	t.Helper()
	if got == nil {
		t.Fatal("Compare returned nil, want a non-nil list")
	}
	var out []string
	for _, f := range got {
		loc := f.Detail[strings.LastIndexByte(f.Detail, ' ')+1 : len(f.Detail)-1]
		if !strings.Contains(f.Detail, f.ID+" ") {
			t.Errorf("detail %q does not name %s", f.Detail, f.ID)
		}
		out = append(out, fmt.Sprintf("%s %s %s", f.Kind, f.ID, loc))
	}
	return out
}

func TestCompare(t *testing.T) {
	t.Parallel()

	d := func(file, fp string, skips bool) Declaration {
		id, err := obligation.Parse("ORD-F01")
		if err != nil {
			t.Fatal(err)
		}
		return Declaration{ID: id, File: file, Test: "TestX", Kind: Subtest, Fingerprint: fp, Line: 7, Skips: skips}
	}
	a, b := d("a_test.go", "fp1", false), d("b_test.go", "fp2", false)
	ds := func(decls ...Declaration) []Declaration { return decls }
	tests := []struct {
		name       string
		base, head []Declaration
		inDelta    bool   // ORD-F01 is in the change's ADDED or MODIFIED deltas
		want       string // "" for no finding
	}{
		{"unchanged", ds(a), ds(a), false, ""},
		{"moved to another file", ds(a), ds(d("c_test.go", "fp1", false)), false, ""},
		{"reordered in one file", ds(a, d("a_test.go", "fp2", false)), ds(d("a_test.go", "fp2", false), a), false, ""},
		{"edited", ds(a), ds(d("a_test.go", "fp9", false)), false, "fingerprint_changed ORD-F01 a_test.go:7"},
		{"edited inside its delta", ds(a), ds(d("a_test.go", "fp9", false)), true, ""},
		{"removed", ds(a), nil, false, "test_removed ORD-F01 a_test.go:7"},
		{"removed inside its delta", ds(a), nil, true, ""},
		{"skip added with an edit", ds(a), ds(d("a_test.go", "fp9", true)), false, "skip_added ORD-F01 a_test.go:7"},
		{"skip added around the test", ds(a), ds(d("a_test.go", "fp1", true)), false, "skip_added ORD-F01 a_test.go:7"},
		{"skip added inside its delta", ds(a), ds(d("a_test.go", "fp1", true)), true, ""},
		{"edited while already skipping", ds(d("a_test.go", "fp1", true)), ds(d("a_test.go", "fp9", true)), false, "fingerprint_changed ORD-F01 a_test.go:7"},
		{"one copy of a duplicate removed", ds(a, b), ds(a), false, "test_removed ORD-F01 b_test.go:7"},
		{"one copy of a duplicate edited", ds(a, b), ds(d("b_test.go", "fp9", false), a), false, "fingerprint_changed ORD-F01 b_test.go:7"},
		{"both copies edited", ds(a, b), ds(d("b_test.go", "fp8", false), d("a_test.go", "fp9", false)), false,
			"fingerprint_changed ORD-F01 a_test.go:7\nfingerprint_changed ORD-F01 b_test.go:7"},
		{"declared again in another file", ds(a), ds(a, b), false, "fingerprint_changed ORD-F01 b_test.go:7"},
		{"first declared in head", nil, ds(a), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := strings.Join(findings(t, Compare(tt.base, tt.head, map[string]bool{"ORD-F01": tt.inDelta})), "\n")
			if got != tt.want {
				t.Errorf("Compare = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCompareScans compares two real scans: a skip added to a subtest and a
// table entry removed are reported, sorted by ID; an entry edited inside its
// delta is not.
func TestCompareScans(t *testing.T) {
	t.Parallel()

	head := edit(t, fpBase, "\t\tif got", "\t\tt.Skip()\n\t\tif got")
	head = edit(t, head, "\t\t{name: \"ORD-F03 sums two\", in: []int{1, 2}},\n", "")
	head = edit(t, head, "in: []int{1}}", "in: []int{5}}")
	got := findings(t, Compare(
		scanFiles(t, map[string]string{"p_test.go": fpBase}),
		scanFiles(t, map[string]string{"p_test.go": head}),
		map[string]bool{"ORD-F02": true},
	))
	want := "skip_added ORD-F01 p_test.go:6\ntest_removed ORD-F03 p_test.go:16"
	if strings.Join(got, "\n") != want {
		t.Errorf("Compare =\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}
