package openspec

import (
	"encoding/json"
	"slices"
	"testing"
)

// The parts of `openspec show --json` the reader must agree with. show
// reports bodies and scenario texts but no names (ADR-0002, rule 7).
type (
	showRequirement struct {
		Text      string `json:"text"`
		Scenarios []struct {
			RawText string `json:"rawText"`
		} `json:"scenarios"`
	}
	showSpec struct {
		RequirementCount int               `json:"requirementCount"`
		Requirements     []showRequirement `json:"requirements"`
	}
	showChange struct {
		DeltaCount int `json:"deltaCount"`
		Deltas     []struct {
			Spec        string           `json:"spec"`
			Operation   string           `json:"operation"`
			Requirement *showRequirement `json:"requirement"`
			Rename      *struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"rename"`
		} `json:"deltas"`
	}
)

func decodeShow[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode show output: %v\n%s", err, data)
	}
	return v
}

func assertRequirement(t *testing.T, got Requirement, want showRequirement) {
	t.Helper()
	if got.Text != want.Text {
		t.Errorf("%s text:\n got %q\nwant %q", got.Name, got.Text, want.Text)
	}
	if len(got.Scenarios) != len(want.Scenarios) {
		t.Errorf("%s: %d scenarios, show has %d", got.Name, len(got.Scenarios), len(want.Scenarios))
		return
	}
	for i, s := range got.Scenarios {
		if s.Text != want.Scenarios[i].RawText {
			t.Errorf("%s scenario %q:\n got %q\nwant %q", got.Name, s.Name, s.Text, want.Scenarios[i].RawText)
		}
	}
}

// assertShowSpec compares a main spec with `openspec show <spec> --type spec --json`.
func assertShowSpec(t *testing.T, got Spec, show []byte) {
	t.Helper()
	want := decodeShow[showSpec](t, show)
	if len(got.Requirements) != want.RequirementCount || len(want.Requirements) != want.RequirementCount {
		t.Fatalf("%s: %d requirements, show counts %d", got.Path, len(got.Requirements), want.RequirementCount)
	}
	for i, q := range got.Requirements {
		assertRequirement(t, q, want.Requirements[i])
	}
}

// assertShowDeltas compares a change's deltas with
// `openspec show <change> --type change --json --deltas-only`.
func assertShowDeltas(t *testing.T, got []Delta, show []byte) {
	t.Helper()
	want := decodeShow[showChange](t, show)
	if len(got) != want.DeltaCount || len(want.Deltas) != want.DeltaCount {
		t.Fatalf("%d deltas, show counts %d", len(got), want.DeltaCount)
	}
	for i, d := range got {
		w := want.Deltas[i]
		if d.Capability != w.Spec || string(d.Op) != w.Operation {
			t.Errorf("delta %d: %s %s, show has %s %s", i, d.Op, d.Capability, w.Operation, w.Spec)
			continue
		}
		switch {
		case d.Op == Renamed && w.Rename != nil:
			if d.From.Name != w.Rename.From || d.To.Name != w.Rename.To {
				t.Errorf("delta %d: RENAMED %q -> %q, show has %q -> %q", i, d.From.Name, d.To.Name, w.Rename.From, w.Rename.To)
			}
		case d.Op != Renamed && w.Requirement != nil:
			assertRequirement(t, d.Requirement, *w.Requirement)
		default:
			t.Errorf("delta %d: %s without its show payload", i, d.Op)
		}
	}
}

// TestShowParity is the offline differential test: the reader extracts the
// requirements, bodies and scenarios OpenSpec 1.13.1 reported for the spike
// specs and change (json/), plus the names show leaves out.
func TestShowParity(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		spec, show string
		names      []string
	}{
		{"refunds/spec-before.md", "json/show-spec-before.json", []string{
			"ORD-F01 Refund is idempotent", "ORD-S01 Fast refund answer",
			"ORD-I01 Ledger entry per refund", "ORD-A01 Gateway confirms refunds at once",
		}},
		{"refunds/spec-after.md", "json/show-spec-after.json", []string{
			"ORD-F01 Refund is idempotent", "ORD-S01 Refund latency under 300 ms",
			"ORD-I01 Ledger entry per refund", "ORD-N01 Refund never exceeds order total",
		}},
	} {
		t.Run(tt.spec, func(t *testing.T) {
			t.Parallel()
			got, found := parseSpec("refunds", tt.spec, fixture(t, tt.spec))
			if len(found) != 0 {
				t.Errorf("findings: %v", found)
			}
			assertShowSpec(t, got, fixture(t, tt.show))
			var names, marked []string
			for _, q := range got.Requirements {
				names = append(names, q.Name)
				if q.Characterization {
					marked = append(marked, q.ID.String())
				}
			}
			if !slices.Equal(names, tt.names) {
				t.Errorf("names = %q, want %q", names, tt.names)
			}
			if !slices.Equal(marked, []string{"ORD-I01"}) {
				t.Errorf("characterization on %q, want only ORD-I01", marked)
			}
		})
	}

	t.Run("change", func(t *testing.T) {
		t.Parallel()
		const delta = "changes/add-refund-limits/specs/refunds/spec.md"
		got, found := parseDelta("refunds", delta, fixture(t, delta))
		if len(found) != 0 {
			t.Errorf("findings: %v", found)
		}
		assertShowDeltas(t, got, fixture(t, "json/show-change-deltas.json"))
	})
}
