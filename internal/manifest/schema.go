package manifest

import "embed"

// Schemas holds the JSON Schemas of both manifests (schema/aval.v1.json and
// schema/change.v1.json). Editors use them through yaml-language-server, and
// tests keep them in sync with the Go validation.
//
//go:embed schema/*.json
var Schemas embed.FS
