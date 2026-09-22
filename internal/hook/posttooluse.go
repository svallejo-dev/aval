package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/testsource"
)

// editTools are the tools, lowercased, that write the file they name: Claude
// Code's Edit, MultiEdit and Write, and Copilot's edit and create. Cursor's
// afterFileEdit has no tool name.
var editTools = []string{"", "edit", "multiedit", "write", "create"}

const tamperNote = "aval's gate fingerprints bound tests: an edit, removal, skip or test data " +
	"change to one whose obligation is not in the delta of the PR's OpenSpec changes blocks " +
	"the PR as tamper. Keep these tests as they were, unless a change you are working on " +
	"adds, modifies, removes or renames their requirement."

// checkBoundTests answers a PostToolUse event. When the agent edited a
// _test.go file that declares obligation IDs (testsource), it reminds the
// agent that their tests are guarded, leaving out the IDs in the delta of the
// active changes. Without a readable OpenSpec tree it lists every ID. It only
// sees what the file declares now: a removed declaration is left to the gate.
func checkBoundTests(ctx context.Context, in input) *response {
	if !strings.HasSuffix(in.FilePath, "_test.go") || !slices.Contains(editTools, strings.ToLower(in.Tool)) {
		return nil
	}
	file := in.FilePath
	if !filepath.IsAbs(file) {
		file = filepath.Join(in.Cwd, file)
	}
	file, err := filepath.Abs(file) // openSpecRoot walks up from it
	if err != nil {
		return nil
	}
	ids, err := boundIDs(file)
	if err != nil {
		return nil
	}
	delta, known := activeDelta(ctx, filepath.Dir(file))
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

// boundIDs returns the obligation IDs that file declares, sorted, without
// duplicates. Scan reads file's directory and the ones below it, because a
// table may live in another test file of the package.
func boundIDs(file string) ([]string, error) {
	decls, err := testsource.Scan(filepath.Dir(file))
	if err != nil {
		return nil, fmt.Errorf("bound tests of %s: %w", file, err)
	}
	var ids []string
	for _, d := range decls {
		if d.File == filepath.Base(file) {
			ids = append(ids, d.ID.String())
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// activeDelta returns the IDs that the active changes of the nearest OpenSpec
// tree above dir add, modify, remove or rename: those whose tests the gate
// expects to change (ADR-0005 §1). Archived changes do not count, even when
// the PR archives them. It reports false when there is no readable tree.
func activeDelta(ctx context.Context, dir string) (map[string]bool, bool) {
	root, ok := openSpecRoot(dir)
	if !ok {
		return nil, false
	}
	repo, err := openspec.Load(ctx, os.DirFS(root))
	if err != nil {
		return nil, false
	}
	ids := map[string]bool{}
	for _, c := range repo.Changes {
		for _, d := range c.Deltas {
			for _, id := range []obligation.ID{d.Requirement.ID, d.From.ID, d.To.ID} {
				if !c.Archived && !id.IsZero() {
					ids[id.String()] = true
				}
			}
		}
	}
	return ids, true
}

// openSpecRoot returns the nearest directory from dir up with openspec/specs/
// or openspec/changes/, as `aval trace` finds the root.
func openSpecRoot(dir string) (string, bool) {
	for {
		for _, sub := range []string{"specs", "changes"} {
			if fi, err := os.Stat(filepath.Join(dir, openspec.Dir, sub)); err == nil && fi.IsDir() {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
