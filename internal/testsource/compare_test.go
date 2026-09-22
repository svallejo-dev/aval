package testsource

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

func mustID(t *testing.T, s string) obligation.ID {
	t.Helper()
	id, err := obligation.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

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
		if !strings.Contains(f.Detail, f.ID+" ") && !strings.Contains(f.Detail, f.ID+",") {
			t.Errorf("detail %q does not name %s", f.Detail, f.ID)
		}
		out = append(out, fmt.Sprintf("%s %s %s", f.Kind, f.ID, loc))
	}
	return out
}

func TestCompare(t *testing.T) {
	t.Parallel()

	d := func(file, fp string, skips bool) Declaration {
		return Declaration{ID: mustID(t, "ORD-F01"), File: file, Test: "TestX", Kind: Subtest, Fingerprint: fp, Line: 7, Skips: skips}
	}
	a, b := d("a_test.go", "fp1", false), d("b_test.go", "fp2", false)
	inTestY := a
	inTestY.Test = "TestY"
	tests := []struct {
		name       string
		base, head []Declaration
		inDelta    bool   // ORD-F01 is in the change's ADDED or MODIFIED deltas
		want       string // "" for no finding
	}{
		{"unchanged", ds(a), ds(a), false, ""},
		{"moved to another file", ds(a), ds(d("c_test.go", "fp1", false)), false, ""},
		{"moved to another directory", ds(a), ds(d("sub/a_test.go", "fp1", false)), false,
			"test_removed ORD-F01 a_test.go:7\nfingerprint_changed ORD-F01 sub/a_test.go:7"},
		{"moved to another test function", ds(a), ds(inTestY), false,
			"test_removed ORD-F01 a_test.go:7\nfingerprint_changed ORD-F01 a_test.go:7"},
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
			changed := map[obligation.ID]bool{mustID(t, "ORD-F01"): tt.inDelta}
			if got := strings.Join(findings(t, Compare(tt.base, tt.head, changed)), "\n"); got != tt.want {
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
		map[obligation.ID]bool{mustID(t, "ORD-F02"): true},
	))
	want := "skip_added ORD-F01 p_test.go:18\ntest_removed ORD-F03 p_test.go:25"
	if strings.Join(got, "\n") != want {
		t.Errorf("Compare =\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}

// bypassBase is a package whose bound tests depend on a helper in another
// file, on a named function, on an import and on a golden file.
var bypassBase = map[string]string{
	"p/p_test.go": `package p

import (
	"strconv"
	"testing"
)

func TestP(t *testing.T) {
	t.Run("ORD-F01 adds", func(t *testing.T) {
		check(t, strconv.Itoa(add(1, 2)), "3")
	})
	t.Run("ORD-F02 named", testNamed)
	for _, tt := range []struct{ name, file string }{{name: "ORD-F03 golden", file: "sum.golden"}} {
		t.Run(tt.name, func(t *testing.T) {
			check(t, strconv.Itoa(add(1, 2)), golden(t, tt.file))
		})
	}
}

func testNamed(t *testing.T) {
	t.Run("sum", func(t *testing.T) { check(t, strconv.Itoa(add(2, 2)), "4") })
}
`,
	"p/helpers_test.go": `package p

import (
	"os"
	"path/filepath"
	"testing"
)

func check(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func golden(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
`,
	"p/testdata/sum.golden": "3",
}

// TestCompareBypasses replays ways to make a failing obligation pass without
// touching its subtest, and legitimate additions that must not be reported.
func TestCompareBypasses(t *testing.T) {
	t.Parallel()

	all := []string{"fingerprint_changed ORD-F01", "fingerprint_changed ORD-F02", "fingerprint_changed ORD-F03"}
	replace := func(file, old, repl string) func(*testing.T, map[string]string) {
		return func(t *testing.T, files map[string]string) { files[file] = edit(t, files[file], old, repl) }
	}
	rename := func(from, to string) func(*testing.T, map[string]string) {
		return func(_ *testing.T, files map[string]string) {
			files[to] = files[from]
			delete(files, from)
		}
	}
	enclosing := func(stmt string) func(*testing.T, map[string]string) {
		return replace("p/p_test.go", "func TestP(t *testing.T) {\n", "func TestP(t *testing.T) {\n"+stmt)
	}
	tests := []struct {
		name  string
		edits []func(*testing.T, map[string]string)
		want  []string
	}{
		{"import swapped", ds(replace("p/p_test.go", `"strconv"`, `strconv "example.com/fakestrconv"`)), all},
		{"moved to another package", ds(rename("p/p_test.go", "q/p_test.go"), rename("p/helpers_test.go", "q/helpers_test.go"), rename("p/testdata/sum.golden", "q/testdata/sum.golden")),
			[]string{"test_removed ORD-F01", "fingerprint_changed ORD-F01", "test_removed ORD-F02", "fingerprint_changed ORD-F02", "test_removed ORD-F03", "fingerprint_changed ORD-F03"}},
		{"moved to another test function", ds(replace("p/p_test.go", "\tt.Run(\"ORD-F02 named\", testNamed)\n", ""), replace("p/p_test.go", "func testNamed", "func TestQ(t *testing.T) { t.Run(\"ORD-F02 named\", testNamed) }\n\nfunc testNamed")),
			[]string{"test_removed ORD-F02", "fingerprint_changed ORD-F02"}},
		{"shadowing in the enclosing test", ds(enclosing("\tadd := func(int, int) int { return 3 }\n")), all},
		{"early return", ds(enclosing("\tif true {\n\t\treturn\n\t}\n")), all},
		{"testing.Short guard", ds(enclosing("\tif testing.Short() {\n\t\treturn\n\t}\n")), all},
		{"skip helper", ds(enclosing("\tskipUnlessDB(t)\n"), replace("p/helpers_test.go", "func check", "func skipUnlessDB(t *testing.T) { t.Skip(\"no db\") }\n\nfunc check")), all},
		{"weakened helper", ds(replace("p/helpers_test.go", "if got != want {", "if got != want && false {")), all},
		{"weakened named function", ds(replace("p/p_test.go", `strconv.Itoa(add(2, 2)), "4"`, `"4", "4"`)), []string{"fingerprint_changed ORD-F02"}},
		{"golden rewritten", ds(replace("p/testdata/sum.golden", "3", "4")), all},
		{"file limited to plan9", ds(rename("p/p_test.go", "p/p_plan9_test.go")), all},
		{"original wrapped in if false", ds(replace("p/p_test.go", "\tt.Run(\"ORD-F01", "\tif false {\n\t\tt.Run(\"ORD-F01"), replace("p/p_test.go", "\t})\n\tt.Run(\"ORD-F02", "\t})\n\t}\n\tt.Run(\"ORD-F02")), all},
		{"sibling subtest added", ds(replace("p/p_test.go", "\t\"strconv\"\n", "\t\"fmt\"\n\t\"strconv\"\n"),
			replace("p/p_test.go", "\tt.Run(\"ORD-F02", "\tt.Run(\"ORD-F09 new\", func(t *testing.T) { check(t, fmt.Sprint(extra()), \"1\") })\n\tt.Run(\"ORD-F02"),
			replace("p/helpers_test.go", "func check", "func extra() int { return 1 }\n\nfunc check")), nil},
		{"table entry added", ds(replace("p/p_test.go", `file: "sum.golden"}}`, `file: "sum.golden"}, {name: "ORD-F08 again", file: "sum.golden"}}`)), nil},
		{"TestMain added", ds(func(_ *testing.T, files map[string]string) {
			files["p/main_test.go"] = "package p\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(0) }\n"
		}), all},
		{"unrelated test file added", ds(func(_ *testing.T, files map[string]string) {
			files["p/other_test.go"] = "package p\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) { t.Skip() }\n"
		}), nil},
	}
	base := scanFiles(t, bypassBase)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := maps.Clone(bypassBase)
			for _, e := range tt.edits {
				e(t, files)
			}
			var got []string
			for _, f := range Compare(base, scanFiles(t, files), nil) {
				got = append(got, string(f.Kind)+" "+f.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Compare = %q, want %q", got, tt.want)
			}
		})
	}
}

func ds[T any](xs ...T) []T { return xs }
