package testsource

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// scanFiles writes files (slash path → content) into a temporary directory
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

// describe renders declarations without their fingerprints.
func describe(decls []Declaration) []string {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		s := fmt.Sprintf("%s %q %s:%d %s %s", d.ID, d.Name, d.File, d.Line, d.Test, d.Kind)
		if d.Skips {
			s += " skips"
		}
		out = append(out, s)
	}
	return out
}

// edit returns src with the first old replaced by repl.
func edit(t *testing.T, src, old, repl string) string {
	t.Helper()
	if !strings.Contains(src, old) {
		t.Fatalf("%q not in the source", old)
	}
	return strings.Replace(src, old, repl, 1)
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
	tests = nil // a later definition does not name the loop's table
	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {})
	}
	for name, n := range map[string]int{"PAY-F07 map key": 1} {
		t.Run(name, func(t *testing.T) { _ = n })
	}
	for _, c := range map[string]struct{ name string }{"k": {name: "PAY-F11 map value"}} {
		t.Run(c.name, func(t *testing.T) {})
	}
	for _, tt := range shared {
		t.Run(tt.name, func(t *testing.T) {})
	}
	cases := []struct{ desc, want string }{{desc: "x", want: "PAY-F08 not the run field"}}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) { _ = c.want })
	}
	for _, v := range map[string]string{"PAY-F12 a key, not the name": "no ID"} {
		t.Run(v, func(t *testing.T) {})
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
		`PAY-F10 "PAY-F10 package-level table" pay/pay_test.go:5 TestTables table_entry`,
		`PAY-F01 "PAY-F01 refunds once" pay/pay_test.go:8 TestPay subtest`,
		`PAY-F02 "PAY-F02" pay/pay_test.go:9 TestPay subtest`,
		`PAY-F03 "PAY-F03\ttab after the ID" pay/pay_test.go:10 TestPay subtest`,
		`PAY-N01 "PAY-N01 nested" pay/pay_test.go:12 TestPay subtest`,
		`PAY-F05 "PAY-F05 keyed" pay/pay_test.go:19 TestTables table_entry`,
		`PAY-F06 "PAY-F06 unkeyed" pay/pay_test.go:19 TestTables table_entry`,
		`PAY-F07 "PAY-F07 map key" pay/pay_test.go:27 TestTables table_entry`,
		`PAY-F11 "PAY-F11 map value" pay/pay_test.go:30 TestTables table_entry`,
		`PAY-F09 "PAY-F09 suite method" pay/pay_test.go:45 (*suite).TestSuite subtest`,
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
		`ORD-F01 "ORD-F01 rejects duplicates" order_test.go:18 TestOrder table_entry`,
		`ORD-F01 "ORD-F01 rejects duplicates" order_test.go:19 TestOrder table_entry`, // go test: ORD-F01#01
		`ORD-F02 "ORD-F02 accepts distinct skus" order_test.go:20 TestOrder table_entry`,
		`ORD-N01 "ORD-N01" order_test.go:21 TestOrder table_entry`,
		`ORD-N01 "ORD-N01" order_test.go:22 TestOrder table_entry`,
		`ORD-F03 "ORD-F03 keeps in/out order" order_test.go:23 TestOrder table_entry`, // not ORD-F04: at 24
		`ORD-F05 "ORD-F05 lists skus in insertion order" order_test.go:45 TestOrderNested subtest`,
		`ORD-F06 "ORD-F06 accepts a single sku" order_test.go:65 TestOrderNested subtest`,
		`ORD-F07 "ORD-F07 survives a restart" order_test.go:92 TestOrderSkip subtest skips`,
	}
	if got := describe(decls); !slices.Equal(got, want) {
		t.Fatalf("Scan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if decls[0].Fingerprint == decls[1].Fingerprint {
		t.Error("the two ORD-F01 entries share a fingerprint, want one each")
	}
}

const fpBase = `package p

import (
	sc "strconv"
	"testing"

	"example.com/go-yaml"
	"example.com/mod/v2"
	"gopkg.in/check.v1"
)

type tc struct {
	name string
	in   []int
}

func TestP(t *testing.T) {
	t.Run("ORD-F01 adds", func(t *testing.T) {
		if got := add(1, 2); got != 3 {
			t.Errorf("add = %d", got)
		}
	})
	tests := []tc{
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

type suite struct{ n int }

func (s *suite) setup() { s.n = 1 }

func (s *suite) TestSuite(t *testing.T) {
	s.setup()
	t.Run("ORD-F04 in a suite", func(t *testing.T) {
		if s.n != 1 {
			t.Error("setup")
		}
	})
}

func runHelper(t *testing.T) {
	t.Run("ORD-F05 in a helper", func(t *testing.T) {
		_ = []*tc{{name: sc.Itoa(1)}}
		_ = map[tc]tc{{name: "k"}: {name: yaml.Name}}
		t.Run("inner", func(t *testing.T) { t.Log(mod.Version, check.Version) })
	})
}
`

func TestFingerprint(t *testing.T) {
	t.Parallel()

	ids := []string{"ORD-F01", "ORD-F02", "ORD-F03", "ORD-F04", "ORD-F05"}
	inTestP, usesTC := ids[:3], []string{"ORD-F01", "ORD-F02", "ORD-F03", "ORD-F05"}
	fingerprints := func(t *testing.T, src string) map[string]string {
		t.Helper()
		m := map[string]string{}
		for _, d := range scanFiles(t, map[string]string{"p_test.go": src}) {
			m[d.ID.String()] = d.Fingerprint
		}
		for _, id := range ids {
			if m[id] == "" {
				t.Fatalf("%s not found", id)
			}
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
	reformatted = edit(t, reformatted, `{name: "ORD-F02 sums one", in: []int{1}}`, "{\n\t\t\tname: \"ORD-F02 sums one\",\n\t\t\tin:   []int{1},\n\t\t}")
	reformatted = edit(t, reformatted, "{\n\t\t\t\tt.Error(\"negative\")\n\t\t\t}", "{ t.Error(\"negative\") }")
	tests := []struct {
		name    string
		src     string
		changed []string // IDs whose fingerprint must change; the others must not
	}{
		{name: "reformatted, commented and moved", src: reformatted},
		{name: "element types written out", src: edit(t, edit(t, edit(t, fpBase, `{name: "ORD-F02`, `tc{name: "ORD-F02`),
			`[]*tc{{name: sc.Itoa(1)}}`, `[]*tc{&tc{name: sc.Itoa(1)}}`),
			`map[tc]tc{{name: "k"}: {name: yaml.Name}}`, `map[tc]tc{tc{name: "k"}: tc{name: yaml.Name}}`)},
		{name: "sibling subtest added", src: edit(t, edit(t, fpBase, "\tsc \"strconv\"\n", "\t\"fmt\"\n\tsc \"strconv\"\n"),
			"\ttests :=", "\tt.Run(\"ORD-F09 new\", func(t *testing.T) { t.Log(fmt.Sprint(extra())) })\n\ttests :=")},
		{name: "table entry added", src: edit(t, fpBase, "\t}\n\tfor", "\t\t{name: \"ORD-F04 sums three\", in: []int{1, 2, 3}},\n\t}\n\tfor")},
		{name: "unrelated helper and test added", src: fpBase + "\nfunc extra() int { return 1 }\n\nfunc TestOther(t *testing.T) { t.Skip() }\n"},
		{name: "helper named like a method", src: fpBase + "\nfunc Error(string) {}\n"},
		{name: "subtest added to another suite method", src: fpBase + "\nfunc (s *suite) TestMore(t *testing.T) { t.Run(\"x\", func(t *testing.T) {}) }\n"},
		{name: "sibling added in a helper", src: edit(t, fpBase, "{\n\tt.Run(\"ORD-F05", "{\n\tt.Run(\"x\", func(t *testing.T) {})\n\tt.Run(\"ORD-F05")},
		{name: "suite helper method", src: edit(t, fpBase, "s.n = 1", "s.n = 2"), changed: []string{"ORD-F04"}},
		{name: "nested subtest edited", src: edit(t, fpBase, "t.Log(mod.Version,", "t.Log(mod.Version + \"x\","), changed: []string{"ORD-F05"}},
		{name: "aliased import swapped", src: edit(t, fpBase, `sc "strconv"`, `sc "example.com/strconv"`), changed: []string{"ORD-F05"}},
		{name: "versioned import swapped", src: edit(t, fpBase, `"example.com/mod/v2"`, `"example.com/mod/v3"`), changed: []string{"ORD-F05"}},
		{name: "gopkg.in import swapped", src: edit(t, fpBase, `"gopkg.in/check.v1"`, `"gopkg.in/check.v2"`), changed: []string{"ORD-F05"}},
		{name: "go- import swapped", src: edit(t, fpBase, `"example.com/go-yaml"`, `"example.com/go-yaml2"`), changed: []string{"ORD-F05"}},
		{name: "assertion value", src: edit(t, fpBase, "got != 3", "got != 4"), changed: []string{"ORD-F01"}},
		{name: "assertion operator", src: edit(t, fpBase, "got != 3", "got == 3"), changed: []string{"ORD-F01"}},
		{name: "statement removed", src: edit(t, fpBase, "\t\t\tt.Errorf(\"add = %d\", got)\n", ""), changed: []string{"ORD-F01"}},
		{name: "skip added", src: edit(t, fpBase, "\t\tif got", "\t\tt.Skip()\n\t\tif got"), changed: []string{"ORD-F01"}},
		{name: "title", src: edit(t, fpBase, "ORD-F01 adds", "ORD-F01 adds two"), changed: []string{"ORD-F01"}},
		{name: "table entry", src: edit(t, fpBase, "in: []int{1}}", "in: []int{2}}"), changed: []string{"ORD-F02"}},
		{name: "table runner", src: edit(t, fpBase, "< 0", "<= 0"), changed: []string{"ORD-F02", "ORD-F03"}},
		{name: "variadic call", src: edit(t, fpBase, "sum(tt.in...)", "sum(tt.in)"), changed: []string{"ORD-F02", "ORD-F03"}},
		{name: "early return", src: edit(t, fpBase, "{\n\tt.Run", "{\n\tif testing.Short() {\n\t\treturn\n\t}\n\tt.Run"), changed: inTestP},
		{name: "helper type", src: edit(t, fpBase, "in   []int", "in   []int8"), changed: usesTC},
		{name: "package clause", src: edit(t, fpBase, "package p", "package p_test"), changed: ids},
		{name: "build constraint", src: "//go:build ignore\n\n" + fpBase, changed: ids},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fingerprints(t, tt.src)
			for _, id := range ids {
				if changed := got[id] != base[id]; changed != slices.Contains(tt.changed, id) {
					t.Errorf("%s fingerprint changed = %v, want %v", id, changed, !changed)
				}
			}
		})
	}
}

func TestBuildConstraint(t *testing.T) {
	t.Parallel()

	plain, err := parser.ParseFile(token.NewFileSet(), "", "package p\n", parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := parser.ParseFile(token.NewFileSet(), "", "//go:build linux&&!cgo\n\npackage p\n", parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		file *ast.File
		name string
		want string
	}{
		{plain, "p_test.go", ""},
		{plain, "linux_test.go", ""}, // no underscore before the GOOS
		{plain, "p_foo_test.go", ""},
		{plain, "p_plan9_test.go", "plan9"},
		{plain, "p_amd64_test.go", "amd64"},
		{plain, "p_linux_amd64_test.go", "linux\namd64"},
		{plain, "p_amd64_linux_test.go", "linux"},
		{tagged, "p_windows_test.go", "windows\nlinux && !cgo"},
	} {
		if got := buildConstraint(tt.file, tt.name); got != tt.want {
			t.Errorf("buildConstraint(%s) = %q, want %q", tt.name, got, tt.want)
		}
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
	t.Run("ORD-F10 skip on t from a closure", func(t *testing.T) { defer func() { t.Skip() }() })
	t.Run("ORD-F11 skip on another receiver", func(t *testing.T) { var s skipper; s.Skip() })
	t.Run("ORD-F12 named function", skipNamed)
	t.Run("ORD-F14 skip on a testing.TB", func(t *testing.T) { func(tb testing.TB) { tb.Skip() }(t) })
}

func skipNamed(t *testing.T) { t.Skip() }

func TestWhole(t *testing.T) {
	t.Run("ORD-F06 in a test that skips", func(t *testing.T) {})
	for _, tt := range []struct{ name string }{{name: "ORD-F09 table in a test that skips"}} {
		t.Run(tt.name, func(t *testing.T) {})
	}
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
	for _, tt := range []struct{ name string }{{name: "ORD-F13 other table"}} {
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
	// ORD-F04 and ORD-F13 do not skip: a sibling's skip is not theirs.
	want := []string{"ORD-F01", "ORD-F02", "ORD-F03", "ORD-F05", "ORD-F10", "ORD-F12", "ORD-F14", "ORD-F06", "ORD-F09", "ORD-F07", "ORD-F08"}
	if !slices.Equal(got, want) {
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
