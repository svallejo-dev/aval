package manifest

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schemas holds the JSON Schemas of both manifests (schema/aval.v1.json and
// schema/change.v1.json). Parsing validates every document against them, and
// editors can use them through yaml-language-server.
//
//go:embed schema/*.json
var Schemas embed.FS

const schemaBase = "https://github.com/svallejo-dev/aval/schema/"

var (
	repoSchema   = sync.OnceValues(func() (*jsonschema.Schema, error) { return compile("aval.v1.json") })
	changeSchema = sync.OnceValues(func() (*jsonschema.Schema, error) { return compile("change.v1.json") })
)

// compile loads one embedded schema. A broken embedded schema is a build
// defect, so the error surfaces on every parse instead of panicking.
func compile(name string) (*jsonschema.Schema, error) {
	raw, err := Schemas.ReadFile("schema/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema %s: %w", name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse embedded schema %s: %w", name, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaBase+name, doc); err != nil {
		return nil, fmt.Errorf("load embedded schema %s: %w", name, err)
	}
	sch, err := c.Compile(schemaBase + name)
	if err != nil {
		return nil, fmt.Errorf("compile embedded schema %s: %w", name, err)
	}
	return sch, nil
}

// validateDoc validates a generically decoded YAML document against a schema.
func validateDoc(schema func() (*jsonschema.Schema, error), doc any) error {
	sch, err := schema()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w: document is not representable as JSON: %w", ErrInvalid, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse document as JSON: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}
