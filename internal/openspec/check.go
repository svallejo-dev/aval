package openspec

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
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
	RuleRetiredID       Rule = "retired-id"        // error: ADDED reuses an ID an archived change removed
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
	Line     int    // 1-based
	Message  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s [%s]", f.Path, f.Line, f.Severity, f.Message, f.Rule)
}

func (q Requirement) finding(s Severity, rule Rule, msg string) Finding {
	return Finding{Severity: s, Rule: rule, Path: q.Path, Line: q.Line, Message: msg}
}

// Check applies the ADR-0002 rules to the main specs and the active changes
// and returns the findings sorted by path and line. Archived changes are
// history: they only contribute the IDs their REMOVED blocks retired.
func (r *Repo) Check() []Finding {
	c := checker{
		main:    make(map[string]map[string]Requirement),
		defined: make(map[string]Requirement),
		retired: make(map[string]string),
		out:     slices.Clone(r.findings),
	}
	for _, ch := range r.Changes {
		for _, d := range ch.Deltas {
			if id := d.Requirement.ID.String(); ch.Archived && d.Op == Removed && id != "" && c.retired[id] == "" {
				c.retired[id] = ch.Dir
			}
		}
	}
	for _, s := range r.Specs {
		names := make(map[string]Requirement, len(s.Requirements))
		for _, q := range s.Requirements {
			if _, dup := names[q.Name]; !dup {
				names[q.Name] = q
			}
			c.define(q)
		}
		c.main[s.Capability] = names
	}
	for _, ch := range r.Changes {
		if ch.Archived {
			continue
		}
		for _, d := range ch.Deltas {
			if d.Op != Added {
				continue
			}
			c.define(d.Requirement)
			if dir, ok := c.retired[d.Requirement.ID.String()]; ok {
				c.add(d.Requirement, SeverityError, RuleRetiredID, "ADDED %q reuses %s, which %s retired; IDs are never reused",
					d.Requirement.Name, d.Requirement.ID, dir)
			}
		}
	}
	for _, ch := range r.Changes {
		if !ch.Archived {
			c.references(ch)
		}
	}
	slices.SortStableFunc(c.out, func(a, b Finding) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
	return c.out
}

type checker struct {
	main    map[string]map[string]Requirement // capability → name → main spec requirement
	defined map[string]Requirement            // ID → its first definition
	retired map[string]string                 // ID → archived change that removed it
	out     []Finding
}

func (c *checker) add(q Requirement, s Severity, rule Rule, format string, args ...any) {
	c.out = append(c.out, q.finding(s, rule, fmt.Sprintf(format, args...)))
}

// define records q as a definition of its ID; a second one is an error.
func (c *checker) define(q Requirement) {
	if q.ID.IsZero() {
		return
	}
	if first, ok := c.defined[q.ID.String()]; ok {
		c.add(q, SeverityError, RuleDuplicateID, "%s is already defined at %s:%d as %q", q.ID, first.Path, first.Line, first.Name)
		return
	}
	c.defined[q.ID.String()] = q
}

// references checks what the deltas of an active change refer to.
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
