// Package trace builds the traceability matrix of a repository: each
// obligation its specs define, the tests that declare it and, when the tests
// ran, how they did, plus the gaps in both directions. It reads nothing
// itself: the matrix is a pure function of what openspec, testsource and
// gotest found.
package trace

import (
	"cmp"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/testsource"
)

// Status is where an obligation stands in the matrix.
type Status string

// Statuses of a row.
const (
	Traced     Status = "traced"     // at least one test declares it
	Unverified Status = "unverified" // its kind requires a test and none declares it
	Untested   Status = "untested"   // no test declares it, and its kind only warns
	Blocking   Status = "blocking"   // an open question: it blocks until a human resolves it
)

// Policy is obligation.Policy as the matrix reports it.
type Policy string

// Policies, one per obligation.Policy.
const (
	RequireTest Policy = "require_test"
	Warn        Policy = "warn"
	Block       Policy = "block"
)

// OrphanKind names a gap between the specs and the tests.
type OrphanKind string

// Kinds of orphan.
const (
	// OrphanUnverified is an obligation whose kind requires a test that no
	// test declares.
	OrphanUnverified OrphanKind = "unverified"
	// OrphanTest is a test declaring an ID that no spec defines.
	OrphanTest OrphanKind = "orphan_test"
	// OrphanRuntime is an ID that tests bound at run time although no test
	// declares it in its source: a name built at run time or a t.Attr. The
	// gate cannot fingerprint such tests, so it cannot tell when they change.
	OrphanRuntime OrphanKind = "undeclared_runtime"
)

// Matrix is the traceability matrix. Every list is sorted and never nil.
type Matrix struct {
	Rows     []Row     `json:"rows"`     // one per obligation, by ID
	Orphans  []Orphan  `json:"orphans"`  // by kind, ID and place
	Warnings []Warning `json:"warnings"` // test names that look bound and are not
	Blocking []string  `json:"blocking"` // IDs of the open questions
	RanTests bool      `json:"ranTests"` // the runtime fields are set
}

// Row is one obligation.
type Row struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // the kind's letter, e.g. "F"
	Policy Policy `json:"policy"`
	Title  string `json:"title"`
	Path   string `json:"path"` // spec or delta that defines it
	Line   int    `json:"line"`
	// Change is the active change whose ADDED delta defines the obligation;
	// empty when a main spec does.
	Change           string `json:"change,omitempty"`
	Characterization bool   `json:"characterization"`
	Status           Status `json:"status"`
	Tests            []Test `json:"tests"` // by file and line
	// Runtime is the worst outcome of every test bound to the ID at run time
	// (gotest.Report.Status); empty when the tests did not run.
	Runtime evidence.Status `json:"runtime,omitempty"`
}

// Test is a declaration that binds a test to the row's obligation.
type Test struct {
	File  string          `json:"file"`
	Line  int             `json:"line"`
	Test  string          `json:"test"` // enclosing top-level function
	Name  string          `json:"name"` // subtest name as written
	Kind  testsource.Kind `json:"kind"`
	Skips bool            `json:"skips"`
	// Runtime is the worst outcome of the tests this declaration ran as;
	// empty when the tests did not run.
	Runtime evidence.Status `json:"runtime,omitempty"`
}

// Orphan is an obligation without a test, or a test without an obligation.
type Orphan struct {
	Kind    OrphanKind `json:"kind"`
	ID      string     `json:"id"`
	Path    string     `json:"path,omitempty"` // spec, or test file of an orphan_test
	Line    int        `json:"line,omitempty"`
	Package string     `json:"package,omitempty"` // undeclared_runtime only
	Test    string     `json:"test,omitempty"`    // declaring function, or the full runtime name
}

// Warning is a test that seems to name an ID but binds none (gotest.Warning).
type Warning struct {
	Kind    gotest.WarningKind `json:"kind"`
	Package string             `json:"package"`
	Test    string             `json:"test"`
	Detail  string             `json:"detail"`
}

// Build traces the obligations of repo to decls, the declarations of a
// testsource.Scan of the repository root. runtime is the report of running
// the tests, or nil when they did not run.
//
// The obligations are the requirements with a valid ID of the main specs and
// of the ADDED deltas of active changes. A duplicate ID gets the row of its
// first definition; openspec.Repo.Check reports the others.
func Build(repo *openspec.Repo, decls []testsource.Declaration, runtime *gotest.Report) Matrix {
	m := Matrix{Rows: []Row{}, Orphans: []Orphan{}, Warnings: []Warning{}, Blocking: []string{}, RanTests: runtime != nil}

	declared := make(map[obligation.ID][]testsource.Declaration)
	for _, d := range decls {
		declared[d.ID] = append(declared[d.ID], d)
	}
	defined := make(map[obligation.ID]bool)
	for _, def := range definitions(repo) {
		if defined[def.req.ID] {
			continue
		}
		defined[def.req.ID] = true
		row := newRow(def, declared[def.req.ID], runtime)
		m.Rows = append(m.Rows, row)
		switch row.Status {
		case Unverified:
			m.Orphans = append(m.Orphans, Orphan{Kind: OrphanUnverified, ID: row.ID, Path: row.Path, Line: row.Line})
		case Blocking:
			m.Blocking = append(m.Blocking, row.ID)
		}
	}
	for _, d := range decls {
		if !defined[d.ID] {
			m.Orphans = append(m.Orphans, Orphan{Kind: OrphanTest, ID: d.ID.String(), Path: d.File, Line: d.Line, Test: d.Test})
		}
	}
	if runtime != nil {
		for id, owners := range runtime.Obligations() {
			if len(declared[id]) > 0 {
				continue
			}
			for _, t := range owners {
				m.Orphans = append(m.Orphans, Orphan{Kind: OrphanRuntime, ID: id.String(), Package: t.Package, Test: t.Name})
			}
		}
		for _, w := range runtime.Warnings {
			m.Warnings = append(m.Warnings, Warning(w))
		}
	}

	slices.SortFunc(m.Rows, func(a, b Row) int { return cmp.Compare(a.ID, b.ID) })
	slices.Sort(m.Blocking)
	slices.SortFunc(m.Orphans, func(a, b Orphan) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID), cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line), cmp.Compare(a.Package, b.Package), cmp.Compare(a.Test, b.Test))
	})
	slices.SortFunc(m.Warnings, func(a, b Warning) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Package, b.Package), cmp.Compare(a.Test, b.Test), cmp.Compare(a.Detail, b.Detail))
	})
	return m
}

// definition is a requirement that defines an obligation.
type definition struct {
	req    openspec.Requirement
	change string // active change of an ADDED delta; empty for a main spec
}

// definitions lists the requirements with an ID of the main specs, then those
// of the ADDED deltas of active changes, in repository order.
func definitions(repo *openspec.Repo) []definition {
	var defs []definition
	for _, s := range repo.Specs {
		for _, q := range s.Requirements {
			if !q.ID.IsZero() {
				defs = append(defs, definition{req: q})
			}
		}
	}
	for _, c := range repo.Changes {
		if c.Archived {
			continue
		}
		for _, d := range c.Deltas {
			if d.Op == openspec.Added && !d.Requirement.ID.IsZero() {
				defs = append(defs, definition{req: d.Requirement, change: c.ID})
			}
		}
	}
	return defs
}

func newRow(def definition, decls []testsource.Declaration, runtime *gotest.Report) Row {
	q := def.req
	row := Row{
		ID:               q.ID.String(),
		Kind:             q.ID.Kind().String(),
		Policy:           policyOf(q.ID.Kind()),
		Title:            q.Title,
		Path:             q.Path,
		Line:             q.Line,
		Change:           def.change,
		Characterization: q.Characterization,
		Tests:            make([]Test, 0, len(decls)),
	}
	var pkgs []string
	for _, d := range decls {
		t := Test{File: d.File, Line: d.Line, Test: d.Test, Name: d.Name, Kind: d.Kind, Skips: d.Skips}
		if runtime != nil {
			own := packagesOf(d, runtime)
			t.Runtime = declarationStatus(d, own, runtime)
			pkgs = append(pkgs, own...)
		}
		row.Tests = append(row.Tests, t)
	}
	slices.SortFunc(row.Tests, func(a, b Test) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	if runtime != nil {
		row.Runtime = runtime.Status(q.ID, pkgs...)
	}

	switch {
	case row.Policy == Block:
		row.Status = Blocking
	case len(decls) > 0:
		row.Status = Traced
	case row.Policy == RequireTest:
		row.Status = Unverified
	default:
		row.Status = Untested
	}
	return row
}

// policyOf maps k's policy; like obligation.Kind.Policy, it fails closed.
func policyOf(k obligation.Kind) Policy {
	switch k.Policy() {
	case obligation.RequireTest:
		return RequireTest
	case obligation.Warn:
		return Warn
	}
	return Block
}

// declarationStatus is the worst outcome of the tests d ran as: those of
// packages pkgs bound to d.ID whose owner is d's subtest, and the subtests
// that inherit from them. It is NotRun when none ran, or BuildFail when one
// of pkgs did not build.
func declarationStatus(d testsource.Declaration, pkgs []string, runtime *gotest.Report) evidence.Status {
	sub := gotest.Report{Packages: runtime.Packages}
	for _, t := range runtime.Tests {
		if slices.Contains(pkgs, t.Package) && slices.ContainsFunc(t.Bindings, func(b gotest.Binding) bool {
			return b.ID == d.ID && owns(d, b.Owner)
		}) {
			sub.Tests = append(sub.Tests, t)
		}
	}
	return sub.Status(d.ID, pkgs...)
}

// owns reports whether owner, the full runtime name of the test that carries
// an ID, is the subtest d declares: its last segment is d's name as go test
// rewrites it (or a "#NN" duplicate of it), below a segment named after d's
// test function or suite method.
func owns(d testsource.Declaration, owner string) bool {
	segs := strings.Split(owner, "/")
	last := segs[len(segs)-1]
	name, _, _ := strings.Cut(rewrite(d.Name), "/")
	if last != name {
		suffix, ok := strings.CutPrefix(last, name+"#")
		if _, err := strconv.Atoi(suffix); !ok || err != nil {
			return false
		}
	}
	fn := d.Test[strings.LastIndex(d.Test, ".")+1:] // (*Suite).TestX runs as …/TestX
	return slices.Contains(segs[:len(segs)-1], fn)
}

// rewrite spells a subtest name the way the testing package reports it:
// white space becomes "_" and unprintable runes are escaped.
func rewrite(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte('_')
		case !strconv.IsPrint(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// packagesOf returns the packages of runtime that can hold d: those whose
// import path ends with d's directory. Without the module path, a
// declaration at the repository root matches every package; its test and
// subtest names still have to match.
func packagesOf(d testsource.Declaration, runtime *gotest.Report) []string {
	dir := path.Dir(d.File)
	var pkgs []string
	for _, p := range runtime.Packages {
		if dir == "." || p.Name == dir || strings.HasSuffix(p.Name, "/"+dir) {
			pkgs = append(pkgs, p.Name)
		}
	}
	return pkgs
}
