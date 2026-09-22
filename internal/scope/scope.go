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
// A two-parent merge contributes only what it changed beyond the merge git
// would have made on its own: the paths of git show --remerge-diff, which
// needs git 2.36 or newer. Observed with git 2.50 and pinned by the tests:
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
// Every git command runs with GIT_NO_REPLACE_OBJECTS=1: the change under
// review could plant replace refs to make git read other commits.
package scope

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/manifest"
)

var (
	// ErrInvalidRange is wrapped by Classify's error for a base or head it
	// refuses before running git.
	ErrInvalidRange = errors.New("invalid commit range")
	// ErrToolMissing is wrapped by Classify's error when git is not on PATH
	// or is older than 2.36. Callers map it to exit code 3.
	ErrToolMissing = errors.New("scope: git 2.36 or newer is required")
)

// Classify classifies every commit of base..head in repoRoot, parents first,
// by the paths it touches. paths are the globs of the base policy, already
// checked by manifest validation. An empty base classifies every commit
// reachable from head. The result is never nil, and neither is any Paths.
func Classify(ctx context.Context, repoRoot, base, head string, paths manifest.Paths) ([]evidence.Commit, error) {
	rng, err := revRange(base, head)
	if err != nil {
		return nil, err
	}
	version, err := git(ctx, repoRoot, "version")
	if err != nil {
		return nil, err
	}
	if err := checkVersion(string(version)); err != nil {
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
		files, err := touched(ctx, repoRoot, shas[0], len(shas)-1)
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

// touched lists the paths commit sha, with the given number of parents,
// touches, as git prints them (sorted). See the package doc for merges.
func touched(ctx context.Context, dir, sha string, parents int) ([]string, error) {
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
	out, err := git(ctx, dir, append(args, "--no-renames", "--name-only", "-z", "--end-of-options", sha, "--")...)
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

// checkVersion returns ErrToolMissing unless out, what git version prints,
// names git 2.36 or newer, the first with --remerge-diff.
func checkVersion(out string) error {
	v, _ := strings.CutPrefix(strings.TrimSpace(out), "git version ")
	var major, minor int
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err == nil && (major > 2 || major == 2 && minor >= 36) {
		return nil
	}
	return fmt.Errorf("%w: git version says %q", ErrToolMissing, strings.TrimSpace(out))
}

// git runs git in dir, ignoring replace refs, and returns its stdout. A
// failure carries git's stderr; a missing git wraps ErrToolMissing and
// exec.ErrNotFound.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // no shell: fixed flags, and revisions follow --end-of-options
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], context.Cause(ctx))
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(exitErr.Stderr))
	case errors.Is(err, exec.ErrNotFound):
		return nil, fmt.Errorf("%w: %w", ErrToolMissing, err)
	case err != nil:
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}
