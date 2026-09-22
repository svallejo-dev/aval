package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const validRepo = `version: 1
context: ORD
mode: observe
tierDefault: 1
openspec:
  version: 1.13.1
paths:
  dx: ["Makefile", ".github/**", "cmd/**", "internal/platform/**"]
  feat: ["internal/**", "api/**", "openspec/**"]
  seam: ["internal/app/app.go"]
`

// parityCase is one manifest document. valid is what Parse must say; goOnly
// marks documents the JSON Schema accepts but a Go-only rule rejects. Every
// other document must get the same answer from Parse and from the schema.
type parityCase struct {
	name       string
	yaml       string
	valid      bool
	goOnly     bool
	wantErr    string // substring required in Go-only errors
	skipSchema bool   // input the schema check cannot even load (oversized)
}

func repoWith(old, repl string) string { return strings.Replace(validRepo, old, repl, 1) }

func TestParseRepo(t *testing.T) {
	t.Parallel()

	tests := []parityCase{
		{name: "valid", yaml: validRepo, valid: true},
		{name: "seam omitted", yaml: repoWith(`  seam: ["internal/app/app.go"]`+"\n", ""), valid: true},
		{name: "unknown field", yaml: validRepo + "strict: false\n"},
		{name: "wrong version", yaml: repoWith("version: 1\n", "version: 2\n")},
		{name: "version as string", yaml: repoWith("version: 1\n", "version: \"1\"\n")},
		{name: "version as float", yaml: repoWith("version: 1\n", "version: 1.9\n")},
		{name: "lower-case context", yaml: repoWith("context: ORD", "context: ord")},
		{name: "unknown mode", yaml: repoWith("mode: observe", "mode: audit")},
		{name: "tier out of range", yaml: repoWith("tierDefault: 1", "tierDefault: 4")},
		{name: "tier missing", yaml: repoWith("tierDefault: 1\n", "")},
		{name: "tier null", yaml: repoWith("tierDefault: 1", "tierDefault: null")},
		{name: "tier as float", yaml: repoWith("tierDefault: 1", "tierDefault: 0.99")},
		{name: "loose openspec version", yaml: repoWith("version: 1.13.1", "version: ^1.13")},
		{name: "missing feat paths", yaml: repoWith(`  feat: ["internal/**", "api/**", "openspec/**"]`+"\n", "")},
		{name: "empty dx list", yaml: repoWith(`["Makefile", ".github/**", "cmd/**", "internal/platform/**"]`, "[]")},
		{name: "empty glob", yaml: repoWith(`"Makefile"`, `""`)},
		{name: "numeric glob", yaml: repoWith(`"Makefile"`, `123`)},
		{name: "seam null", yaml: repoWith(`seam: ["internal/app/app.go"]`, "seam: null")},
		{name: "invalid glob", yaml: repoWith(`"cmd/**"`, `"cmd/[**"`), goOnly: true, wantErr: "not a valid glob"},
		{name: "pattern in two families", yaml: repoWith(`"api/**"`, `"Makefile"`), goOnly: true, wantErr: "appears in both dx and feat"},
		{name: "pattern repeated in a family", yaml: repoWith(`"cmd/**"`, `"Makefile"`), goOnly: true, wantErr: "paths.dx: \"Makefile\" is repeated"},
		{name: "two documents", yaml: validRepo + "---\n" + validRepo, goOnly: true, wantErr: "exactly one YAML document"},
		{name: "blank", yaml: "  \n", wantErr: "empty document"},
		{name: "comments only", yaml: "# nothing here\n", wantErr: "empty document"},
		{name: "BOM only", yaml: "\xef\xbb\xbf", wantErr: "empty document"},
		{name: "oversized", yaml: validRepo + "#" + strings.Repeat("x", maxManifestBytes), wantErr: "larger than", skipSchema: true},
	}
	sch := compileSchema(t, "aval.v1.json")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRepo(strings.NewReader(tt.yaml))
			assertParity(t, tt, err, sch)
		})
	}
}

func TestParseRepoFields(t *testing.T) {
	t.Parallel()

	m, err := ParseRepo(strings.NewReader(validRepo))
	if err != nil {
		t.Fatal(err)
	}
	if m.Context != "ORD" || m.Mode != Observe || m.TierDefault != 1 || m.OpenSpec.Version != "1.13.1" {
		t.Errorf("unexpected manifest: %+v", m)
	}
	if len(m.Paths.DX) != 4 || len(m.Paths.Feat) != 3 || len(m.Paths.Seam) != 1 {
		t.Errorf("unexpected paths: %+v", m.Paths)
	}
}

func TestParseChange(t *testing.T) {
	t.Parallel()

	tests := []parityCase{
		{name: "tier 2 with owner", yaml: "version: 1\ntier: 2\nowner: \"@team-orders\"\n", valid: true},
		{name: "tier 0 without owner", yaml: "version: 1\ntier: 0\n", valid: true},
		{name: "tier out of range", yaml: "version: 1\ntier: 5\n"},
		{name: "tier missing", yaml: "version: 1\n"},
		{name: "tier null", yaml: "version: 1\ntier: null\n"},
		{name: "tier as float", yaml: "version: 1\ntier: 3.7\n"},
		{name: "unknown field", yaml: "version: 1\ntier: 1\nrisk: high\n"},
		{name: "missing version", yaml: "tier: 1\n"},
		{name: "empty owner", yaml: "version: 1\ntier: 1\nowner: \"\"\n"},
		{name: "null owner", yaml: "version: 1\ntier: 1\nowner: null\n"},
		{name: "numeric owner", yaml: "version: 1\ntier: 1\nowner: 42\n"},
	}
	sch := compileSchema(t, "change.v1.json")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseChange(strings.NewReader(tt.yaml))
			assertParity(t, tt, err, sch)
		})
	}
}

// assertParity checks Parse's answer and that the schema file, validated
// independently, agrees with it except for Go-only rules.
func assertParity(t *testing.T, tt parityCase, err error, sch *jsonschema.Schema) {
	t.Helper()
	switch {
	case tt.valid && err != nil:
		t.Fatalf("Parse: unexpected error: %v", err)
	case !tt.valid && err == nil:
		t.Fatal("Parse: accepted an invalid document")
	case !tt.valid && !errors.Is(err, ErrInvalid):
		t.Fatalf("Parse error = %v, want it to wrap ErrInvalid", err)
	case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
		t.Fatalf("Parse error = %v, want it to contain %q", err, tt.wantErr)
	}
	if tt.skipSchema {
		return
	}
	wantSchema := tt.valid || tt.goOnly
	if got := schemaAccepts(t, sch, tt.yaml); got != wantSchema {
		t.Errorf("JSON Schema accepts = %v, want %v", got, wantSchema)
	}
}

func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	sch, err := compile(name)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// schemaAccepts reports whether the first YAML document validates against sch.
func schemaAccepts(t *testing.T, sch *jsonschema.Schema, doc string) bool {
	t.Helper()
	var v any
	if err := yaml.NewDecoder(strings.NewReader(doc)).Decode(&v); err != nil || v == nil {
		return false // not YAML, or an empty document
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("yaml to json: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal instance: %v", err)
	}
	return sch.Validate(inst) == nil
}
