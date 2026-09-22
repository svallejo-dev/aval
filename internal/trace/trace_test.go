package trace

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/testsource"
)

const (
	specPath = "openspec/specs/refunds/spec.md"
	testFile = "refund/refund_test.go"
	pkg      = "example.com/shop/refund"
)

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repo    *openspec.Repo
		decls   []testsource.Declaration
		runtime *gotest.Report // nil: the tests did not run
		want    []string       // describe(Build(...))
	}{
		{
			name:  "traced",
			repo:  specs(t, "ORD-F01 Refund is idempotent"),
			decls: []testsource.Declaration{decl(t, "ORD-F01 refunds once", 6, "TestRefund")},
			want:  []string{"row ORD-F01 F require_test traced spec:1 tests=[refund/refund_test.go:6]"},
		},
		{
			name: "F, N and I without a test are unverified",
			repo: specs(t, "ORD-F01 a", "ORD-N01 b", "ORD-I01 c"),
			want: []string{
				"row ORD-F01 F require_test unverified spec:1 tests=[]",
				"row ORD-I01 I require_test unverified spec:3 tests=[]",
				"row ORD-N01 N require_test unverified spec:2 tests=[]",
				"orphan unverified ORD-F01 openspec/specs/refunds/spec.md:1",
				"orphan unverified ORD-I01 openspec/specs/refunds/spec.md:3",
				"orphan unverified ORD-N01 openspec/specs/refunds/spec.md:2",
			},
		},
		{
			name: "S and A without a test only warn",
			repo: specs(t, "ORD-S01 fast", "ORD-A01 gateway confirms"),
			want: []string{
				"row ORD-A01 A warn untested spec:2 tests=[]",
				"row ORD-S01 S warn untested spec:1 tests=[]",
			},
		},
		{
			name:  "an open question blocks, with or without a test",
			repo:  specs(t, "ORD-O02 which currency", "ORD-O01 who approves"),
			decls: []testsource.Declaration{decl(t, "ORD-O01 approver", 6, "TestRefund")},
			want: []string{
				"row ORD-O01 O block blocking spec:2 tests=[refund/refund_test.go:6]",
				"row ORD-O02 O block blocking spec:1 tests=[]",
				"blocking ORD-O01",
				"blocking ORD-O02",
			},
		},
		{
			name: "a test of an ID no spec defines is an orphan",
			repo: specs(t, "ORD-F01 a"),
			decls: []testsource.Declaration{
				decl(t, "ORD-F09 nobody asked", 20, "TestRefund"),
				decl(t, "ORD-F01 a", 6, "TestRefund"),
			},
			want: []string{
				"row ORD-F01 F require_test traced spec:1 tests=[refund/refund_test.go:6]",
				"orphan orphan_test ORD-F09 refund/refund_test.go:20 TestRefund",
			},
		},
		{
			name: "duplicate and invalid IDs: first definition wins, no ID no row",
			repo: &openspec.Repo{Specs: []openspec.Spec{{Requirements: []openspec.Requirement{
				req(t, "ORD-F01 first", 1), {Name: "[ORD-F02] bracketed", Path: specPath, Line: 2}, req(t, "ORD-F01 second", 3),
			}}}},
			want: []string{"row ORD-F01 F require_test unverified spec:1 tests=[]", "orphan unverified ORD-F01 openspec/specs/refunds/spec.md:1"},
		},
		{
			name: "ADDED in an active change defines an obligation, archived history does not",
			repo: &openspec.Repo{Changes: []openspec.Change{
				{ID: "add-limits", Deltas: []openspec.Delta{{Op: openspec.Added, Requirement: req(t, "ORD-F02 limit", 4)}, {Op: openspec.Removed, Requirement: req(t, "ORD-F05 gone", 9)}}},
				{ID: "old", Archived: true, Deltas: []openspec.Delta{{Op: openspec.Added, Requirement: req(t, "ORD-F03 old", 2)}}},
			}},
			decls: []testsource.Declaration{decl(t, "ORD-F02 limit", 6, "TestRefund"), decl(t, "ORD-F03 old", 9, "TestRefund")},
			want: []string{
				"row ORD-F02 F require_test traced spec:4 change=add-limits tests=[refund/refund_test.go:6]",
				"orphan orphan_test ORD-F03 refund/refund_test.go:9 TestRefund",
			},
		},
		{
			name: "runtime: each declaration gets its tests' outcome and the row the worst",
			repo: specs(t, "ORD-F01 a", "ORD-N01 b", "ORD-I01 c"),
			decls: []testsource.Declaration{
				decl(t, "ORD-F01 refunds once", 6, "TestRefund"),
				decl(t, "ORD-F01 refunds\tonce more", 12, "TestRefund"),
				decl(t, "ORD-N01 caps", 20, "TestRefund"),
				decl(t, "ORD-I01 ledger", 30, "(*Suite).TestLedger"),
			},
			runtime: report(evidence.Fail,
				ran("TestRefund/ORD-F01_refunds_once", evidence.Pass),
				ran("TestRefund/ORD-F01_refunds_once_more", evidence.Fail),
				ran("TestRefund/ORD-N01_caps", evidence.Pass),
				ran("TestRefund/ORD-N01_caps/negative", evidence.Skipped),
				ran("TestSuite/TestLedger/ORD-I01_ledger#01", evidence.Pass),
			),
			want: []string{
				"row ORD-F01 F require_test traced spec:1 run=fail tests=[refund/refund_test.go:6 pass, refund/refund_test.go:12 fail]",
				"row ORD-I01 I require_test traced spec:3 run=pass tests=[refund/refund_test.go:30 pass]",
				"row ORD-N01 N require_test traced spec:2 run=skipped tests=[refund/refund_test.go:20 skipped]",
			},
		},
		{
			name:    "runtime: a declaration that did not run, or whose package did not build",
			repo:    specs(t, "ORD-F01 a", "ORD-F02 b"),
			decls:   []testsource.Declaration{decl(t, "ORD-F01 a", 6, "TestRefund"), {ID: id(t, "ORD-F02"), Name: "ORD-F02 b", File: "other/other_test.go", Line: 3, Test: "TestOther"}},
			runtime: &gotest.Report{Packages: []gotest.Package{{Name: pkg, Status: evidence.Pass}, {Name: "example.com/shop/other", Status: evidence.BuildFail}}},
			want: []string{
				"row ORD-F01 F require_test traced spec:1 run=not_run tests=[refund/refund_test.go:6 not_run]",
				"row ORD-F02 F require_test traced spec:2 run=build_fail tests=[other/other_test.go:3 build_fail]",
			},
		},
		{
			name:  "runtime: an ID bound only at run time is undeclared, and warnings pass through",
			repo:  specs(t, "ORD-F07 dynamic"),
			decls: []testsource.Declaration{decl(t, "ORD-F01 a", 6, "TestRefund")},
			runtime: func() *gotest.Report {
				r := report(evidence.Pass, ran("TestRefund/ORD-F07_dynamic", evidence.Pass), ran("TestRefund/ORD-F01_a", evidence.Pass))
				r.Warnings = []gotest.Warning{
					{Kind: gotest.SuspiciousSegment, Package: pkg, Test: "TestRefund/ORD-F04:_x", Detail: "ORD-F04:_x"},
					{Kind: gotest.InvalidAttr, Package: pkg, Test: "TestRefund", Detail: "nope"},
				}
				return r
			}(),
			want: []string{
				"row ORD-F07 F require_test unverified spec:1 run=pass tests=[]",
				"orphan orphan_test ORD-F01 refund/refund_test.go:6 TestRefund",
				"orphan undeclared_runtime ORD-F07 example.com/shop/refund TestRefund/ORD-F07_dynamic",
				"orphan unverified ORD-F07 openspec/specs/refunds/spec.md:1",
				"warning invalid_attr TestRefund nope",
				"warning suspicious_segment TestRefund/ORD-F04:_x ORD-F04:_x",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := Build(tt.repo, tt.decls, tt.runtime)
			if m.RanTests != (tt.runtime != nil) {
				t.Errorf("RanTests = %v, want %v", m.RanTests, tt.runtime != nil)
			}
			if got := describe(m); !slices.Equal(got, tt.want) {
				t.Errorf("Build:\ngot  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestBuildNeverNil pins the JSON contract: empty lists are [], never null.
func TestBuildNeverNil(t *testing.T) {
	t.Parallel()
	m := Build(&openspec.Repo{}, nil, nil)
	if m.Rows == nil || m.Orphans == nil || m.Warnings == nil || m.Blocking == nil {
		t.Errorf("Build of an empty repository = %+v, want empty, non-nil lists", m)
	}
}

// describe renders m one line per row, orphan, warning and blocking ID.
func describe(m Matrix) []string {
	var out []string
	for _, r := range m.Rows {
		s := fmt.Sprintf("row %s %s %s %s spec:%d", r.ID, r.Kind, r.Policy, r.Status, r.Line)
		if r.Change != "" {
			s += " change=" + r.Change
		}
		if r.Runtime != "" {
			s += " run=" + string(r.Runtime)
		}
		tests := make([]string, 0, len(r.Tests))
		for _, t := range r.Tests {
			tests = append(tests, strings.TrimSpace(fmt.Sprintf("%s:%d %s", t.File, t.Line, t.Runtime)))
		}
		out = append(out, s+" tests=["+strings.Join(tests, ", ")+"]")
	}
	for _, o := range m.Orphans {
		place := fmt.Sprintf("%s:%d %s", o.Path, o.Line, o.Test)
		if o.Kind == OrphanRuntime {
			place = o.Package + " " + o.Test
		}
		out = append(out, strings.TrimSpace(fmt.Sprintf("orphan %s %s %s", o.Kind, o.ID, place)))
	}
	for _, w := range m.Warnings {
		out = append(out, fmt.Sprintf("warning %s %s %s", w.Kind, w.Test, w.Detail))
	}
	for _, b := range m.Blocking {
		out = append(out, "blocking "+b)
	}
	return out
}

func id(t *testing.T, s string) obligation.ID {
	t.Helper()
	v, err := obligation.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func req(t *testing.T, name string, line int) openspec.Requirement {
	t.Helper()
	v, title, ok := obligation.FromRequirementName(name)
	if !ok {
		t.Fatalf("%q does not start with an ID", name)
	}
	return openspec.Requirement{ID: v, Title: title, Name: name, Path: specPath, Line: line}
}

// specs is a repository with one main spec whose requirements are names, on
// lines 1, 2, …
func specs(t *testing.T, names ...string) *openspec.Repo {
	t.Helper()
	s := openspec.Spec{Capability: "refunds", Path: specPath}
	for i, n := range names {
		s.Requirements = append(s.Requirements, req(t, n, i+1))
	}
	return &openspec.Repo{Specs: []openspec.Spec{s}}
}

func decl(t *testing.T, name string, line int, test string) testsource.Declaration {
	t.Helper()
	v, ok := obligation.FromTestSegment(strings.Fields(name)[0])
	if !ok {
		t.Fatalf("%q does not start with an ID", name)
	}
	return testsource.Declaration{ID: v, Name: name, File: testFile, Line: line, Test: test, Kind: testsource.Subtest}
}

// ran is a test of pkg bound, like gotest does, to the ID that starts the
// outermost segment of its name carrying one.
func ran(name string, status evidence.Status) gotest.TestOutcome {
	o := gotest.TestOutcome{Package: pkg, Name: name, Status: status}
	segs := strings.Split(name, "/")
	for i, s := range segs {
		if v, ok := obligation.FromTestSegment(s); ok {
			o.Bindings = []gotest.Binding{{ID: v, Owner: strings.Join(segs[:i+1], "/")}}
			break
		}
	}
	return o
}

func report(status evidence.Status, tests ...gotest.TestOutcome) *gotest.Report {
	return &gotest.Report{Packages: []gotest.Package{{Name: pkg, Status: status}}, Tests: tests}
}
