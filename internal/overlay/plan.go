package overlay

import (
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// plannedTarget is a validated target and its runs: one go test process per
// top-level test of Tests, in order of first appearance, selecting the
// target's tests under it and nothing else (gotest.Runs, ADR-0005 §2.3).
type plannedTarget struct {
	Target
	runs []gotest.Selection
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
		plan = append(plan, plannedTarget{Target: t, runs: gotest.Runs(t.ID, t.Tests)})
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
