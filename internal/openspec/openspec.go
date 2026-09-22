// Package openspec reads the OpenSpec tree of a repository (main specs, active
// and archived changes) and checks it against aval's obligation-ID rules
// (ADR-0002).
//
// The reader mirrors the markdown readers of OpenSpec 1.13.1
// (requirement-blocks.js and requirement-text.js), so aval sees the same
// requirements, names and scenarios that OpenSpec validates and archives. It
// never runs Node: the identity of a requirement comes from the markdown,
// because `openspec show --json` does not report names (ADR-0002, rule 7).
package openspec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// Dir is where OpenSpec keeps its tree, relative to the repository root.
const Dir = "openspec"

// Op is the operation a delta applies to a main spec.
type Op string

// Delta operations, as written in the `## <OP> Requirements` section titles.
const (
	Added    Op = "ADDED"
	Modified Op = "MODIFIED"
	Removed  Op = "REMOVED"
	Renamed  Op = "RENAMED"
)

// Scenario is a `#### ` heading of a requirement whose body is not empty.
type Scenario struct {
	Name string // heading text without "####", a closing "#" run or a "Scenario:" prefix
	Line int    // 1-based line of the heading
	Text string // trimmed body, OpenSpec's scenario rawText
}

// Requirement is one `### Requirement:` block of a spec or a delta.
type Requirement struct {
	ID    obligation.ID // zero when the name does not start with a valid ID
	Title string        // the name after the ID; empty when ID is zero
	Name  string        // normalized name, the key OpenSpec matches on (case-sensitive)
	Path  string        // slash-separated path from the repository root
	Line  int           // 1-based line of the header
	// Text is the body as OpenSpec reports it: trimmed lines up to the first
	// heading, without `**key**:` metadata lines unless they are all there is.
	Text string
	// Characterization marks pre-existing behavior: the body carries the
	// metadata line `**aval**: characterization`.
	Characterization bool
	Scenarios        []Scenario
}

// Spec is a main spec, openspec/specs/<capability>/spec.md.
type Spec struct {
	Capability   string // directory under openspec/specs, e.g. "refunds" or "billing/refunds"
	Path         string
	Requirements []Requirement
}

// Delta is one operation of a change on a capability.
type Delta struct {
	Op         Op
	Capability string
	// Requirement is the block of an ADDED, MODIFIED or REMOVED delta. A
	// REMOVED written as a bullet carries only its name and location.
	Requirement Requirement
	// From and To are the two sides of a RENAMED delta: name, ID, title and
	// location only.
	From, To Requirement
}

// Change is an OpenSpec change, active in openspec/changes/<id>/ or archived
// in openspec/changes/archive/<date>-<id>/.
type Change struct {
	ID       string // directory name, without the date prefix when archived
	Dir      string // slash-separated path from the repository root
	Archived bool
	Manifest *manifest.Change // the change's aval.yaml; nil when absent
	Deltas   []Delta
}

// Repo is the OpenSpec tree of a repository.
type Repo struct {
	Specs   []Spec   // sorted by capability
	Changes []Change // active changes, then archived ones, each sorted by directory
	// findings holds the single-file findings collected while reading the
	// main specs and the active changes.
	findings []Finding
}

var archivedDir = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}-(.+)$`)

// Load reads the OpenSpec tree of the repository at the root of fsys, usually
// os.DirFS(root). It fails if openspec/ does not exist, or if a change carries
// an invalid aval.yaml (the error wraps manifest.ErrInvalid).
func Load(ctx context.Context, fsys fs.FS) (*Repo, error) {
	if _, err := fs.Stat(fsys, Dir); err != nil {
		return nil, fmt.Errorf("openspec: %w", err)
	}
	r := &Repo{}
	files, err := discoverSpecFiles(ctx, fsys, path.Join(Dir, "specs"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		src, err := fs.ReadFile(fsys, f.path)
		if err != nil {
			return nil, fmt.Errorf("openspec: %w", err)
		}
		spec, found := parseSpec(f.capability, f.path, src)
		r.Specs = append(r.Specs, spec)
		r.findings = append(r.findings, found...)
	}

	changes := path.Join(Dir, "changes")
	archive := path.Join(changes, "archive")
	for _, group := range []struct {
		dir      string
		archived bool
	}{{changes, false}, {archive, true}} {
		names, err := changeDirs(fsys, group.dir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if !group.archived && name == "archive" {
				continue
			}
			if err := r.loadChange(ctx, fsys, group.dir, name, group.archived); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func (r *Repo) loadChange(ctx context.Context, fsys fs.FS, parent, name string, archived bool) error {
	ch := Change{ID: name, Dir: path.Join(parent, name), Archived: archived}
	if m := archivedDir.FindStringSubmatch(name); archived && m != nil {
		ch.ID = m[1]
	}
	var err error
	if ch.Manifest, err = readManifest(fsys, path.Join(ch.Dir, "aval.yaml")); err != nil {
		return err
	}
	files, err := discoverSpecFiles(ctx, fsys, path.Join(ch.Dir, "specs"))
	if err != nil {
		return err
	}
	for _, f := range files {
		src, err := fs.ReadFile(fsys, f.path)
		if err != nil {
			return fmt.Errorf("openspec: %w", err)
		}
		deltas, found := parseDelta(f.capability, f.path, src)
		ch.Deltas = append(ch.Deltas, deltas...)
		if !archived {
			r.findings = append(r.findings, found...)
		}
	}
	r.Changes = append(r.Changes, ch)
	return nil
}

func readManifest(fsys fs.FS, name string) (*manifest.Change, error) {
	f, err := fsys.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("openspec: %w", err)
	}
	defer f.Close()
	m, err := manifest.ParseChange(f)
	if err != nil {
		return nil, fmt.Errorf("openspec: %s: %w", name, err)
	}
	return &m, nil
}

// changeDirs lists the change directories in dir, skipping hidden ones, as
// OpenSpec does. A missing dir has none.
func changeDirs(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("openspec: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

type specFile struct{ capability, path string }

// discoverSpecFiles finds every spec.md below root at any depth, as OpenSpec
// does: hidden entries are skipped, symlinked directories are not followed, a
// spec.md directly in root has no capability and is ignored. The capability
// is the directory path below root. Results are sorted by capability.
func discoverSpecFiles(ctx context.Context, fsys fs.FS, root string) ([]specFile, error) {
	var out []specFile
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		capability := path.Dir(strings.TrimPrefix(p, root+"/"))
		if d.IsDir() || d.Name() != "spec.md" || capability == "." {
			return nil
		}
		if ok, err := isRegularFile(fsys, p, d); err != nil || !ok {
			return err
		}
		out = append(out, specFile{capability: capability, path: p})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("openspec: %w", err)
	}
	slices.SortFunc(out, func(a, b specFile) int { return strings.Compare(a.capability, b.capability) })
	return out, nil
}

// isRegularFile resolves a symlinked spec.md; a dangling link is not a spec.
func isRegularFile(fsys fs.FS, p string, d fs.DirEntry) (bool, error) {
	if d.Type()&fs.ModeSymlink == 0 {
		return d.Type().IsRegular(), nil
	}
	info, err := fs.Stat(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat: %w", err)
	}
	return info.Mode().IsRegular(), nil
}
