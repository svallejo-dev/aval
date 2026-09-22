package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	goldenPath = "testdata/bundle.golden.json"
	baseSHA    = "1111111111111111111111111111111111111111"
	headSHA    = "2222222222222222222222222222222222222222"
)

// sampleBundle is a blocked tier-1 change: one obligation with strong
// evidence and one whose test already passed at the base.
func sampleBundle() Bundle {
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	return Bundle{
		SchemaVersion: SchemaVersion,
		Repo:          "svallejo-dev/aval-sandbox",
		Base:          baseSHA,
		Head:          headSHA,
		AvalVersion:   "v0.0.0-test",
		GeneratedAt:   at,
		Mode:          "enforce",
		Tier:          1,
		Changes:       []string{"add-refunds"},
		Obligations: []Obligation{
			{ID: "ORD-F01", Kind: "F", Source: "openspec/specs/refunds/spec.md#ORD-F01 Refund is idempotent",
				Tests: []string{"TestRefunds/ORD-F01_second_refund_is_a_no-op"}, Before: Fail, After: Pass, Strength: Strong},
			{ID: "ORD-N01", Kind: "N", Source: "openspec/specs/refunds/spec.md#ORD-N01 Never refund more than charged",
				Tests: []string{"TestRefunds/ORD-N01_rejects_excess"}, Before: Pass, After: Pass, Strength: None},
		},
		Checks: []Check{
			{Name: "go-test", Command: "go test -json ./...", ExitCode: 0, DurationMS: 4210, Status: Pass},
			{Name: "golangci-lint", Command: "golangci-lint run --new-from-merge-base=main", ExitCode: 0, DurationMS: 9120, Status: Pass},
		},
		Scope: []Commit{
			{SHA: headSHA, Family: "feat", Paths: []string{"internal/refund/refund.go", "internal/refund/refund_test.go"}},
		},
		Tamper:   []Finding{},
		Override: nil,
		Verdict: Verdict{Result: ResultBlock, Reasons: []Reason{
			{Code: "fail_before_missing", Message: "test passed at the base and the requirement is not marked characterization", ID: "ORD-N01"},
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
}

func TestGoldenMatchesSchema(t *testing.T) {
	t.Parallel()

	sch := compileSchema(t)
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSON(sch, raw); err != nil {
		t.Fatalf("golden bundle does not match the JSON Schema: %v", err)
	}

	// The schema must reject what Validate rejects structurally.
	bad := bytes.Replace(raw, []byte(`"mode": "enforce"`), []byte(`"mode": "audit"`), 1)
	if err := validateJSON(sch, bad); err == nil {
		t.Error("schema accepted mode \"audit\"")
	}
	extra := bytes.Replace(raw, []byte(`"tier": 1,`), []byte(`"tier": 1, "trusted": true,`), 1)
	if err := validateJSON(sch, extra); err == nil {
		t.Error("schema accepted an unknown top-level field")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Bundle)
		wantErr string
	}{
		{name: "sample is valid", mutate: func(*Bundle) {}},
		{name: "wrong schema version", mutate: func(b *Bundle) { b.SchemaVersion = 2 }, wantErr: "schemaVersion"},
		{name: "short sha", mutate: func(b *Bundle) { b.Head = "2222222" }, wantErr: "40-character"},
		{name: "unknown mode", mutate: func(b *Bundle) { b.Mode = "audit" }, wantErr: "mode"},
		{name: "tier out of range", mutate: func(b *Bundle) { b.Tier = 4 }, wantErr: "tier"},
		{name: "unknown result", mutate: func(b *Bundle) { b.Verdict.Result = "maybe" }, wantErr: "verdict.result"},
		{name: "block without reasons", mutate: func(b *Bundle) { b.Verdict.Reasons = nil }, wantErr: "at least one reason"},
		{name: "override without reason", mutate: func(b *Bundle) {
			b.Override = &Override{Actor: "@owner", LabeledAt: time.Now()}
		}, wantErr: "override"},
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

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema))
	if err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	const url = "https://github.com/svallejo-dev/aval/schema/bundle.v1.json"
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

func validateJSON(sch *jsonschema.Schema, raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse instance: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return nil
}
