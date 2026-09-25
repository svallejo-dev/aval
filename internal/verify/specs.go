package verify

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
)

// deltaOf says how the pull request's changes touched one obligation.
type deltaOf struct {
	id    obligation.ID
	delta evidence.Delta
	req   openspec.Requirement // the delta block that names it
}

// needsFailBefore reports whether d asks for fail-before evidence: an added
// or modified obligation of a kind that requires a test (ADR-0005 §2).
func needsFailBefore(d deltaOf) bool {
	return d.delta != evidence.Unchanged && d.id.Kind().Policy() == obligation.RequireTest
}

// touched returns the OpenSpec changes the range touches and, for every
// obligation their deltas name, how they touched it. ADDED and MODIFIED ask
// for fail-before; REMOVED only asks not to regress, so it counts as
// unchanged, and RENAMED is not part of the delta at all: it keeps the ID, so
// the test need not change (ADR-0005 §1).
func (c *collector) touched() ([]gate.Change, map[obligation.ID]deltaOf, error) {
	heads, headIDs := map[string]openspec.Change{}, map[string]bool{}
	for _, ch := range c.headSpecs.Changes {
		heads[ch.Dir], headIDs[ch.ID] = ch, true
	}
	basesByDir, basesByID := map[string]openspec.Change{}, map[string]openspec.Change{}
	if c.baseSpecs != nil {
		for _, ch := range c.baseSpecs.Changes {
			basesByDir[ch.Dir], basesByID[ch.ID] = ch, ch
		}
	}

	changes := []gate.Change{}
	seen := map[string]bool{}
	deltas := map[obligation.ID]deltaOf{}
	for _, dir := range changeDirs(c.changed) {
		ch, atHead := heads[dir]
		if !atHead {
			// The pull request deleted the change, or archived it, in which
			// case its new directory carries it. A deletion still keeps the
			// tier the base gave it: head may raise the tier, never lower it
			// (ADR-0005 §1), and with it the premortem the tier asks for.
			base, atBase := basesByDir[dir]
			if !atBase || headIDs[base.ID] || seen[base.ID] {
				continue
			}
			seen[base.ID] = true
			changes = append(changes, gate.Change{ID: base.ID, BaseTier: tierOf(base.Manifest)})
			continue
		}
		if seen[ch.ID] {
			continue
		}
		seen[ch.ID] = true
		var ids []obligation.ID
		for _, d := range ch.Deltas {
			for _, q := range []openspec.Requirement{d.Requirement, d.From, d.To} {
				if !q.ID.IsZero() {
					ids = append(ids, q.ID)
				}
			}
			id, delta := d.Requirement.ID, deltaOp(d.Op)
			if id.IsZero() || d.Op == openspec.Renamed {
				continue
			}
			// An ADDED or MODIFIED of an ID wins over a REMOVED of it
			// elsewhere: the stronger claim is the one that needs evidence.
			if prev, ok := deltas[id]; ok && prev.delta != evidence.Unchanged && delta == evidence.Unchanged {
				continue
			}
			deltas[id] = deltaOf{id: id, delta: delta, req: d.Requirement}
		}
		pm, err := premortem(c.ev.Root, dir, ids)
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, gate.Change{
			ID: ch.ID, Tier: tierOf(ch.Manifest), BaseTier: tierOf(basesByID[ch.ID].Manifest), Premortem: pm,
		})
	}
	return changes, deltas, nil
}

// deltaOp maps an OpenSpec operation to the bundle's delta.
func deltaOp(op openspec.Op) evidence.Delta {
	switch op {
	case openspec.Added:
		return evidence.Added
	case openspec.Modified:
		return evidence.Modified
	}
	return evidence.Unchanged
}

// tierOf is the tier of a change manifest, 0 when there is none: a change
// without an aval.yaml claims nothing, and the policy's tierDefault still
// applies.
func tierOf(m *manifest.Change) manifest.Tier {
	if m == nil {
		return 0
	}
	return m.Tier
}

// changeDirs returns the change directories the paths touch, sorted:
// openspec/changes/<id>/ and openspec/changes/archive/<date>-<id>/
// (ADR-0005 §1).
func changeDirs(paths []string) []string {
	prefix := openspec.Dir + "/changes/"
	var dirs []string
	for _, p := range paths {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		dir, _, ok := strings.Cut(rest, "/")
		if ok && dir == "archive" {
			dir, _, ok = strings.Cut(rest[len("archive/"):], "/")
			dir = path.Join("archive", dir)
		}
		if ok { // a file directly in changes/ belongs to no change
			dirs = append(dirs, prefix+dir)
		}
	}
	slices.Sort(dirs)
	return slices.Compact(dirs)
}

// itemStart matches the marker of a top-level markdown list item and fence
// the start or end of a fenced code block, indented by up to three spaces as
// CommonMark allows.
var (
	itemStart = regexp.MustCompile(`^([-*+]|[0-9]{1,9}[.)])[ \t]`)
	fence     = regexp.MustCompile("^ {0,3}(```|~~~)")
)

// premortem reads dir/premortem.md under root: whether it exists, how many
// top-level list items it has outside fenced code blocks, and which of them
// cite no obligation ID of their own change (ADR-0005 §4).
func premortem(root, dir string, ids []obligation.ID) (gate.Premortem, error) {
	name := filepath.Join(root, filepath.FromSlash(dir), "premortem.md")
	data, err := os.ReadFile(name) //nolint:gosec // a fixed name under the repository root
	if errors.Is(err, fs.ErrNotExist) {
		return gate.Premortem{}, nil
	}
	if err != nil {
		return gate.Premortem{}, fmt.Errorf("verify: %w", err)
	}
	pm := gate.Premortem{Present: true}
	for _, item := range listItems(string(data)) {
		pm.Items++
		if !slices.ContainsFunc(ids, func(id obligation.ID) bool { return strings.Contains(item, id.String()) }) {
			first, _, _ := strings.Cut(item, "\n")
			pm.Unmapped = append(pm.Unmapped, strings.TrimSpace(first))
		}
	}
	return pm, nil
}

// listItems returns the top-level list items of md: the item's own line plus
// the indented lines that continue it, and nothing inside a fenced code
// block. A table row is not an item, and neither is a nested item.
func listItems(md string) []string {
	var items []string
	fenced := false
	for line := range strings.Lines(md) {
		switch {
		case fence.MatchString(line):
			fenced = !fenced
		case fenced:
		case itemStart.MatchString(line):
			items = append(items, line)
		case len(items) > 0 && strings.TrimSpace(line) != "" && (line[0] == ' ' || line[0] == '\t'):
			items[len(items)-1] += line
		}
	}
	return items
}

// newFindings returns the openspec.Check findings at head that the base did
// not have, matched by rule, path and message with the line number left out
// (ADR-0005 §4). The path of a change this pull request archived is
// normalized to the active path it had at the base, in the message as well as
// in the path itself, so that moving a change does not turn its findings into
// new ones.
func (c *collector) newFindings() []openspec.Finding {
	head := c.headSpecs.Check(openspec.CheckOptions{Base: c.baseSpecs})
	if c.baseSpecs == nil {
		return head
	}
	moved := c.archivedHere()
	// A count, not a set: two identical findings at head where the base had
	// one is one new finding, and the pull request answers for it.
	was := make(map[[3]string]int)
	for _, f := range c.baseSpecs.Check(openspec.CheckOptions{}) {
		was[findingKey(f, nil)]++
	}
	out := []openspec.Finding{}
	for _, f := range head {
		if k := findingKey(f, moved); was[k] > 0 {
			was[k]--
			continue
		}
		out = append(out, f)
	}
	return out
}

// archivedHere maps the directory of every change this pull request archived
// to the active directory it has at the base.
func (c *collector) archivedHere() map[string]string {
	atBase := make(map[string]string, len(c.baseSpecs.Changes))
	dirs := make(map[string]bool, len(c.baseSpecs.Changes))
	for _, ch := range c.baseSpecs.Changes {
		dirs[ch.Dir] = true
		if !ch.Archived {
			atBase[ch.ID] = ch.Dir
		}
	}
	moved := map[string]string{}
	for _, ch := range c.headSpecs.Changes {
		if base, ok := atBase[ch.ID]; ch.Archived && !dirs[ch.Dir] && ok {
			moved[ch.Dir] = base
		}
	}
	return moved
}

// findingKey identifies a finding for the base-to-head comparison: its rule,
// path and message, with the directories of moved rewritten to the paths the
// base knows and the line number left out.
func findingKey(f openspec.Finding, moved map[string]string) [3]string {
	p, msg := f.Path, f.Message
	for from, to := range moved {
		p = strings.ReplaceAll(p, from, to)
		msg = strings.ReplaceAll(msg, from, to)
	}
	return [3]string{string(f.Rule), p, msg}
}
