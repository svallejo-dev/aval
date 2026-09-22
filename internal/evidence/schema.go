package evidence

import _ "embed"

// Schema is the JSON Schema of bundle version 1 (schema/bundle.v1.json).
//
//go:embed schema/bundle.v1.json
var Schema []byte
