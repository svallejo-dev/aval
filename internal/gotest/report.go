// Package gotest runs `go test -json` and turns its test2json stream into
// per-test outcomes bound to obligation IDs (ADR-0002).
//
// A test is bound to the ID that starts the outermost segment of its name
// carrying one ("TestOrder/ORD-F01_rejects_duplicates"), and to every ID set
// with t.Attr("aval.req", ...). Subtests inherit their ancestors' bindings,
// but only the test that carries an ID, its owner, is evidence for it: the
// subtests of "ORD-F05 lists skus" are not extra ORD-F05 tests.
package gotest

import (
	"slices"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// Report is what one `go test -json` stream says about packages and tests.
type Report struct {
	Packages []Package     // in order of first appearance
	Tests    []TestOutcome // grouped by package, each in order of first appearance
	Warnings []Warning
	// Other holds what belongs to no package: lines that are not test2json
	// events and build output no package claimed.
	Other string
	// ExitCode is go test's exit status when the report comes from Run.
	ExitCode int
}

// Package is the outcome of one package.
type Package struct {
	Name string // as in the Package field: an import path, or a pattern that failed setup
	// Status is Pass, Fail, BuildFail, Skipped (no test files), or NotRun
	// when the stream ended before the package did.
	Status evidence.Status
	// FailedBuild names what did not build: the package, a dependency, or a
	// pattern that matched nothing ("[setup failed]").
	FailedBuild  string
	NoTestFiles  bool
	NoTestsToRun bool // -run selected nothing; the package still passes
	// FailedOutsideTests reports a package that failed although every test
	// that started passed or was skipped: goleak.VerifyTestMain, a TestMain
	// that exits non-zero, an invalid -run pattern. No obligation is blamed.
	FailedOutsideTests bool
	Output             string // package-level output, compiler errors included
	Truncated          bool   // Output was cut, see MaxOutput
}

// TestOutcome is the outcome of one test or subtest.
type TestOutcome struct {
	Package string
	Name    string // full name as reported, e.g. "TestOrder/ORD-F01_rejects_duplicates"
	// Status is Pass, Fail, Skipped, or NotRun for a test that started and
	// never ended: killed by the -timeout alarm, by a panic elsewhere, or by
	// a truncated stream. Tests that never started have no TestOutcome. With
	// -count > 1 it is the worst of all runs.
	Status   evidence.Status
	Bindings []Binding
	// Output is what the test printed, frame lines (=== RUN, --- FAIL, ...)
	// excepted, unless it passed: a test's output is dropped once every run
	// of it ended in pass, so a long run keeps only what explains failures,
	// skips and hangs.
	Output    string
	Truncated bool // Output was cut, see MaxOutput
}

// Binding ties a test to an obligation.
type Binding struct {
	ID obligation.ID
	// Owner is the full name of the outermost test that carries ID in its
	// name or in an aval.req attr. It is the test itself unless the binding
	// is inherited.
	Owner string
}

// WarningKind classifies a Warning.
type WarningKind string

// Things that look like bindings but bind nothing.
const (
	// SuspiciousSegment is a segment that starts with an ID followed by
	// something other than "_", "#NN" or its end, such as "ORD-F04:_x" from
	// t.Run("ORD-F04: x").
	SuspiciousSegment WarningKind = "suspicious_segment"
	// NestedID is an ID in a segment below one that already binds another:
	// the outermost wins.
	NestedID WarningKind = "nested_id"
	// InvalidAttr is an aval.req attr whose value is not an obligation ID.
	InvalidAttr WarningKind = "invalid_attr"
)

// Warning reports a test that silently fails to bind an ID it seems to name.
type Warning struct {
	Kind    WarningKind
	Package string
	Test    string // the first test it was seen on
	Detail  string // the segment or attr value at fault
}

// Obligations maps each bound ID to the tests that own it, in report order.
// Subtests that only inherit a binding are left out.
func (r Report) Obligations() map[obligation.ID][]TestOutcome {
	m := make(map[obligation.ID][]TestOutcome)
	for _, t := range r.Tests {
		for _, b := range t.Bindings {
			if b.Owner == t.Name {
				m[b.ID] = append(m[b.ID], t)
			}
		}
	}
	return m
}

// Status is the outcome of obligation id: the worst Status among every test
// bound to it, owners and inheriting subtests alike, ranked by
// evidence.Worse. So a skipped table case marks the obligation skipped even
// though go test passes its parent.
//
// With no bound test the ID did not run: selecting a missing ID passes with
// "no tests to run", and that is NotRun, never Pass. pkgs names the packages
// the ID's tests live in, as another run knows them (the gate passes the
// owners' packages at head when it asks about the base): if any of them
// failed to build here, the status is BuildFail. A package that failed to
// build elsewhere in the report never makes it BuildFail.
//
// Package-level failures do not change the status; see
// Package.FailedOutsideTests.
func (r Report) Status(id obligation.ID, pkgs ...string) evidence.Status {
	var status evidence.Status
	for _, t := range r.Tests {
		if !slices.ContainsFunc(t.Bindings, func(b Binding) bool { return b.ID == id }) {
			continue
		}
		if status == "" { // the first bound test sets it, the rest can only worsen it
			status = t.Status
			continue
		}
		status = evidence.Worse(status, t.Status)
	}
	if status != "" {
		return status
	}
	for _, p := range r.Packages {
		if p.Status == evidence.BuildFail && slices.Contains(pkgs, p.Name) {
			return evidence.BuildFail
		}
	}
	return evidence.NotRun
}
