package verify

import (
	"fmt"
	"io/fs"
	"os"
	"path"
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
			changes = append(changes, gate.Change{ID: base.ID, BaseTier: c.baseTier(base.ID, base.Manifest)})
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
		pm, err := premortem(os.DirFS(c.ev.Root), dir, ids)
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, gate.Change{
			ID: ch.ID, Tier: tierOf(ch.Manifest),
			BaseTier: c.baseTier(ch.ID, basesByID[ch.ID].Manifest), Premortem: pm,
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

// baseTier is the tier a change already carried: the higher of what the merge
// base and the default branch give it. Both, because cutting the branch before
// the change was raised to tier 2 would otherwise lower it back, and the head
// may raise a tier, never lower it (ADR-0005 §1).
func (c *collector) baseTier(id string, atChangeBase *manifest.Change) manifest.Tier {
	t := tierOf(atChangeBase)
	if c.trustSpecs == nil {
		return t
	}
	for _, ch := range c.trustSpecs.Changes {
		if ch.ID == id {
			t = max(t, tierOf(ch.Manifest))
		}
	}
	return t
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

// premortem is what the change in dir claims could go wrong, in the shape the
// gate consumes. The markdown is openspec's to read; the shape is the gate's,
// which openspec cannot name because the gate imports openspec.
func premortem(fsys fs.FS, dir string, ids []obligation.ID) (gate.Premortem, error) {
	pm, err := openspec.ReadPremortem(fsys, dir, ids)
	if err != nil {
		return gate.Premortem{}, fmt.Errorf("verify: %w", err)
	}
	return gate.Premortem{Present: pm.Present, Items: pm.Items, Unmapped: pm.Unmapped}, nil
}

// newFindings returns the openspec.Check findings at head that BOTH bases
// already had, matched by rule, path and message with the line number left out
// (ADR-0005 §4). The path of a change this pull request archived is
// normalized to the active path it had at the merge base, in the message as well
// as in the path itself, so that moving a change does not turn its findings into
// new ones.
//
// Both bases, because either one alone can be made to excuse a finding. A
// finding only the merge base has was chosen by whoever cut the branch, who
// could cut from a commit that already carried it; a finding only the default
// branch has is somebody else's, and not yet in this range. Only a finding that
// stands at both ends was not put there by this pull request.
func (c *collector) newFindings() []openspec.Finding {
	head := c.headSpecs.Check(openspec.CheckOptions{Base: c.baseSpecs})
	if c.baseSpecs == nil || c.trustSpecs == nil {
		return head
	}
	// A count, not a set: two identical findings at head where a base had one
	// is one new finding, and the pull request answers for it. What excuses a
	// finding is the lower of the two bases' counts — nothing either base does
	// not have is excused.
	atChange := c.findingCounts(c.baseSpecs)
	atTrust := c.findingCounts(c.trustSpecs)
	was := make(map[[3]string]int, len(atChange))
	for k, n := range atChange {
		if m := atTrust[k]; m > 0 {
			was[k] = min(n, m)
		}
	}
	out := []openspec.Finding{}
	for _, f := range head {
		if k := findingKey(f, nil); was[k] > 0 {
			was[k]--
			continue
		}
		out = append(out, f)
	}
	return out
}

// findingCounts counts one base's own findings by key, with the directory of
// every change this pull request archived rewritten to the active directory that
// base has it under: each base is compared against the paths it knows, so moving
// a change turns no finding of it into a new one (ADR-0005 §4).
func (c *collector) findingCounts(specs *openspec.Repo) map[[3]string]int {
	moved := c.archivedHere(specs)
	out := make(map[[3]string]int)
	for _, f := range specs.Check(openspec.CheckOptions{}) {
		out[findingKey(f, moved)]++
	}
	return out
}

// archivedHere maps the active directory a change has in specs to the archived
// directory this pull request moved it to, so that the base's own findings are
// keyed the way head spells them.
func (c *collector) archivedHere(specs *openspec.Repo) map[string]string {
	active := make(map[string]string, len(specs.Changes))
	dirs := make(map[string]bool, len(specs.Changes))
	for _, ch := range specs.Changes {
		dirs[ch.Dir] = true
		if !ch.Archived {
			active[ch.ID] = ch.Dir
		}
	}
	moved := map[string]string{}
	for _, ch := range c.headSpecs.Changes {
		if from, ok := active[ch.ID]; ch.Archived && !dirs[ch.Dir] && ok {
			moved[from] = ch.Dir
		}
	}
	return moved
}

// findingKey identifies a finding for the base-to-head comparison: its rule,
// path and message, with the directories of moved rewritten and the line number
// left out. A base's findings are keyed with the map archivedHere built for that
// base, so they land on the paths head spells; head's own need no rewriting.
func findingKey(f openspec.Finding, moved map[string]string) [3]string {
	p, msg := f.Path, f.Message
	for from, to := range moved {
		p = strings.ReplaceAll(p, from, to)
		msg = strings.ReplaceAll(msg, from, to)
	}
	return [3]string{string(f.Rule), p, msg}
}
