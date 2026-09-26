package gotest

import (
	"regexp"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// Selection is one go test process of ADR-0005 §2.3: the tests of one
// obligation under one top-level test.
type Selection struct {
	Test string // the top-level test, as go test reports it
	// Pattern is the -run pattern that selects the obligation's tests under
	// Test, what runs below them, and nothing else.
	Pattern string
}

// Runs groups names — the full names of the tests that own an obligation in a
// run at head, as `go test -json` reports them — by their top-level test, and
// builds each group's -run pattern: one Selector alternative per name,
// repeats left out. There is one Selection per (top-level test, id), in the
// order the top-level tests first appear, and each one is a go test process
// of its own, so that neither a sibling subtest nor an earlier Test can
// change the obligation's outcome (ADR-0005 §2.3 and §3).
//
// Every alternative is a whole path, prefix repeated, and that is not
// redundancy: go test reads a top-level "|" as an alternation of independent
// patterns and splits each one on "/" by itself. So alternatives of different
// depths are fine — ^TestX$/^ORD-F01([_#]|$)|^TestX$/^Sub$/^ORD-F01([_#]|$)
// selects both — and factoring the shared prefix out would break them: in
// ^TestX$/^a$|^b$ the second alternative is one level deep and gets matched
// against the top-level test's own name, so the test named b is never
// selected at all, and an obligation nothing selects has no measured status.
//
// None of that is documented. `go help testflag` describes only the split on
// "/", and the code that does the rest — splitRegexp and alternationMatch in
// $GOROOT/src/testing/match.go, which also leaves a "|" inside "()" or "[]"
// literal, as ([_#]|$) needs — is unexported and promises nothing. So it is
// not a contract to read but behavior to check: TestRunsSelectFixtureTests
// runs both halves of it against a real go test, which is where a toolchain
// that changes its mind will show up.
func Runs(id obligation.ID, names []string) []Selection {
	var runs []Selection
	byTop := make(map[string][]string, len(names))
	for _, name := range names {
		top, _, _ := strings.Cut(name, "/")
		if _, ok := byTop[top]; !ok {
			runs = append(runs, Selection{Test: top})
		}
		byTop[top] = append(byTop[top], name)
	}
	for i, r := range runs {
		var alts []string
		for _, name := range byTop[r.Test] {
			if alt := Selector(id, name); !slices.Contains(alts, alt) {
				alts = append(alts, alt)
			}
		}
		runs[i].Pattern = strings.Join(alts, "|")
	}
	return runs
}

// Selector returns the -run pattern that selects the test fullName names, cut
// at the outermost level that carries id: every level regexp-escaped and
// anchored, and the level that carries id matched with the "_title" and "#NN"
// suffixes go test adds, so that a duplicate runs too and a longer ID does
// not: ^TestSuite$/^TestX$/^ORD-F01([_#]|$) selects ORD-F01, ORD-F01_x and
// ORD-F01#01, never ORD-F010. fullName is a full test name as
// `go test -json` reports it; a test that owns id only through an aval.req
// attr carries it in no level and is selected by its exact name.
//
// Use Selector and Runs for the tests of an obligation in a run whose names
// are known, which is every caller of ADR-0005 §2.3; RunPattern is for a
// top-level test named on its own, with no run to take the subtests' names
// from.
func Selector(id obligation.ID, fullName string) string {
	var levels []string
	for level := range strings.SplitSeq(fullName, "/") {
		if got, ok := obligation.FromTestSegment(level); ok && got == id {
			return strings.Join(append(levels, "^"+regexp.QuoteMeta(id.String())+"([_#]|$)"), "/")
		}
		levels = append(levels, "^"+regexp.QuoteMeta(level)+"$")
	}
	return strings.Join(levels, "/")
}
