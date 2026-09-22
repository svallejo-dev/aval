package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	goldenPath = "testdata/bundle.golden.json"
	baseSHA    = "1111111111111111111111111111111111111111"
	headSHA    = "2222222222222222222222222222222222222222"
	olderSHA   = "3333333333333333333333333333333333333333" // an earlier head of the same PR
)

var (
	reviewAt = time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	genAt    = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
)

// sampleBundle is a blocked tier-1 change with one obligation of each
// strength, a mixed commit, a valid approval and a rejected override.
func sampleBundle() Bundle {
	return Bundle{
		SchemaVersion: SchemaVersion,
		Repo:          "svallejo-dev/aval-sandbox",
		Base:          baseSHA,
		Head:          headSHA,
		AvalVersion:   "v0.0.0-test",
		GeneratedAt:   genAt,
		Mode:          "enforce",
		Tier:          1,
		Changes:       []string{"add-refunds"},
		Obligations: []Obligation{
			{ID: "ORD-F01", Kind: "F", Source: "openspec/specs/refunds/spec.md#ORD-F01 Refund is idempotent", Delta: Added,
				Tests: []string{"TestRefunds/ORD-F01_second_refund_is_a_no-op"}, Before: Fail, After: Pass, Strength: Strong},
			{ID: "ORD-F02", Kind: "F", Source: "openspec/specs/refunds/spec.md#ORD-F02 Refund needs a charge", Delta: Added,
				Tests: []string{"TestRefunds/ORD-F02_unknown_charge"}, Before: BuildFail, After: Pass, Strength: Weak,
				Note: "refund.New did not exist at the base"},
			{ID: "ORD-I01", Kind: "I", Source: "openspec/specs/refunds/spec.md#ORD-I01 Totals never go negative", Delta: Modified,
				Characterization: true, Tests: []string{"TestRefunds/ORD-I01_totals"}, Before: Pass, After: Pass, Strength: Characterized},
			{ID: "ORD-N01", Kind: "N", Source: "openspec/specs/refunds/spec.md#ORD-N01 Never refund more than charged", Delta: Added,
				Tests: []string{"TestRefunds/ORD-N01_rejects_excess"}, Before: Pass, After: Pass, Strength: None},
		},
		Checks: []Check{
			{Name: "go-test", Command: "go test -json ./...", ExitCode: 0, DurationMS: 4210, Status: Pass},
			{Name: "golangci-lint", Command: "golangci-lint run --new-from-merge-base=main", ExitCode: 1, DurationMS: 9120, Status: Fail, Artifact: "lint.json"},
		},
		Scope: []Commit{
			{SHA: headSHA, Family: FamilyMixed, Families: []Family{FamilyDX, FamilyFeat}, Paths: []string{".golangci.yml", "internal/refund/refund.go"}},
		},
		Tamper: []Finding{{Kind: SkipAdded, ID: "ORD-N01", Detail: "t.Skip added to TestRefunds/ORD-N01_rejects_excess"}},
		Approvals: []Approval{
			{Kind: ApprovalHuman, Actor: "@lead", CommitID: headSHA, SubmittedAt: reviewAt, Valid: true},
			{Kind: ApprovalOverride, Actor: "@dev", CommitID: olderSHA, SubmittedAt: reviewAt.Add(-time.Hour), Reason: "hotfix",
				Valid: false, Rejection: "review of an earlier commit"},
		},
		Verdict: Verdict{Result: ResultBlock, Reasons: []Reason{
			{Code: "fail_before_missing", Message: "test passed at the base and the requirement is not characterization", ID: "ORD-N01"},
			{Code: "mixed_commit", Message: "a commit touches dx and feat paths"},
		}},
		NotCollected: []string{"mutation", "rollback", "slo"},
	}
}

func TestBundleGolden(t *testing.T) {
	t.Parallel()

	got, err := json.MarshalIndent(sampleBundle(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("bundle JSON changed; if intended, run go test -update and review the diff\ngot:\n%s", got)
	}
	if err := validateJSON(want); err != nil {
		t.Errorf("golden does not match the schema: %v", err)
	}
}

// TestNilSlicesAreArrays: "no findings" must serialize as [] (valid), never null.
func TestNilSlicesAreArrays(t *testing.T) {
	t.Parallel()

	b := Bundle{
		SchemaVersion: SchemaVersion, Repo: "r", Base: baseSHA, Head: headSHA, AvalVersion: "v0", GeneratedAt: genAt,
		Mode: "observe", Tier: 0, Verdict: Verdict{Result: ResultPass},
		Obligations: []Obligation{{ID: "ORD-F01", Kind: "F", Source: "s", Delta: Unchanged, Before: NotApply, After: NotRun, Strength: None, Note: "no bound test"}},
		Scope:       []Commit{{SHA: headSHA, Family: FamilyFeat}},
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("minimal bundle with nil slices: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("null")) {
		t.Errorf("found a null, want none: %s", raw)
	}
}

// TestEveryConstantIsInSchema catches enum drift in either direction.
func TestEveryConstantIsInSchema(t *testing.T) {
	t.Parallel()

	statuses := []Status{Pass, Fail, BuildFail, NotRun, Skipped, NotApply}
	for _, s := range statuses {
		b := sampleBundle()
		b.Checks[0].Status = s
		mustSchema(t, "status "+string(s), b)
	}
	for _, d := range []Delta{Added, Modified, Unchanged} {
		b := sampleBundle()
		b.Obligations[3].Delta = d
		mustSchema(t, "delta "+string(d), b)
	}
	for _, st := range []Strength{Strong, Weak, Characterized, None} {
		b := sampleBundle()
		b.Obligations[3].Strength = st
		mustSchema(t, "strength "+string(st), b)
	}
	for _, f := range []Family{FamilyDX, FamilyFeat, FamilySeam, FamilyOther} {
		b := sampleBundle()
		b.Scope[0] = Commit{SHA: headSHA, Family: f, Paths: []string{"x"}}
		mustSchema(t, "family "+string(f), b)
	}
	for _, k := range []FindingKind{FingerprintChanged, TestRemoved, SkipAdded, PolicyEdited, BaselineEdited} {
		b := sampleBundle()
		b.Tamper[0].Kind = k
		mustSchema(t, "finding "+string(k), b)
	}
	for _, k := range []ApprovalKind{ApprovalHuman, ApprovalOverride} {
		b := sampleBundle()
		b.Approvals[1].Kind = k
		b.Approvals[1].Reason = ""
		mustSchema(t, "approval kind "+string(k), b)
	}
	for _, r := range []Result{ResultPass, ResultWarn, ResultBlock} {
		b := sampleBundle()
		b.Verdict.Result = r
		mustSchema(t, "result "+string(r), b)
	}
	for _, k := range []string{"F", "N", "I", "S", "A", "O"} {
		b := sampleBundle()
		b.Obligations[3].Kind, b.Obligations[3].ID = k, "ORD-"+k+"09"
		mustSchema(t, "kind "+k, b)
	}
}

func mustSchema(t *testing.T, what string, b Bundle) {
	t.Helper()
	if err := validateSchema(b); err != nil {
		t.Errorf("%s rejected by the schema: %v", what, err)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Bundle)
		wantErr string // "" means valid
	}{
		{name: "sample is valid", mutate: func(*Bundle) {}},
		{name: "v1 schema version", mutate: func(b *Bundle) { b.SchemaVersion = 1 }, wantErr: "schema"},
		{name: "future schema version", mutate: func(b *Bundle) { b.SchemaVersion = 3 }, wantErr: "schema"},
		{name: "short head sha", mutate: func(b *Bundle) { b.Head = "2222222" }, wantErr: "schema"},
		{name: "invalid base sha", mutate: func(b *Bundle) { b.Base = "not-a-sha" }, wantErr: "schema"},
		{name: "unknown mode", mutate: func(b *Bundle) { b.Mode = "audit" }, wantErr: "schema"},
		{name: "tier too high", mutate: func(b *Bundle) { b.Tier = 4 }, wantErr: "schema"},
		{name: "negative tier", mutate: func(b *Bundle) { b.Tier = -1 }, wantErr: "schema"},
		{name: "empty aval version", mutate: func(b *Bundle) { b.AvalVersion = "" }, wantErr: "schema"},
		{name: "zero generatedAt", mutate: func(b *Bundle) { b.GeneratedAt = time.Time{} }, wantErr: "generatedAt"},
		{name: "unknown result", mutate: func(b *Bundle) { b.Verdict.Result = "maybe" }, wantErr: "schema"},
		{name: "block without reasons", mutate: func(b *Bundle) { b.Verdict.Reasons = nil }, wantErr: "at least one reason"},
		{name: "warn without reasons", mutate: func(b *Bundle) { b.Verdict = Verdict{Result: ResultWarn} }, wantErr: "at least one reason"},
		{name: "pass without reasons is valid", mutate: func(b *Bundle) { b.Verdict = Verdict{Result: ResultPass} }},
		{name: "bad obligation id", mutate: func(b *Bundle) { b.Obligations[0].ID = "ord-f01" }, wantErr: "schema"},
		{name: "negative duration", mutate: func(b *Bundle) { b.Checks[0].DurationMS = -1 }, wantErr: "schema"},
		{name: "empty check name", mutate: func(b *Bundle) { b.Checks[0].Name = "" }, wantErr: "schema"},
		{name: "strong without fail before", mutate: func(b *Bundle) { b.Obligations[0].Before = Pass }, wantErr: "strong needs"},
		{name: "strong without pass after", mutate: func(b *Bundle) { b.Obligations[0].After = Fail }, wantErr: "strong needs"},
		{name: "weak without build fail", mutate: func(b *Bundle) { b.Obligations[1].Before = Fail }, wantErr: "weak needs"},
		{name: "characterization not declared", mutate: func(b *Bundle) { b.Obligations[2].Characterization = false }, wantErr: "characterization strength"},
		{name: "strong on unchanged obligation", mutate: func(b *Bundle) { b.Obligations[0].Delta = Unchanged }, wantErr: "only applies to added or modified"},
		{name: "mixed without families", mutate: func(b *Bundle) { b.Scope[0].Families = nil }, wantErr: "mixed"},
		{name: "families on a feat commit", mutate: func(b *Bundle) { b.Scope[0].Family = FamilyFeat }, wantErr: "mixed"},
		{name: "approval without actor", mutate: func(b *Bundle) { b.Approvals[0].Actor = "" }, wantErr: "schema"},
		{name: "approval with a short commit id", mutate: func(b *Bundle) { b.Approvals[0].CommitID = "2222222" }, wantErr: "schema"},
		{name: "unknown approval kind", mutate: func(b *Bundle) { b.Approvals[0].Kind = "label" }, wantErr: "schema"},
		{name: "approval without submittedAt", mutate: func(b *Bundle) { b.Approvals[0].SubmittedAt = time.Time{} }, wantErr: "submittedAt"},
		{name: "valid approval with a rejection", mutate: func(b *Bundle) { b.Approvals[0].Rejection = "stale" }, wantErr: "no rejection"},
		{name: "human approval with a reason", mutate: func(b *Bundle) { b.Approvals[0].Reason = "because" }, wantErr: "only an override"},
		{name: "valid approval of an earlier commit", mutate: func(b *Bundle) { b.Approvals[0].CommitID = olderSHA }, wantErr: "head commit"},
		{name: "rejected approval of an earlier commit", mutate: func(b *Bundle) {
			b.Approvals[0].CommitID, b.Approvals[0].Valid, b.Approvals[0].Rejection = olderSHA, false, "review of an earlier commit"
		}},
		{name: "rejected override without rejection", mutate: func(b *Bundle) { b.Approvals[1].Rejection = "" }, wantErr: "rejection reason"},
		{name: "rejected override without reason is recordable", mutate: func(b *Bundle) {
			b.Approvals[1].Reason, b.Approvals[1].Rejection = "", "no reason given"
		}},
		{name: "valid override of an earlier commit", mutate: func(b *Bundle) {
			b.Approvals[1].Valid, b.Approvals[1].Rejection = true, ""
		}, wantErr: "head commit"},
		{name: "valid override without reason", mutate: func(b *Bundle) {
			b.Approvals[1].Valid, b.Approvals[1].Rejection, b.Approvals[1].CommitID, b.Approvals[1].Reason = true, "", headSHA, ""
		}, wantErr: "needs a reason"},
		{name: "valid override of the head commit", mutate: func(b *Bundle) {
			b.Approvals[1].Valid, b.Approvals[1].Rejection, b.Approvals[1].CommitID = true, "", headSHA
		}},
		{name: "no approvals", mutate: func(b *Bundle) { b.Approvals = nil }},
		{name: "characterization failing before", mutate: func(b *Bundle) { b.Obligations[2].Before = Fail }, wantErr: "characterization strength"},
		{name: "characterization failing after", mutate: func(b *Bundle) { b.Obligations[2].After = Fail }, wantErr: "characterization strength"},
		{name: "kind letter mismatch", mutate: func(b *Bundle) { b.Obligations[0].Kind = "N" }, wantErr: "kind letter"},
		{name: "duplicate families", mutate: func(b *Bundle) { b.Scope[0].Families = []Family{FamilyFeat, FamilyFeat} }, wantErr: "schema"},
		{name: "mixed from seam and other", mutate: func(b *Bundle) { b.Scope[0].Families = []Family{FamilySeam, FamilyOther} }, wantErr: "[dx feat]"},
		{name: "mixed with families out of order", mutate: func(b *Bundle) { b.Scope[0].Families = []Family{FamilyFeat, FamilyDX} }, wantErr: "[dx feat]"},
		{name: "mixed with a third family", mutate: func(b *Bundle) {
			b.Scope[0].Families = []Family{FamilyDX, FamilyFeat, FamilySeam}
		}, wantErr: "[dx feat]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := sampleBundle()
			tt.mutate(&b)
			err := b.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate error = %v, want ErrInvalid containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestSchemaIsClosed: readers must reject fields they do not understand.
func TestSchemaIsClosed(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown top-level field", func(m map[string]any) { m["trusted"] = true }},
		{"approvals missing", func(m map[string]any) { delete(m, "approvals") }},
		{"approvals null", func(m map[string]any) { m["approvals"] = nil }},
		{"v1 override field", func(m map[string]any) { m["override"] = nil }},
		{"unknown approval field", func(m map[string]any) {
			if list, ok := m["approvals"].([]any); ok {
				if first, ok := list[0].(map[string]any); ok {
					first["trusted"] = true
				}
			}
		}},
		{"verdict missing", func(m map[string]any) { delete(m, "verdict") }},
	} {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		tc.mutate(m)
		mutated, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSON(mutated); err == nil {
			t.Errorf("%s: schema accepted it", tc.name)
		}
	}
}

func TestCheckHead(t *testing.T) {
	t.Parallel()

	b := sampleBundle()
	if err := b.CheckHead(headSHA); err != nil {
		t.Errorf("CheckHead(head) = %v, want nil", err)
	}
	if err := b.CheckHead(baseSHA); !errors.Is(err, ErrInvalid) {
		t.Errorf("CheckHead(other) = %v, want ErrInvalid", err)
	}
}

// TestSchemaEnforcesApprovalRules: the approval rules the schema can express
// hold in the schema itself, not only in Go (ADR-0004: one source of truth).
func TestSchemaEnforcesApprovalRules(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(*Approval)
	}{
		{"valid with a rejection", func(a *Approval) { a.Valid, a.Rejection = true, "stale" }},
		{"rejected without rejection", func(a *Approval) { a.Valid, a.Rejection = false, "" }},
		{"human approval with a reason", func(a *Approval) { a.Kind, a.Reason = ApprovalHuman, "because" }},
		{"valid override without reason", func(a *Approval) {
			a.Kind, a.Valid, a.Rejection, a.Reason = ApprovalOverride, true, "", ""
		}},
	} {
		b := sampleBundle()
		b.Approvals[1].CommitID = headSHA
		tc.mutate(&b.Approvals[1])
		if err := validateSchema(b); err == nil {
			t.Errorf("%s: schema accepted it", tc.name)
		}
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(bytes.NewReader(golden))
	if err != nil {
		t.Fatalf("Parse(golden): %v", err)
	}
	if again, _ := json.MarshalIndent(b, "", "  "); !bytes.Equal(append(again, '\n'), golden) {
		t.Errorf("golden does not round-trip through Parse:\n%s", again)
	}

	// Each case is valid once re-encoded, so only a check of the raw bytes
	// catches it.
	for _, tc := range []struct {
		name string
		edit func(string) string
	}{
		{"unknown field", func(s string) string { return strings.Replace(s, `"repo":`, `"trusted": true, "repo":`, 1) }},
		{"v1 override field", func(s string) string { return strings.Replace(s, `"approvals":`, `"override": null, "approvals":`, 1) }},
		{"empty reason on a rejected override", func(s string) string {
			return strings.Replace(s, `"reason": "hotfix"`, `"reason": ""`, 1)
		}},
		{"empty rejection on a valid approval", func(s string) string {
			return strings.Replace(s, `"valid": true`, `"valid": true, "rejection": ""`, 1)
		}},
		{"duplicate head", func(s string) string {
			return strings.Replace(s, `"head":`, `"head": "`+olderSHA+`", "head":`, 1)
		}},
		{"trailing data", func(s string) string { return s + "{}" }},
	} {
		edited := tc.edit(string(golden))
		if edited == string(golden) {
			t.Fatalf("%s: the edit did not apply", tc.name)
		}
		if _, err := Parse(strings.NewReader(edited)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse = %v, want ErrInvalid", tc.name, err)
		}
	}
	if _, err := Parse(bytes.NewReader(append(golden, bytes.Repeat([]byte(" "), maxBundleBytes)...))); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized bundle: Parse = %v, want ErrInvalid", err)
	}
}

// TestSchemaEnforcesMixedRule: mixed means exactly [dx feat] in the schema
// too, not only in Go.
func TestSchemaEnforcesMixedRule(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]Commit{
		"mixed without families":  {SHA: headSHA, Family: FamilyMixed},
		"mixed from seam and dx":  {SHA: headSHA, Family: FamilyMixed, Families: []Family{FamilyDX, FamilySeam}},
		"mixed out of order":      {SHA: headSHA, Family: FamilyMixed, Families: []Family{FamilyFeat, FamilyDX}},
		"families on a dx commit": {SHA: headSHA, Family: FamilyDX, Families: []Family{FamilyDX, FamilyFeat}},
	} {
		b := sampleBundle()
		b.Scope[0] = c
		if err := validateSchema(b); err == nil {
			t.Errorf("%s: schema accepted it", name)
		}
	}
}
