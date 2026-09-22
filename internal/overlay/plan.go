package overlay

import (
	"fmt"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// plannedTarget is a validated target and its runs.
type plannedTarget struct {
	Target
	runs []plannedRun // one per top-level test of Tests, in order of first appearance
}

type plannedRun struct {
	test    string // the top-level test
	pattern string // the -run pattern that selects the target's tests under it
}

func newPlan(targets []Target) ([]plannedTarget, error) {
	plan := make([]plannedTarget, 0, len(targets))
	seen := make(map[obligation.ID]bool, len(targets))
	for _, t := range targets {
		if err := t.validate(); err != nil {
			return nil, err
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("%w: %s appears twice", ErrInvalidTarget, t.ID)
		}
		seen[t.ID] = true
		var tops []string
		names := make(map[string][]string)
		for _, name := range t.Tests {
			top, _, _ := strings.Cut(name, "/")
			tops = appendNew(tops, top)
			names[top] = appendNew(names[top], name)
		}
		pt := plannedTarget{Target: t}
		for _, top := range tops {
			pt.runs = append(pt.runs, plannedRun{test: top, pattern: pattern(t.ID, names[top])})
		}
		plan = append(plan, pt)
	}
	return plan, nil
}

func (t Target) validate() error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalidTarget, t.ID, fmt.Sprintf(format, args...))
	}
	switch {
	case t.ID.IsZero():
		return fmt.Errorf("%w: no ID", ErrInvalidTarget)
	case len(t.Tests) == 0:
		return invalid("no tests")
	case len(t.Packages) == 0:
		return invalid("no packages")
	}
	for _, name := range t.Tests {
		levels := strings.Split(name, "/")
		if !token.IsIdentifier(levels[0]) || slices.Contains(levels, "") {
			return invalid("%q is not a test name as go test reports it", name)
		}
	}
	for _, pkg := range t.Packages {
		if pkg == "" || strings.HasPrefix(pkg, "-") {
			return invalid("%q is not an import path", pkg)
		}
	}
	return nil
}

// pattern returns the -run pattern that selects the tests named, which own
// id, what runs under them, and nothing else: one alternative per name,
// each level quoted and anchored. The level that carries id matches it with
// any "_..." or "#NN" suffix, so duplicates run too:
// ^TestSuite$/^TestX$/^ORD-F01([_#]|$). A test bound only by an aval.req
// attr is selected by its exact name.
func pattern(id obligation.ID, names []string) string {
	var alts []string
	for _, name := range names {
		alts = appendNew(alts, selector(id, name))
	}
	return strings.Join(alts, "|")
}

// selector selects name down to its outermost level that carries id.
func selector(id obligation.ID, name string) string {
	var levels []string
	for level := range strings.SplitSeq(name, "/") {
		if got, ok := obligation.FromTestSegment(level); ok && got == id {
			return strings.Join(append(levels, "^"+regexp.QuoteMeta(id.String())+"([_#]|$)"), "/")
		}
		levels = append(levels, "^"+regexp.QuoteMeta(level)+"$")
	}
	return strings.Join(levels, "/")
}

// appendNew appends the values of vs that s does not hold yet.
func appendNew[T comparable](s []T, vs ...T) []T {
	for _, v := range vs {
		if !slices.Contains(s, v) {
			s = append(s, v)
		}
	}
	return s
}
