package testsource

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// Compare reports the tampering signals between the declarations of the base
// and head commits for every ID not in changed, the IDs of the change's ADDED
// and MODIFIED deltas, whose tests are expected to change:
//
//   - skip_added: a bound test skips in head and did not in base;
//   - fingerprint_changed: a bound test or something it depends on changed,
//     or the ID gained a declaration, in the same file or another one;
//   - test_removed: a declaration of the ID has no counterpart in head.
//
// Declarations of an ID are paired one by one, and only within the same
// directory and top-level test function: moving a test to another package or
// function is a removal plus a new declaration. Within those, pairing goes
// first by fingerprint, so moving a test inside its package is not a change,
// then by file, then in order. A copy of an ID declared elsewhere therefore
// never hides the removal, edit or skip of another copy. IDs first declared
// in head bound no test before and are ignored. The result is sorted by ID and
// never nil.
func Compare(base, head []Declaration, changed map[obligation.ID]bool) []evidence.Finding {
	bases, heads := byID(base), byID(head)
	ids := slices.SortedFunc(maps.Keys(bases), func(a, b obligation.ID) int {
		return strings.Compare(a.String(), b.String())
	})
	out := []evidence.Finding{}
	for _, id := range ids {
		if !changed[id] {
			out = append(out, compareID(id.String(), bases[id], heads[id])...)
		}
	}
	return out
}

// pairings are tried in order; each pairs what the previous ones left.
var pairings = []func(b, h Declaration) bool{
	func(b, h Declaration) bool { return b.Fingerprint == h.Fingerprint },
	func(b, h Declaration) bool { return b.File == h.File },
	func(Declaration, Declaration) bool { return true },
}

func compareID(id string, base, head []Declaration) []evidence.Finding {
	pair := make([]int, len(base)) // index into head, or -1
	used := make([]bool, len(head))
	for i := range pair {
		pair[i] = -1
	}
	for _, same := range pairings {
		for i, b := range base {
			for j, h := range head {
				if pair[i] < 0 && !used[j] && b.Test == h.Test && path.Dir(b.File) == path.Dir(h.File) && same(b, h) {
					pair[i], used[j] = j, true
				}
			}
		}
	}

	var out []evidence.Finding
	add := func(kind evidence.FindingKind, format string, d Declaration) {
		detail := fmt.Sprintf(format+" (%s in %s:%d)", id, d.Test, d.File, d.Line)
		out = append(out, evidence.Finding{Kind: kind, ID: id, Detail: detail})
	}
	for i, b := range base {
		switch {
		case pair[i] < 0:
			add(evidence.TestRemoved, "test bound to %s removed outside its delta", b)
		case head[pair[i]].Skips && !b.Skips:
			add(evidence.SkipAdded, "test bound to %s skips now", head[pair[i]])
		case head[pair[i]].Fingerprint != b.Fingerprint:
			add(evidence.FingerprintChanged, "test bound to %s, or what it depends on, changed outside its delta", head[pair[i]])
		}
	}
	for j, h := range head {
		if !used[j] {
			add(evidence.FingerprintChanged, "%s declared again outside its delta", h)
		}
	}
	return out
}

func byID(decls []Declaration) map[obligation.ID][]Declaration {
	m := make(map[obligation.ID][]Declaration)
	for _, d := range decls {
		m[d.ID] = append(m[d.ID], d)
	}
	return m
}
