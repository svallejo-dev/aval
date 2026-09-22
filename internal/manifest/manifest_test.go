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

func TestParseRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		yaml        string
		wantGoErr   string // substring; "" means valid for Go
		schemaValid bool   // what the JSON Schema should say
	}{
		{name: "valid", yaml: validRepo, schemaValid: true},
		{name: "unknown field", yaml: validRepo + "strict: false\n", wantGoErr: "field strict not found", schemaValid: false},
		{name: "wrong version", yaml: strings.Replace(validRepo, "version: 1\n", "version: 2\n", 1), wantGoErr: "version: got 2", schemaValid: false},
		{name: "lower-case context", yaml: strings.Replace(validRepo, "context: ORD", "context: ord", 1), wantGoErr: "context:", schemaValid: false},
		{name: "unknown mode", yaml: strings.Replace(validRepo, "mode: observe", "mode: audit", 1), wantGoErr: "mode:", schemaValid: false},
		{name: "tier out of range", yaml: strings.Replace(validRepo, "tierDefault: 1", "tierDefault: 4", 1), wantGoErr: "tierDefault:", schemaValid: false},
		{name: "loose openspec version", yaml: strings.Replace(validRepo, "version: 1.13.1", "version: ^1.13", 1), wantGoErr: "openspec.version:", schemaValid: false},
		{name: "missing feat paths", yaml: strings.Replace(validRepo, `  feat: ["internal/**", "api/**", "openspec/**"]`+"\n", "", 1), wantGoErr: "paths.feat:", schemaValid: false},
		// Rules only Go can check: glob syntax and a pattern shared by two families.
		{name: "invalid glob", yaml: strings.Replace(validRepo, `"cmd/**"`, `"cmd/[**"`, 1), wantGoErr: "not a valid glob", schemaValid: true},
		{name: "pattern in two families", yaml: strings.Replace(validRepo, `"api/**"`, `"Makefile"`, 1), wantGoErr: "appears in both dx and feat", schemaValid: true},
		{name: "two documents", yaml: validRepo + "---\n" + validRepo, wantGoErr: "exactly one YAML document", schemaValid: true},
		{name: "empty", yaml: "  \n", wantGoErr: "empty document", schemaValid: false},
	}
	sch := compileSchema(t, "aval.v1.json")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := ParseRepo(strings.NewReader(tt.yaml))
			switch {
			case tt.wantGoErr == "" && err != nil:
				t.Fatalf("ParseRepo: unexpected error: %v", err)
			case tt.wantGoErr != "" && err == nil:
				t.Fatalf("ParseRepo: got %+v, want error containing %q", m, tt.wantGoErr)
			case tt.wantGoErr != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.wantGoErr)):
				t.Fatalf("ParseRepo error = %v, want ErrInvalid containing %q", err, tt.wantGoErr)
			}
			if got := schemaAccepts(t, sch, tt.yaml); got != tt.schemaValid {
				t.Errorf("JSON Schema valid = %v, want %v", got, tt.schemaValid)
			}
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

	tests := []struct {
		name        string
		yaml        string
		wantErr     bool
		schemaValid bool
	}{
		{name: "tier 2 with owner", yaml: "version: 1\ntier: 2\nowner: \"@team-orders\"\n", schemaValid: true},
		{name: "tier 0 without owner", yaml: "version: 1\ntier: 0\n", schemaValid: true},
		{name: "tier out of range", yaml: "version: 1\ntier: 5\n", wantErr: true},
		{name: "unknown field", yaml: "version: 1\ntier: 1\nrisk: high\n", wantErr: true},
		{name: "missing version", yaml: "tier: 1\n", wantErr: true},
	}
	sch := compileSchema(t, "change.v1.json")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseChange(strings.NewReader(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseChange error = %v, wantErr %v", err, tt.wantErr)
			}
			if got := schemaAccepts(t, sch, tt.yaml); got != tt.schemaValid {
				t.Errorf("JSON Schema valid = %v, want %v", got, tt.schemaValid)
			}
		})
	}
}

func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	raw, err := Schemas.ReadFile("schema/" + name)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("schema %s is not valid JSON: %v", name, err)
	}
	url := "https://github.com/svallejo-dev/aval/schema/" + name
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatalf("compile schema %s: %v", name, err)
	}
	return sch
}

// schemaAccepts reports whether the first YAML document validates against sch.
func schemaAccepts(t *testing.T, sch *jsonschema.Schema, doc string) bool {
	t.Helper()
	var v any
	if err := yaml.NewDecoder(strings.NewReader(doc)).Decode(&v); err != nil {
		return false // not even YAML; empty documents land here too
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
