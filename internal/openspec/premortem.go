package openspec

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// Premortem is what a change's premortem.md holds, as this package reads it.
// What a gate asks of it — a premortem at all, an item per obligation — is the
// gate's business (ADR-0005 §4); the markdown is this one's.
type Premortem struct {
	Present bool // premortem.md exists
	Items   int  // top-level list items outside fenced code blocks
	// Unmapped are the items that cite none of the IDs asked about, each as
	// its own first line, trimmed.
	Unmapped []string
}

// listItemStart matches the marker of a top-level markdown list item and
// listFence the start or end of a fenced code block, indented by up to three
// spaces as CommonMark allows.
var (
	listItemStart = regexp.MustCompile(`^([-*+]|[0-9]{1,9}[.)])[ \t]`)
	listFence     = regexp.MustCompile("^ {0,3}(```|~~~)")
)

// ReadPremortem reads dir/premortem.md from fsys — the repository as an fs.FS,
// usually os.DirFS(root), and a change directory such as
// openspec/changes/<id>: whether it exists, how many top-level list items it
// has outside fenced code blocks, and which of them cite none of ids, the
// obligation IDs the change's own deltas name. A missing file is not an error:
// a change need not have a premortem, and whether it must is the gate's rule.
func ReadPremortem(fsys fs.FS, dir string, ids []obligation.ID) (Premortem, error) {
	data, err := fs.ReadFile(fsys, path.Join(dir, "premortem.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return Premortem{}, nil
	}
	if err != nil {
		return Premortem{}, fmt.Errorf("openspec: %w", err)
	}
	pm := Premortem{Present: true}
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
		case listFence.MatchString(line):
			fenced = !fenced
		case fenced:
		case listItemStart.MatchString(line):
			items = append(items, line)
		case len(items) > 0 && strings.TrimSpace(line) != "" && (line[0] == ' ' || line[0] == '\t'):
			items[len(items)-1] += line
		}
	}
	return items
}
