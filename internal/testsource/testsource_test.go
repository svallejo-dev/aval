package testsource

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// scanFiles writes files (slash path → source) into a temporary directory
// and scans it.
func scanFiles(t *testing.T, files map[string]string) []Declaration {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	decls, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return decls
}

// describe renders d without its fingerprint, which TestFingerprint covers.
func describe(decls []Declaration) []string {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		s := fmt.Sprintf("%s %s:%d %s %s", d.ID, d.File, d.Line, d.Test, d.Kind)
		if d.Skips {
			s += " skips"
		}
		out = append(out, s)
	}
	return out
}

const declSrc = `package pay

import "testing"

var shared = []struct{ name string }{{name: "PAY-F10 package-level table"}}

func TestPay(t *testing.T) {
	t.Run("PAY-F01 refunds once", func(t *testing.T) {})
	t.Run("PAY-F02", func(t *testing.T) {})
	t.Run("PAY-F03\ttab after the ID", func(t *testing.T) {})
	t.Run("PAY-F04: no blank after the ID", func(t *testing.T) {})
	t.Run("no ID", func(t *testing.T) { t.Run("PAY-N01 nested", func(t *testing.T) {}) })
}

func TestTables(t *testing.T) {
	tests := []struct {
		name string
		n    int
	}{{name: "PAY-F05 keyed", n: 1}, {"PAY-F06 unkeyed", 2}, {name: "no ID", n: 3}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { _ = tt.n })
	}
	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {})
	}
	for name, n := range map[string]int{"PAY-F07 map key": 1} {
		t.Run(name, func(t *testing.T) { _ = n })
	}
	for _, tt := range shared {
		t.Run(tt.name, func(t *testing.T) {})
	}
	cases := []struct{ desc, want string }{{desc: "x", want: "PAY-F08 not the run field"}}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) { _ = c.want })
	}
}

func (s *suite) TestSuite() { s.Run("PAY-F09 suite method", func() {}) }
`

func TestScanDeclarations(t *testing.T) {
	t.Parallel()

	ignored := "package pay\nfunc TestX(t *testing.T) { t.Run(\"PAY-F99 x\", func(t *testing.T) {}) }\n"
	got := describe(scanFiles(t, map[string]string{
		"pay/pay_test.go":        declSrc,
		"pay/pay.go":             ignored, // not a test file
		"pay/_draft_test.go":     ignored,
		"pay/testdata/x_test.go": ignored,
		"vendor/v/v_test.go":     ignored,
		".git/g_test.go":         ignored,
		"_old/o_test.go":         ignored,
	}))
	want := []string{
		"PAY-F10 pay/pay_test.go:5 TestTables table_entry",
		"PAY-F01 pay/pay_test.go:8 TestPay subtest",
		"PAY-F02 pay/pay_test.go:9 TestPay subtest",
		"PAY-F03 pay/pay_test.go:10 TestPay subtest",
		"PAY-N01 pay/pay_test.go:12 TestPay subtest",
		"PAY-F05 pay/pay_test.go:19 TestTables table_entry",
		"PAY-F06 pay/pay_test.go:19 TestTables table_entry",
		"PAY-F07 pay/pay_test.go:26 TestTables table_entry",
		"PAY-F09 pay/pay_test.go:38 (*suite).TestSuite subtest",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Scan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestScanFixture scans a real test file of the go test -json fixtures.
func TestScanFixture(t *testing.T) {
	t.Parallel()

	decls, err := Scan(filepath.Join("..", "gotest", "testdata", "fixturemod", "pass"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ORD-F01 order_test.go:18 TestOrder table_entry",
		"ORD-F01 order_test.go:19 TestOrder table_entry", // go test runs it as ORD-F01#01
		"ORD-F02 order_test.go:20 TestOrder table_entry",
		"ORD-N01 order_test.go:21 TestOrder table_entry",
		"ORD-N01 order_test.go:22 TestOrder table_entry",
		"ORD-F03 order_test.go:23 TestOrder table_entry", // not ORD-F04: at line 24
		"ORD-F05 order_test.go:45 TestOrderNested subtest",
		"ORD-F06 order_test.go:65 TestOrderNested subtest",
		"ORD-F07 order_test.go:92 TestOrderSkip subtest skips",
	}
	if got := describe(decls); !slices.Equal(got, want) {
		t.Fatalf("Scan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if decls[0].Fingerprint == decls[1].Fingerprint {
		t.Error("the two ORD-F01 entries share a fingerprint, want one each")
	}
}

const fpBase = `package p

import "testing"

func TestP(t *testing.T) {
	t.Run("ORD-F01 adds", func(t *testing.T) {
		if got := add(1, 2); got != 3 {
			t.Errorf("add = %d", got)
		}
	})
	tests := []struct {
		name string
		in   []int
	}{
		{name: "ORD-F02 sums one", in: []int{1}},
		{name: "ORD-F03 sums two", in: []int{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if sum(tt.in...) < 0 {
				t.Error("negative")
			}
		})
	}
}
`

// edit returns src with the first old replaced by repl.
func edit(t *testing.T, src, old, repl string) string {
	t.Helper()
	if !strings.Contains(src, old) {
		t.Fatalf("%q not in the source", old)
	}
	return strings.Replace(src, old, repl, 1)
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	fingerprints := func(t *testing.T, src string) map[string]string {
		t.Helper()
		m := map[string]string{}
		for _, d := range scanFiles(t, map[string]string{"p_test.go": src}) {
			m[d.ID.String()] = d.Fingerprint
		}
		if len(m) != 3 {
			t.Fatalf("found %d IDs, want 3", len(m))
		}
		return m
	}
	base := fingerprints(t, fpBase)
	for id, fp := range base {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fp) {
			t.Errorf("%s fingerprint %q is not a hex SHA-256", id, fp)
		}
	}

	// Only comments, blank lines, line breaks and positions differ.
	reformatted := edit(t, fpBase, "func TestP", "func TestOther(t *testing.T) {}\n\n// TestP moved.\nfunc TestP")
	reformatted = edit(t, reformatted, "add(1, 2)", "add(1, // one\n\n\t\t\t2)")
	reformatted = edit(t, reformatted, "{name: \"ORD-F02 sums one\", in: []int{1}}", "{\n\t\t\tname: \"ORD-F02 sums one\",\n\t\t\tin:   []int{1},\n\t\t}")
	reformatted = edit(t, reformatted, "{\n\t\t\t\tt.Error(\"negative\")\n\t\t\t}", "{ t.Error(\"negative\") }")
	tests := []struct {
		name    string
		src     string
		changed []string // IDs whose fingerprint must change; the others must not
	}{
		{name: "reformatted, commented and moved", src: reformatted},
		{name: "assertion value", src: edit(t, fpBase, "got != 3", "got != 4"), changed: []string{"ORD-F01"}},
		{name: "assertion operator", src: edit(t, fpBase, "got != 3", "got == 3"), changed: []string{"ORD-F01"}},
		{name: "statement removed", src: edit(t, fpBase, "\t\t\tt.Errorf(\"add = %d\", got)\n", ""), changed: []string{"ORD-F01"}},
		{name: "skip added", src: edit(t, fpBase, "\t\tif got", "\t\tt.Skip()\n\t\tif got"), changed: []string{"ORD-F01"}},
		{name: "title", src: edit(t, fpBase, "ORD-F01 adds", "ORD-F01 adds two"), changed: []string{"ORD-F01"}},
		{name: "table entry", src: edit(t, fpBase, "in: []int{1}}", "in: []int{2}}"), changed: []string{"ORD-F02"}},
		{name: "table runner", src: edit(t, fpBase, "< 0", "<= 0"), changed: []string{"ORD-F02", "ORD-F03"}},
		{name: "variadic call", src: edit(t, fpBase, "sum(tt.in...)", "sum(tt.in)"), changed: []string{"ORD-F02", "ORD-F03"}},
		{name: "build constraint", src: "//go:build ignore\n\n" + fpBase, changed: []string{"ORD-F01", "ORD-F02", "ORD-F03"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fingerprints(t, tt.src)
			for id, fp := range base {
				if changed := got[id] != fp; changed != slices.Contains(tt.changed, id) {
					t.Errorf("%s fingerprint changed = %v, want %v", id, changed, !changed)
				}
			}
		})
	}
}

func TestScanSkips(t *testing.T) {
	t.Parallel()

	src := `package p

import "testing"

func TestOwn(t *testing.T) {
	t.Run("ORD-F01 skip", func(t *testing.T) { t.Skip("x") })
	t.Run("ORD-F02 skipf", func(t *testing.T) { t.Skipf("%d", 1) })
	t.Run("ORD-F03 skipnow in a nested subtest", func(t *testing.T) {
		t.Run("inner", func(t *testing.T) { t.SkipNow() })
	})
	t.Run("ORD-F04 runs", func(t *testing.T) {})
	t.Run("parent", func(t *testing.T) {
		t.Skip("skips its subtests")
		t.Run("ORD-F05 under a skipping parent", func(t *testing.T) {})
	})
}

func TestWhole(t *testing.T) {
	t.Run("ORD-F06 in a test that skips", func(t *testing.T) {})
	if testing.Short() {
		t.Skip("slow")
	}
}

func TestTable(t *testing.T) {
	for _, tt := range []struct{ name string; flaky bool }{{name: "ORD-F07 a"}, {name: "ORD-F08 b", flaky: true}} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flaky {
				t.Skip("flaky")
			}
		})
	}
	for _, tt := range []struct{ name string }{{name: "ORD-F09 other table"}} {
		t.Run(tt.name, func(t *testing.T) {})
	}
}
`
	var got []string
	for _, d := range scanFiles(t, map[string]string{"p_test.go": src}) {
		if d.Skips {
			got = append(got, d.ID.String())
		}
	}
	// ORD-F04 and ORD-F09 do not skip: a sibling's skip is not theirs.
	if want := []string{"ORD-F01", "ORD-F02", "ORD-F03", "ORD-F05", "ORD-F06", "ORD-F07", "ORD-F08"}; !slices.Equal(got, want) {
		t.Errorf("skipping declarations = %v, want %v", got, want)
	}
}

func TestScanErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bad := "package p\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) {\n\tx := := 1\n}\n"
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "bad_test.go"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if decls, err := Scan(dir); err == nil || !strings.Contains(err.Error(), "sub/bad_test.go:6:") || decls != nil {
		t.Errorf("Scan of a malformed file = %v, %v; want no declarations and an error at sub/bad_test.go:6", decls, err)
	}
	if _, err := Scan(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Scan of a missing directory = %v, want fs.ErrNotExist", err)
	}
}
