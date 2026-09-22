package evidence

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema is the JSON Schema of bundle version 1 (schema/bundle.v1.json).
//
//go:embed schema/bundle.v1.json
var Schema []byte

const schemaURL = "https://github.com/svallejo-dev/aval/schema/bundle.v1.json"

// compiledSchema compiles Schema once. A broken embedded schema is a build
// defect, so the error surfaces on every validation instead of panicking.
var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema))
	if err != nil {
		return nil, fmt.Errorf("parse embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("load embedded schema: %w", err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile embedded schema: %w", err)
	}
	return sch, nil
})

// validateSchema marshals b exactly as it would be written and validates it,
// so the Go types can never drift from what the schema accepts.
func validateSchema(b Bundle) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal bundle: %w", err)
	}
	return validateJSON(raw)
}

// validateJSON validates raw bundle JSON against the schema.
func validateJSON(raw []byte) error {
	sch, err := compiledSchema()
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse bundle JSON: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return nil
}
