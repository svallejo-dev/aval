// Package envelope defines the JSON contract aval emits with --json, on
// success and on failure. It depends only on the standard library, so agent
// hooks can emit it within their latency budget (ADR-0003).
package envelope

import (
	"encoding/json"
	"fmt"
	"io"
)

// SchemaVersion is the version of the contract Write emits.
const SchemaVersion = 1

// Envelope is the JSON document every command emits with --json.
type Envelope struct {
	SchemaVersion int     `json:"schemaVersion"`
	Command       string  `json:"command"`
	OK            bool    `json:"ok"`
	Data          any     `json:"data"`
	Errors        []Issue `json:"errors"`
}

// Issue is a failure as aval reports it: an entry of the envelope's errors in
// json mode, and a line on stderr otherwise.
type Issue struct {
	Code    string `json:"code"` // stable name of the exit code, e.g. "usage"
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"` // what to do about it, if known
}

// Write encodes e to w as indented JSON. It stamps SchemaVersion and turns nil
// errors into an empty array, so errors is never null.
func Write(w io.Writer, e Envelope) error {
	e.SchemaVersion = SchemaVersion
	if e.Errors == nil {
		e.Errors = []Issue{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(e); err != nil {
		return fmt.Errorf("encode %s envelope: %w", e.Command, err)
	}
	return nil
}
