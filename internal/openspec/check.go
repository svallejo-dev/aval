package openspec

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// Severity says whether a finding blocks.
type Severity string

// Severities.
const (
	SeverityError Severity = "error" // breaks an ADR-0002 rule
	SeverityWarn  Severity = "warn"  // must be visible, never blocks
)

// Rule names the check behind a finding. The values are stable: tools and
// agents may branch on them.
type Rule string

// Rules of ADR-0002. The comment gives each rule's severity.
const (
	RuleLooseHeader     Rule = "loose-header"      // error: header other than "### Requirement: "
	RuleTrailingHash    Rule = "trailing-hash"     // error: " #" run closing the name
	RuleBacktick        Rule = "backtick"          // error: backtick in the name
	RuleInvalidID       Rule = "invalid-id"        // error: name without a leading obligation ID
	RuleNameLength      Rule = "name-length"       // warn: name of 50 characters or more
	RuleStrayHeading    Rule = "stray-heading"     // error: other "###" heading in a requirement section
	RuleUnpairedRename  Rule = "unpaired-rename"   // error: RENAMED FROM: or TO: without its pair
	RuleDuplicateID     Rule = "duplicate-id"      // error: ID defined twice
	RuleRetiredID       Rule = "retired-id"        // error: definition of an ID an archived change removed or renamed away
	RuleUnknownID       Rule = "unknown-id"        // error: MODIFIED, REMOVED or RENAMED of an undefined ID
	RuleUnmatchedName   Rule = "unmatched-name"    // error: reference that matches no requirement name exactly
	RuleRenamedID       Rule = "renamed-id"        // error: RENAMED that changes the ID
	RuleModifiedOldName Rule = "modified-old-name" // error: MODIFIED under the name RENAMED replaces
	RuleDroppedScenario Rule = "dropped-scenario"  // error: MODIFIED without a current scenario
	RuleDroppedMarker   Rule = "dropped-marker"    // warn: MODIFIED without the characterization marker
)

// Finding is a rule violation at a place in the OpenSpec tree.
type Finding struct {
	Severity Severity
	Rule     Rule
	Path     string // slash-separated path from the repository root
	Line     int    // 1-based; 0 when no line applies
	Message  string
}

func (f Finding) String() string {
	where := f.Path
	if f.Line > 0 {
		where = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return fmt.Sprintf("%s: %s: %s [%s]", where, f.Severity, f.Message, f.Rule)
}

func (q Requirement) finding(s Severity, rule Rule, msg string) Finding {
	return Finding{Severity: s, Rule: rule, Path: q.Path, Line: q.Line, Message: msg}
}

// CheckOptions is what Check needs besides the tree itself.
type CheckOptions struct {
	// Base is the tree at the pull request's base commit, loaded from a
	// checkout or a git tree of that commit. A change archived in the tree
	// but not in Base was archived by the pull request itself, together with
	// its code (ADR-0002): Check applies every delta rule to it against
	// Base's main specs, which are the specs as they were before the archive.
	// Rebuilding them from the tree instead would be wrong: a MODIFIED block
	// replaces the old requirement, so its scenarios and marker are gone.
	// With a nil Base every archived change is history.
	Base *Repo
}

// Check applies the ADR-0002 rules and returns the findings sorted by path
// and line. The main specs and the active changes are always checked;
// archived changes are history, which only retires IDs, unless opts.Base
// shows that the pull request archived them.
func (r *Repo) Check(opts CheckOptions) []Finding {
	var active, history, archivedHere []Change
	inBase := make(map[string]bool)
	if opts.Base != nil {
		for _, ch := range opts.Base.Changes {
			inBase[ch.Dir] = ch.Archived
		}
	}
	for _, ch := range r.Changes {
		switch {
		case !ch.Archived:
			active = append(active, ch)
		case opts.Base == nil || inBase[ch.Dir]:
			history = append(history, ch)
		default:
			archivedHere = append(archivedHere, ch)
		}
	}

	c := newChecker(retirements(slices.Concat(history, archivedHere)))
	c.out = slices.Clone(r.findings)
	c.specs(r.Specs, true)
	c.changes(active)
	out := c.out
	if len(archivedHere) > 0 {
		b := newChecker(retirements(history))
		b.specs(opts.Base.Specs, false)
		b.changes(archivedHere)
		out = append(out, b.out...)
	}
	slices.SortStableFunc(out, func(a, b Finding) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
	return out
}

// retirements maps each ID the archived changes retired to the change that
// retired it: REMOVED blocks, and RENAMED pairs that changed the ID.
func retirements(archived []Change) map[string]string {
	retired := make(map[string]string)
	retire := func(id obligation.ID, dir string) {
		if !id.IsZero() && retired[id.String()] == "" {
			retired[id.String()] = dir
		}
	}
	for _, ch := range archived {
		for _, d := range ch.Deltas {
			switch {
			case d.Op == Removed:
				retire(d.Requirement.ID, ch.Dir)
			case d.Op == Renamed && !d.To.ID.IsZero() && d.From.ID != d.To.ID:
				retire(d.From.ID, ch.Dir)
			}
		}
	}
	return retired
}

type checker struct {
	main    map[string]map[string]Requirement // capability → name → main spec requirement
	defined map[string]Requirement            // ID → its first definition
	retired map[string]string                 // ID → archived change that retired it
	out     []Finding
}

func newChecker(retired map[string]string) *checker {
	return &checker{
		main:    make(map[string]map[string]Requirement),
		defined: make(map[string]Requirement),
		retired: retired,
	}
}

func (c *checker) add(q Requirement, s Severity, rule Rule, format string, args ...any) {
	c.out = append(c.out, q.finding(s, rule, fmt.Sprintf(format, args...)))
}

// specs indexes the main specs and records their definitions. report is
// false for a base tree, whose own problems are not the pull request's.
func (c *checker) specs(specs []Spec, report bool) {
	for _, s := range specs {
		names := make(map[string]Requirement, len(s.Requirements))
		for _, q := range s.Requirements {
			if _, dup := names[q.Name]; !dup {
				names[q.Name] = q
			}
			c.define(q, report)
		}
		c.main[s.Capability] = names
	}
}

// changes checks changes as if they were active: their single-file
// findings, their definitions and their references.
func (c *checker) changes(changes []Change) {
	for _, ch := range changes {
		c.out = append(c.out, ch.findings...)
		for _, d := range ch.Deltas {
			if d.Op == Added {
				c.define(d.Requirement, true)
			}
		}
	}
	for _, ch := range changes {
		c.references(ch)
	}
}

// define records q as a definition of its ID. A second definition, or one of
// a retired ID, is an error when report is set.
func (c *checker) define(q Requirement, report bool) {
	if q.ID.IsZero() {
		return
	}
	id := q.ID.String()
	if dir, ok := c.retired[id]; ok && report {
		c.add(q, SeverityError, RuleRetiredID, "%q reuses %s, which %s retired; IDs are never reused", q.Name, q.ID, dir)
	}
	if first, ok := c.defined[id]; ok {
		if report {
			c.add(q, SeverityError, RuleDuplicateID, "%s is already defined at %s:%d as %q", q.ID, first.Path, first.Line, first.Name)
		}
		return
	}
	c.defined[id] = q
}

// references checks what the deltas of a change refer to.
func (c *checker) references(ch Change) {
	renamedTo := make(map[string]map[string]string)   // capability → old name → new name
	renamedFrom := make(map[string]map[string]string) // capability → new name → old name
	for _, d := range ch.Deltas {
		if d.Op != Renamed {
			continue
		}
		if renamedTo[d.Capability] == nil {
			renamedTo[d.Capability], renamedFrom[d.Capability] = map[string]string{}, map[string]string{}
		}
		renamedTo[d.Capability][d.From.Name] = d.To.Name
		renamedFrom[d.Capability][d.To.Name] = d.From.Name
	}
	for _, d := range ch.Deltas {
		switch q := d.Requirement; d.Op {
		case Modified:
			if to, ok := renamedTo[d.Capability][q.Name]; ok {
				c.add(q, SeverityError, RuleModifiedOldName, "MODIFIED %q uses the old name of a RENAMED requirement; use the new name %q", q.Name, to)
				continue
			}
			current, ok := c.resolve("MODIFIED", d.Capability, q, renamedFrom[d.Capability])
			if !ok {
				continue
			}
			if missing := missingScenarios(current, q); len(missing) > 0 {
				c.add(q, SeverityError, RuleDroppedScenario, "MODIFIED %q omits scenario(s) the current spec still has: %s; a MODIFIED block replaces the whole requirement",
					q.Name, quoteAll(missing))
			}
			if current.Characterization && !q.Characterization {
				c.add(q, SeverityWarn, RuleDroppedMarker, "MODIFIED %q drops the \"**aval**: characterization\" marker that %s:%d carries",
					q.Name, current.Path, current.Line)
			}
		case Removed:
			c.resolve("REMOVED", d.Capability, q, nil)
		case Renamed:
			c.resolve("RENAMED FROM", d.Capability, d.From, renamedFrom[d.Capability])
			if !d.From.ID.IsZero() && !d.To.ID.IsZero() && d.From.ID != d.To.ID {
				c.add(d.To, SeverityError, RuleRenamedID, "RENAMED changes the ID from %s to %s; a rename keeps the ID, so write REMOVED %s and ADDED %s instead",
					d.From.ID, d.To.ID, d.From.ID, d.To.ID)
			}
		}
	}
}

// resolve finds the main spec requirement that q, a reference from a delta
// on capability, names. Like archive, it matches the exact name, following
// the change's renames back from their new names.
func (c *checker) resolve(what, capability string, q Requirement, renamedFrom map[string]string) (Requirement, bool) {
	if q.ID.IsZero() {
		return Requirement{}, false // already an invalid-id finding
	}
	def, ok := c.defined[q.ID.String()]
	if !ok {
		retired := ""
		if dir, ok := c.retired[q.ID.String()]; ok {
			retired = fmt.Sprintf("; %s retired it", dir)
		}
		c.add(q, SeverityError, RuleUnknownID, "%s %q refers to %s, which no main spec or active ADDED defines%s", what, q.Name, q.ID, retired)
		return Requirement{}, false
	}
	seen := make(map[string]bool)
	for name := q.Name; !seen[name]; {
		if current, ok := c.main[capability][name]; ok {
			return current, true
		}
		seen[name] = true
		if name, ok = renamedFrom[name]; !ok {
			break
		}
	}
	c.add(q, SeverityError, RuleUnmatchedName, "%s %q matches no requirement of %q exactly (names are case-sensitive), so archive would refuse it; %s is %q at %s:%d",
		what, q.Name, capability, q.ID, def.Name, def.Path, def.Line)
	return Requirement{}, false
}

// missingScenarios mirrors OpenSpec's findMissingCurrentScenarios: the
// scenario names of current that incoming lacks, counting repeated names.
func missingScenarios(current, incoming Requirement) []string {
	left := make(map[string]int, len(incoming.Scenarios))
	for _, s := range incoming.Scenarios {
		left[s.Name]++
	}
	var missing []string
	for _, s := range current.Scenarios {
		if left[s.Name] > 0 {
			left[s.Name]--
			continue
		}
		missing = append(missing, s.Name)
	}
	return missing
}

func quoteAll(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}
