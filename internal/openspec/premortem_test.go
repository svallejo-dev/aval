package openspec

import (
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// ids parses the obligation IDs a test names.
func ids(t *testing.T, names ...string) []obligation.ID {
	t.Helper()
	out := make([]obligation.ID, 0, len(names))
	for _, s := range names {
		id, err := obligation.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// TestReadPremortem checks the CommonMark reading of a premortem.md, as
// internal/verify checked it before the parsing moved here: what counts as a
// top-level item, what a fence hides, and which items cite no ID of the
// change (ADR-0005 §4).
func TestReadPremortem(t *testing.T) {
	t.Parallel()
	const md = "# Premortem\n\n" +
		"- ORD-F01 is retried and refunds twice\n  with a second line\n" +
		"- nothing guards this one\n" +
		"1) ORD-F02 overflows\n" +
		"```\n- ORD-F03 inside a fence is not an item\n```\n" +
		"~~~\n- ORD-F03 inside a tilde fence is not an item either\n~~~\n" +
		"  - a nested item is not top level\n" +
		"| a table row | is not an item |\n"
	const dir = "openspec/changes/add-refunds"
	fsys := fstest.MapFS{dir + "/premortem.md": {Data: []byte(md)}}

	pm, err := ReadPremortem(fsys, dir, ids(t, "ORD-F01", "ORD-F02"))
	if err != nil {
		t.Fatal(err)
	}
	want := Premortem{Present: true, Items: 3, Unmapped: []string{"- nothing guards this one"}}
	if !reflect.DeepEqual(pm, want) {
		t.Errorf("ReadPremortem = %+v, want %+v", pm, want)
	}
	if pm, err := ReadPremortem(fsys, "openspec/changes/nowhere", nil); err != nil || pm.Present || pm.Items != 0 {
		t.Errorf("ReadPremortem without a file = %+v, %v; want it absent", pm, err)
	}
}
