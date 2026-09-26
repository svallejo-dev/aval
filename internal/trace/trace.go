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

// Runtime is what running the tests of the repository produced.
type Runtime struct {
	Report gotest.Report
	// Module is the module path of the repository root's go.mod. A
	// declaration in directory dir only matches tests of package
	// Module/dir, or Module itself at the root.
	Module string
}

// Matrix is the traceability matrix. Every list is sorted and never nil.
type Matrix struct {
	Rows     []Row     `json:"rows"`     // one per obligation, by ID
	Orphans  []Orphan  `json:"orphans"`  // by kind, ID and place
	Warnings []Warning `json:"warnings"` // test names that look bound and are not
	Blocking []string  `json:"blocking"` // IDs of the open questions
	// BuildFailures are the packages, or the patterns, that did not build or
	// set up when the tests ran: their tests never ran.
	BuildFailures []BuildFailure `json:"buildFailures"`
	RanTests      bool           `json:"ranTests"` // the runtime fields are set
}

// BuildFailure is a package of the test run that did not build.
type BuildFailure struct {
	Package     string `json:"package"`     // import path, or a pattern such as "./..."
	FailedBuild string `json:"failedBuild"` // what did not build: it, a dependency, or the pattern
	Output      string `json:"output"`      // the compiler's or the go command's explanation
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
	// Runtime is the worst outcome of the ID at run time: of each
	// declaration and of any other test bound to the ID, ranked by
	// evidence.Worse. Empty when the tests did not run.
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
// testsource.Scan of the repository root. rt is what running the tests
// produced, or nil when they did not run.
//
// The obligations are the requirements with a valid ID of the main specs and
// of the ADDED deltas of active changes. A duplicate ID gets the row of its
// first definition; openspec.Repo.Check reports the others.
func Build(repo *openspec.Repo, decls []testsource.Declaration, rt *Runtime) Matrix {
	m := Matrix{Rows: []Row{}, Orphans: []Orphan{}, Warnings: []Warning{}, Blocking: []string{}, BuildFailures: []BuildFailure{}, RanTests: rt != nil}

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
		row := newRow(def, declared[def.req.ID], rt)
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
	if rt != nil {
		for id, owners := range rt.Report.Obligations() {
			for _, t := range owners {
				if !slices.ContainsFunc(declared[id], func(d testsource.Declaration) bool { return rt.declares(d, t.Package, t.Name) }) {
					m.Orphans = append(m.Orphans, Orphan{Kind: OrphanRuntime, ID: id.String(), Package: t.Package, Test: t.Name})
				}
			}
		}
		for _, w := range rt.Report.Warnings {
			m.Warnings = append(m.Warnings, Warning(w))
		}
		for _, p := range rt.Report.Packages {
			if p.Status == evidence.BuildFail {
				m.BuildFailures = append(m.BuildFailures, BuildFailure{Package: p.Name, FailedBuild: p.FailedBuild, Output: p.Output})
			}
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
	slices.SortFunc(m.BuildFailures, func(a, b BuildFailure) int { return cmp.Compare(a.Package, b.Package) })
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

func newRow(def definition, decls []testsource.Declaration, rt *Runtime) Row {
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
	if rt != nil {
		row.Runtime = rt.Report.Status(q.ID) // the ID's tests, declared or not; not_run without any
	}
	for _, d := range decls {
		t := Test{File: d.File, Line: d.Line, Test: d.Test, Name: d.Name, Kind: d.Kind, Skips: d.Skips}
		if rt != nil {
			t.Runtime = rt.status(d)
			row.Runtime = evidence.Worse(row.Runtime, t.Runtime)
		}
		row.Tests = append(row.Tests, t)
	}
	slices.SortFunc(row.Tests, func(a, b Test) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})

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

// status is the worst outcome of the tests d ran as: those of its package
// whose owner d declares, with the subtests that inherit from them. It is
// build_fail when its package did not build, and not_run when none ran.
func (rt *Runtime) status(d testsource.Declaration) evidence.Status {
	pkg := rt.importPath(d)
	sub := gotest.Report{Packages: rt.Report.Packages}
	for _, t := range rt.Report.Tests {
		if slices.ContainsFunc(t.Bindings, func(b gotest.Binding) bool {
			return b.ID == d.ID && rt.declares(d, t.Package, b.Owner)
		}) {
			sub.Tests = append(sub.Tests, t)
		}
	}
	return sub.Status(d.ID, pkg)
}

// importPath is the import path of the package of d's file.
func (rt *Runtime) importPath(d testsource.Declaration) string {
	if dir := path.Dir(d.File); dir != "." {
		return rt.Module + "/" + dir
	}
	return rt.Module
}

// declares reports whether d declares owner, the full name of a test of
// package pkg that carries d's ID: pkg is d's package, and owner ends with
// "/" and d's name as the testing package spells it, or with that and a
// "#NN" duplicate suffix. When d sits in a Test function or a suite's Test
// method, owner must run under it; a declaration in a helper runs under
// whichever test calls it.
func (rt *Runtime) declares(d testsource.Declaration, pkg, owner string) bool {
	if rt.Module == "" || pkg != rt.importPath(d) {
		return false
	}
	name := "/" + rewrite(d.Name)
	parent, ok := strings.CutSuffix(owner, name)
	if !ok {
		i := strings.LastIndexByte(owner, '#')
		if _, err := strconv.Atoi(owner[i+1:]); i < 0 || err != nil {
			return false
		}
		if parent, ok = strings.CutSuffix(owner[:i], name); !ok {
			return false
		}
	}
	fn := d.Test[strings.LastIndex(d.Test, ".")+1:] // (*Suite).TestX runs as …/TestX
	return !strings.HasPrefix(fn, "Test") || slices.Contains(strings.Split(parent, "/"), fn)
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
