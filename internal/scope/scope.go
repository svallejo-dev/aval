// Package scope classifies each commit of a range into a command family by
// the paths it touches, with the dx, feat and seam globs of the base policy
// (ADR-0005 §3b).
//
// # Paths
//
// A path is seam when any seam glob matches it, whatever else matches too.
// Otherwise it takes the family of the longest dx or feat glob (counted in
// runes) that matches it, dx on a tie, and it is other when none does.
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
// A merge contributes only the paths of its combined diff, git show --cc
// --name-only: the files whose merged content differs from every parent.
// Observed with git 2.50; the tests pin the first three:
//
//   - A clean merge of main into the branch, where each side changed other
//     files, lists nothing and is other.
//   - An evil merge lists the files it changes itself, e.g. one that neither
//     side touched.
//   - --name-only does not apply the hunk simplification of --cc: a file
//     both sides changed differs from every parent, so it is listed even
//     when git merged it without conflicts, and the merge takes its family.
//   - A merge that keeps one parent's version of a file, dropping the other
//     side's change to it, matches that parent and lists nothing.
package scope

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/manifest"
)

// ErrInvalidRange is wrapped by Classify's error for a base or head it
// refuses before running git.
var ErrInvalidRange = errors.New("invalid commit range")

// Classify classifies every commit of base..head in repoRoot, parents first,
// by the paths it touches. paths are the globs of the base policy, already
// checked by manifest validation. An empty base classifies every commit
// reachable from head. The result is never nil, and neither is any Paths.
func Classify(ctx context.Context, repoRoot, base, head string, paths manifest.Paths) ([]evidence.Commit, error) {
	rng, err := revRange(base, head)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, repoRoot, "rev-list", "--reverse", "--topo-order", "--parents", "--end-of-options", rng, "--")
	if err != nil {
		return nil, err
	}
	commits := []evidence.Commit{}
	for line := range strings.Lines(string(out)) {
		shas := strings.Fields(line) // the commit, then its parents
		if len(shas) == 0 {
			continue
		}
		files, err := touched(ctx, repoRoot, shas[0], len(shas) > 2)
		if err != nil {
			return nil, err
		}
		family, families := ClassifyPaths(paths, files)
		commits = append(commits, evidence.Commit{SHA: shas[0], Family: family, Families: families, Paths: files})
	}
	return commits, nil
}

// ClassifyPaths returns the family of a commit that touches files and, for
// a mixed commit only, the families it spans.
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
// of the longest matching dx or feat glob, dx on a tie; other if none.
func pathFamily(paths manifest.Paths, file string) evidence.Family {
	if isSeam(paths, file) {
		return evidence.FamilySeam
	}
	best, bestLen := evidence.FamilyOther, -1
	for _, fam := range []struct {
		family evidence.Family
		globs  []string
	}{{evidence.FamilyDX, paths.DX}, {evidence.FamilyFeat, paths.Feat}} {
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

// touched lists the paths commit sha touches, as git prints them (sorted):
// its combined diff for a merge, its diff against its parent otherwise, and
// every file for a root commit.
func touched(ctx context.Context, dir, sha string, merge bool) ([]string, error) {
	args := []string{"diff-tree", "--root", "--no-commit-id", "--no-renames", "-r", "--name-only", "-z", "--end-of-options", sha, "--"}
	if merge {
		args = []string{
			"show", "--cc", "--no-renames", "--name-only", "--format=", "-z",
			"--no-color", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--end-of-options", sha, "--",
		}
	}
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// git runs git in dir and returns its stdout. A failure carries git's stderr;
// a missing git wraps exec.ErrNotFound.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // no shell: fixed flags, and revisions follow --end-of-options
	cmd.Dir = dir
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], context.Cause(ctx))
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(exitErr.Stderr))
	case err != nil:
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}
