package hook

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/testsource"
)

// editTools are the Claude Code tools that write the file they name.
var editTools = []string{"Edit", "Write"}

const tamperNote = "aval's gate fingerprints bound tests: an edit, removal, skip or test data " +
	"change to one whose obligation is not in the delta of the PR's OpenSpec changes blocks " +
	"the PR as tamper. Keep these tests as they were, unless a change you are working on " +
	"adds, modifies or removes their requirement."

// checkBoundTests answers a PostToolUse event. When the agent edited a
// _test.go file of its repository that declares obligation IDs (testsource),
// it reminds the agent that their tests are guarded, leaving out the IDs in
// the delta of the active changes. Without a readable OpenSpec tree it lists
// every ID. It only sees what the file declares now: a removed declaration is
// left to the gate.
func checkBoundTests(ctx context.Context, in input) *response {
	file := in.ToolInput.FilePath
	if !strings.HasSuffix(file, "_test.go") || !slices.Contains(editTools, in.ToolName) {
		return nil
	}
	root, ok := gitRoot(cmp.Or(in.Cwd, "."))
	if !ok {
		return nil
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(in.Cwd, file)
	}
	dir, err := resolve(filepath.Dir(file))
	if err != nil || !within(root, dir) {
		return nil
	}
	ids, err := boundIDs(dir, filepath.Base(file))
	if err != nil {
		return nil
	}
	delta, known := activeDelta(ctx, dir, root)
	ids = slices.DeleteFunc(ids, func(id string) bool { return delta[id] })
	if len(ids) == 0 {
		return nil
	}
	msg := "aval: this file binds tests to " + strings.Join(ids, ", ")
	if known {
		msg += ", which are not in the delta of any active OpenSpec change"
	}
	return &response{Specific: &specific{HookEventName: "PostToolUse", AdditionalContext: msg + ". " + tamperNote}}
}

// boundIDs returns the obligation IDs that file, in dir, declares: sorted,
// without duplicates. Only dir's package is read; a table may live in any of
// its test files.
func boundIDs(dir, file string) ([]string, error) {
	decls, err := testsource.ScanDir(dir)
	if err != nil {
		return nil, fmt.Errorf("bound tests of %s: %w", file, err)
	}
	var ids []string
	for _, d := range decls {
		if d.File == file {
			ids = append(ids, d.ID.String())
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// activeDelta returns the IDs that the active changes of the nearest OpenSpec
// tree from dir up to root add, modify or remove: those whose tests the gate
// expects to change (ADR-0005 §1). A rename keeps its ID and its tests stay
// guarded. It reports false when there is no readable tree.
//
// TODO(M2): share the gate's helper for the changes of the PR, which counts
// the changes that base..head touches, archived ones included, instead of
// every active change.
func activeDelta(ctx context.Context, dir, root string) (map[string]bool, bool) {
	for !isOpenSpecRoot(dir) {
		if dir == root || filepath.Dir(dir) == dir {
			return nil, false
		}
		dir = filepath.Dir(dir)
	}
	repo, err := openspec.Load(ctx, os.DirFS(dir))
	if err != nil {
		return nil, false
	}
	ids := map[string]bool{}
	for _, c := range repo.Changes {
		for _, d := range c.Deltas {
			if !c.Archived && d.Op != openspec.Renamed && !d.Requirement.ID.IsZero() {
				ids[d.Requirement.ID.String()] = true
			}
		}
	}
	return ids, true
}

// isOpenSpecRoot reports whether dir holds openspec/specs/ or
// openspec/changes/, as `aval trace` finds the root.
func isOpenSpecRoot(dir string) bool {
	for _, sub := range []string{"specs", "changes"} {
		if fi, err := os.Stat(filepath.Join(dir, openspec.Dir, sub)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// gitRoot returns the nearest directory from dir up that holds .git, with
// symlinks resolved. It runs no git, so the hook starts no process for it.
func gitRoot(dir string) (string, bool) {
	dir, err := resolve(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// resolve returns the absolute path of dir with symlinks resolved.
func resolve(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	return resolved, nil
}

// within reports whether dir is root or below it.
func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
