package openspec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/svallejo-dev/aval/internal/manifest"
)

func TestLoadLayout(t *testing.T) {
	t.Parallel()

	spec := &fstest.MapFile{Data: []byte(f01)}
	delta := &fstest.MapFile{Data: []byte("## REMOVED Requirements\n- `### Requirement: ORD-F01 One`\n")}
	fsys := fstest.MapFS{
		"openspec/specs/billing/refunds/spec.md":                   spec,
		"openspec/specs/a/b/spec.md":                               spec,
		"openspec/specs/a-b/spec.md":                               spec,
		"openspec/specs/.hidden/spec.md":                           spec,
		"openspec/specs/spec.md":                                   spec,
		"openspec/specs/x/notes.md":                                spec,
		"openspec/specs/pipe/spec.md":                              {Mode: fs.ModeNamedPipe},
		"openspec/changes/README.md":                               spec,
		"openspec/changes/c1/aval.yaml":                            {Data: []byte("version: 1\ntier: 0\n")},
		"openspec/changes/c1/specs/a-b/spec.md":                    delta,
		"openspec/changes/.draft/specs/a-b/spec.md":                delta,
		"openspec/changes/archive/2026-01-02-old/specs/a/spec.md":  delta,
		"openspec/changes/archive/2026-01-02-old/specs/b/spec.md":  delta,
		"openspec/changes/archive/legacy/specs/a/spec.md":          delta,
		"openspec/changes/archive/.tmp/specs/a/spec.md":            delta,
		"openspec/changes/archive/2026-01-03-empty/proposal.md":    spec,
		"openspec/changes/archive/2026-01-02-old/specs/a/notes.md": delta,
	}
	r, err := Load(context.Background(), fsys)
	if err != nil {
		t.Fatal(err)
	}

	var specs []string
	for _, s := range r.Specs {
		specs = append(specs, fmt.Sprintf("%s %s %d", s.Capability, s.Path, len(s.Requirements)))
	}
	wantSpecs := []string{
		"a-b openspec/specs/a-b/spec.md 1", // code-point order, as OpenSpec sorts: '-' < '/'
		"a/b openspec/specs/a/b/spec.md 1",
		"billing/refunds openspec/specs/billing/refunds/spec.md 1",
	}
	if !slices.Equal(specs, wantSpecs) {
		t.Errorf("specs:\n got %q\nwant %q", specs, wantSpecs)
	}

	var changes []string
	for _, c := range r.Changes {
		var caps []string
		for _, d := range c.Deltas {
			caps = append(caps, d.Capability)
		}
		changes = append(changes, fmt.Sprintf("%s %s archived=%v manifest=%v deltas=%v", c.ID, c.Dir, c.Archived, c.Manifest != nil, caps))
	}
	wantChanges := []string{
		"c1 openspec/changes/c1 archived=false manifest=true deltas=[a-b]",
		"old openspec/changes/archive/2026-01-02-old archived=true manifest=false deltas=[a b]",
		"empty openspec/changes/archive/2026-01-03-empty archived=true manifest=false deltas=[]",
		"legacy openspec/changes/archive/legacy archived=true manifest=false deltas=[a]",
	}
	if !slices.Equal(changes, wantChanges) {
		t.Errorf("changes:\n got %q\nwant %q", changes, wantChanges)
	}
	if m := r.Changes[0].Manifest; m.Version != 1 || m.Tier != 0 {
		t.Errorf("manifest = %+v", m)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	specs := fstest.MapFS{"openspec/specs/a/spec.md": {Data: []byte(f01)}}
	tests := []struct {
		name string
		ctx  context.Context
		fsys fs.FS
		is   error
		msg  string
	}{
		{name: "no openspec directory", ctx: context.Background(), fsys: fstest.MapFS{"README.md": {}}, is: fs.ErrNotExist},
		{name: "canceled", ctx: canceled, fsys: specs, is: context.Canceled},
		{
			name: "invalid change manifest",
			ctx:  context.Background(),
			fsys: fstest.MapFS{"openspec/changes/archive/2026-01-01-c/aval.yaml": {Data: []byte("version: 1\ntier: 9\n")}},
			is:   manifest.ErrInvalid,
			msg:  "openspec/changes/archive/2026-01-01-c/aval.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := Load(tt.ctx, tt.fsys)
			if !errors.Is(err, tt.is) || r != nil {
				t.Fatalf("Load() = %v, %v; want an error wrapping %v", r, err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("error %q does not name %q", err, tt.msg)
			}
		})
	}
}

// TestLoadSymlinks: a symlinked spec.md is read, a dangling one or one that
// points at a directory is not, and symlinked directories are not followed.
func TestLoadSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	write("shared/spec.md", f01)
	link("../../../shared/spec.md", "openspec/specs/linked/spec.md")
	link("../../../shared/missing.md", "openspec/specs/dangling/spec.md")
	link("../../../shared", "openspec/specs/dir/spec.md")
	link("../../shared", "openspec/specs/followed")

	r, err := Load(context.Background(), os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Specs) != 1 || r.Specs[0].Capability != "linked" || len(r.Specs[0].Requirements) != 1 {
		t.Errorf("specs = %+v, want only the linked spec", r.Specs)
	}
}
