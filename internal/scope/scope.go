// Package scope classifies each commit of a range into a command family by
// the paths it touches, with the dx, feat and seam globs of the base policy
// (ADR-0005 §3b).
//
// # Paths
//
// A path is seam when any seam glob matches it, whatever else matches too.
// Otherwise it takes the family of the longest dx or feat glob (counted in
// runes) that matches it, feat on a tie (the stricter family), and it is
// other when none does.
//
// A commit that touches dx and feat paths is mixed, with Families [dx feat].
// Otherwise it is dx or feat when it touches that family, seam when it
// touches seam paths only (other paths aside), and other when it touches no
// family. Seam and other paths never make a commit mixed; TouchesSeam lets
// the gate warn seam_touched.
//
// # Commits
//
// Classify lists base..head parents first (git rev-list --reverse
// --topo-order); an empty base lists every commit reachable from head,
// root included. A regular commit contributes the paths of git diff-tree
// --name-only. Renames are not detected, so a moved file counts as its old
// and its new path, and moving it across families makes a mixed commit. An
// empty commit touches nothing and is other.
//
// # Merge commits
//
// A two-parent merge contributes only what it changed beyond the merge git
// would have made on its own: the paths of git show --remerge-diff. Observed
// with git 2.50 and pinned by the tests:
//
//   - A clean merge of main into the branch lists nothing and is other, also
//     when both sides changed the same file and git merged it cleanly.
//   - An evil merge lists the files it changes itself.
//   - A conflict resolution lists the files that conflicted.
//   - A merge that keeps the branch's version of a file, dropping main's
//     change to it, lists that file.
//
// git skips --remerge-diff for an octopus merge, printing a warning instead,
// so one contributes its combined diff, git show --cc --name-only: the files
// whose content differs from every parent. That keeps an evil octopus merge
// visible, but it misses a dropped change and lists a file that several
// parents changed even if git merged it cleanly.
//
// # What the change cannot steer
//
// The files of head must not change the answer, so Classify runs git
// hardened (package git: 2.40 or newer, git.ErrToolMissing otherwise), which
// among other things means:
//
//   - --ignore-submodules=none, so a head .gitmodules with ignore = all
//     cannot hide gitlink changes;
//   - --attr-source=<empty tree>, so head .gitattributes cannot pick the
//     merge drivers --remerge-diff uses, e.g. merge=binary to hide a
//     dropped change;
//   - GIT_NO_REPLACE_OBJECTS=1 and GIT_GRAFT_FILE=/dev/null, so replace
//     refs and grafts cannot rewrite the history.
//
// The repository's own .git/config and .git/info are still trusted, and the
// change's code could rewrite them once it runs: Classify must run before
// any of it executes, such as its tests (ADR-0005 §1).
package scope

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/platform/git"
)

// ErrInvalidRange is wrapped by Classify's error for a base or head it
// refuses before running git.
var ErrInvalidRange = errors.New("invalid commit range")

// Classify classifies every commit of base..head in repoRoot, parents first,
// by the paths it touches. paths are the globs of the base policy, already
// checked by manifest validation. An empty base classifies every commit
// reachable from head. The result is never nil, and neither is any Paths.
// When git is not on PATH or is older than 2.40, the error wraps
// git.ErrToolMissing.
func Classify(ctx context.Context, repoRoot, base, head string, paths manifest.Paths) ([]evidence.Commit, error) {
	rng, err := revRange(base, head)
	if err != nil {
		return nil, err
	}
	g := git.New(repoRoot)
	if err := g.CheckVersion(ctx); err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}
	out, err := g.Run(ctx, nil, "rev-list", "--reverse", "--topo-order", "--parents", "--end-of-options", rng, "--")
	if err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}
	commits := []evidence.Commit{}
	for line := range strings.Lines(string(out)) {
		shas := strings.Fields(line) // the commit, then its parents
		if len(shas) == 0 {
			continue
		}
		files, err := touched(ctx, g, shas[0], len(shas)-1)
		if err != nil {
			return nil, err
		}
		family, families := ClassifyPaths(paths, files)
		commits = append(commits, evidence.Commit{SHA: shas[0], Family: family, Families: families, Paths: files})
	}
	return commits, nil
}

// ClassifyPaths returns the family of a commit that touches files and, for
// a mixed commit only, the families it spans. Globs follow doublestar, where
// a directory glob such as "tools" or "tools/" matches nothing under the
// directory: that takes "tools/**".
func ClassifyPaths(paths manifest.Paths, files []string) (evidence.Family, []evidence.Family) {
	var dx, feat, seam bool
	for _, f := range files {
		switch pathFamily(paths, f) {
		case evidence.FamilyDX:
			dx = true
		case evidence.FamilyFeat:
			feat = true
		case evidence.FamilySeam:
			seam = true
		}
	}
	switch {
	case dx && feat:
		return evidence.FamilyMixed, []evidence.Family{evidence.FamilyDX, evidence.FamilyFeat}
	case dx:
		return evidence.FamilyDX, nil
	case feat:
		return evidence.FamilyFeat, nil
	case seam:
		return evidence.FamilySeam, nil
	}
	return evidence.FamilyOther, nil
}

// TouchesSeam reports whether c touches a seam path of paths. The gate warns
// seam_touched for a feat commit that does (ADR-0005 §4).
func TouchesSeam(paths manifest.Paths, c evidence.Commit) bool {
	return slices.ContainsFunc(c.Paths, func(f string) bool { return isSeam(paths, f) })
}

// pathFamily returns the family of one path: seam first, then the family
// of the longest matching dx or feat glob, feat on a tie; other if none.
func pathFamily(paths manifest.Paths, file string) evidence.Family {
	if isSeam(paths, file) {
		return evidence.FamilySeam
	}
	best, bestLen := evidence.FamilyOther, -1
	// feat goes first: a dx glob must be strictly longer to win.
	for _, fam := range []struct {
		family evidence.Family
		globs  []string
	}{{evidence.FamilyFeat, paths.Feat}, {evidence.FamilyDX, paths.DX}} {
		for _, g := range fam.globs {
			if n := utf8.RuneCountInString(g); n > bestLen && doublestar.MatchUnvalidated(g, file) {
				best, bestLen = fam.family, n
			}
		}
	}
	return best
}

func isSeam(paths manifest.Paths, file string) bool {
	return slices.ContainsFunc(paths.Seam, func(g string) bool { return doublestar.MatchUnvalidated(g, file) })
}

// revRange returns the rev-list argument for base..head. A revision that
// starts with "-" is refused: git would read it as an option.
func revRange(base, head string) (string, error) {
	switch {
	case head == "":
		return "", fmt.Errorf("%w: empty head", ErrInvalidRange)
	case strings.HasPrefix(base, "-"), strings.HasPrefix(head, "-"):
		return "", fmt.Errorf("%w: %q..%q: a revision may not start with -", ErrInvalidRange, base, head)
	case base == "":
		return head, nil
	}
	return base + ".." + head, nil
}

// touched lists the paths commit sha, with the given number of parents,
// touches, as git prints them (sorted). See the package doc for merges.
func touched(ctx context.Context, g *git.Runner, sha string, parents int) ([]string, error) {
	var args []string
	switch {
	case parents == 2:
		args = []string{"show", "--remerge-diff"}
	case parents > 2:
		args = []string{"show", "--cc"}
	default: // --root: a root commit touches every file it has
		args = []string{"diff-tree", "--root", "--no-commit-id", "-r"}
	}
	if parents >= 2 {
		args = append(args, "--format=", "--no-color", "--no-ext-diff", "--no-textconv", "--no-show-signature")
	}
	args = append(args, "--no-renames", "--ignore-submodules=none", "--name-only", "-z", "--end-of-options", sha, "--")
	out, err := g.Run(ctx, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}
	files := []string{}
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}
