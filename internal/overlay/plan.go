package overlay

import (
	"fmt"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// plan is the validated targets, grouped into one go test run per
// top-level test.
type plan struct {
	targets []plannedTarget
	runs    []plannedRun
}

type plannedTarget struct {
	Target
	tops []string // the top-level tests of Tests, deduplicated
}

type plannedRun struct {
	test   string
	owners []owner  // the tests to select, from every target with one under test
	pkgs   []string // the union of those targets' packages
}

// owner is a test that owns an obligation at head.
type owner struct {
	id   obligation.ID
	name string
}

func newPlan(targets []Target) (plan, error) {
	var p plan
	seen := make(map[obligation.ID]bool, len(targets))
	for _, t := range targets {
		if err := t.validate(); err != nil {
			return plan{}, err
		}
		if seen[t.ID] {
			return plan{}, fmt.Errorf("%w: %s appears twice", ErrInvalidTarget, t.ID)
		}
		seen[t.ID] = true
		pt := plannedTarget{Target: t}
		for _, name := range t.Tests {
			top, _, _ := strings.Cut(name, "/")
			pt.tops = appendNew(pt.tops, top)
			i := slices.IndexFunc(p.runs, func(r plannedRun) bool { return r.test == top })
			if i < 0 {
				i = len(p.runs)
				p.runs = append(p.runs, plannedRun{test: top})
			}
			p.runs[i].owners = appendNew(p.runs[i].owners, owner{t.ID, name})
			p.runs[i].pkgs = appendNew(p.runs[i].pkgs, t.Packages...)
		}
		p.targets = append(p.targets, pt)
	}
	return p, nil
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

// pattern returns the -run pattern that selects the run's owners and what
// runs under them, and nothing else: one alternative per path, each level
// quoted and anchored. The level that carries the ID matches it with any
// "_..." or "#NN" suffix, so duplicates run too, and IDs under the same
// path share it: ^TestSuite$/^TestX$/^(ORD-F01|ORD-F02)([_#]|$). An owner
// bound only by an aval.req attr is selected by its exact name.
func (r plannedRun) pattern() string {
	type path struct {
		levels string   // quoted and anchored, joined by "/"
		ids    []string // quoted IDs one level down; none selects levels exactly
	}
	var paths []path
	for _, o := range r.owners {
		levels, id := o.selector()
		i := slices.IndexFunc(paths, func(p path) bool { return p.levels == levels && (len(p.ids) > 0) == (id != "") })
		if i < 0 {
			i = len(paths)
			paths = append(paths, path{levels: levels})
		}
		if id != "" {
			paths[i].ids = appendNew(paths[i].ids, id)
		}
	}
	alts := make([]string, len(paths))
	for i, p := range paths {
		switch len(p.ids) {
		case 0:
			alts[i] = p.levels
		case 1:
			alts[i] = p.levels + "/^" + p.ids[0] + "([_#]|$)"
		default:
			alts[i] = p.levels + "/^(" + strings.Join(p.ids, "|") + ")([_#]|$)"
		}
	}
	return strings.Join(alts, "|")
}

// selector splits the owner's name at the outermost level that carries its
// ID: the levels above it, quoted and anchored, and the quoted ID. With no
// such level, the attr case, levels is the whole name and id is "".
func (o owner) selector() (levels, id string) {
	var quoted []string
	for level := range strings.SplitSeq(o.name, "/") {
		if got, ok := obligation.FromTestSegment(level); ok && got == o.id {
			return strings.Join(quoted, "/"), regexp.QuoteMeta(o.id.String())
		}
		quoted = append(quoted, "^"+regexp.QuoteMeta(level)+"$")
	}
	return strings.Join(quoted, "/"), ""
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
