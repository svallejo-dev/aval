package openspec

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

// liveVersion is the OpenSpec release the spike fixtures were captured with.
const liveVersion = "1.13.1"

// TestLive is the live differential test: real OpenSpec, run through npx
// over each spike repository, must report what the spike captured, and the
// reader must see the requirements, bodies and scenarios its show reports.
// It needs Node and the network, so it runs only with AVAL_OPENSPEC_LIVE=1
// (the openspec-live CI job).
func TestLive(t *testing.T) {
	if os.Getenv("AVAL_OPENSPEC_LIVE") != "1" {
		t.Skip("set AVAL_OPENSPEC_LIVE=1 to run OpenSpec " + liveVersion + " through npx")
	}

	type liveCase struct {
		change   string
		fsys     fstest.MapFS
		captured string // validate output of the change
		specItem string // validate output of the main spec
	}
	proposal := &fstest.MapFile{Data: []byte("## Why\nSpike case.\n\n## What Changes\n- Spike case.\n")}
	baseline := fstest.MapFS{"openspec/specs/refunds/spec.md": {Data: fixture(t, "refunds/spec-before.md")}}
	for _, name := range []string{".openspec.yaml", "proposal.md", "tasks.md", "specs/refunds/spec.md"} {
		baseline["openspec/changes/add-refund-limits/"+name] = &fstest.MapFile{Data: fixture(t, "changes/add-refund-limits/"+name)}
	}
	cases := []liveCase{{
		change: "add-refund-limits", fsys: baseline,
		captured: "json/validate-change.json", specItem: "json/validate-spec-before.json",
	}}
	dirs, err := fs.Glob(spike, "*/*/delta.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range dirs {
		dir := path.Dir(delta)
		c := liveCase{
			change: path.Base(dir), fsys: spikeRepo(t, path.Base(dir), fixture(t, delta)),
			captured: dir + "/validate.json", specItem: "json/validate-spec-after.json",
		}
		c.fsys["openspec/changes/"+c.change+"/proposal.md"] = proposal
		cases = append(cases, c)
	}

	// Sequential on purpose: concurrent first runs of npx race on its cache.
	for _, c := range cases {
		t.Run(c.change, func(t *testing.T) {
			root := writeRepo(t, c.fsys)

			report, err := Validate(t.Context(), root, liveVersion)
			if err != nil {
				t.Fatal(err)
			}
			want := slices.Concat(decodeReport(t, c.captured).Items, decodeReport(t, c.specItem).Items)
			if !equalItems(report.Items, want) {
				t.Errorf("validate:\n got %+v\nwant %+v", report.Items, want)
			}

			r, err := Load(t.Context(), os.DirFS(root))
			if err != nil {
				t.Fatal(err)
			}
			assertShowSpec(t, r.Specs[0], openspecCLI(t, root, "show", "refunds", "--type", "spec", "--json"))
			assertShowDeltas(t, r.Changes[0].Deltas, openspecCLI(t, root, "show", c.change, "--type", "change", "--json", "--deltas-only"))
		})
	}
}

func decodeReport(t *testing.T, name string) Report {
	t.Helper()
	r, err := parseReport(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// equalItems compares reports item by item, in any order: OpenSpec sorts
// them with the locale's collation.
func equalItems(got, want []Item) bool {
	return len(got) == len(want) && !slices.ContainsFunc(want, func(w Item) bool {
		return !slices.ContainsFunc(got, func(g Item) bool {
			return g.ID == w.ID && g.Type == w.Type && g.Valid == w.Valid && slices.Equal(g.Issues, w.Issues)
		})
	})
}

func writeRepo(t *testing.T, fsys fstest.MapFS) string {
	t.Helper()
	root := t.TempDir()
	for name, f := range fsys {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// openspecCLI runs one OpenSpec command the way Validate does and returns
// its stdout.
func openspecCLI(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), runTimeout)
	defer cancel()
	out, err := execRunner{}.Run(ctx, dir, cliEnv, "npx", append([]string{"-y", npmPackage + "@" + liveVersion}, args...)...)
	if err != nil || out.code != 0 {
		t.Fatalf("openspec %q: exit %d, %v\n%s%s", args, out.code, err, out.stdout, out.stderr)
	}
	return out.stdout
}
