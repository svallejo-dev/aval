package ui

import (
	"encoding/json"
	"fmt"
	"io"
)

// envelope is the JSON contract every command emits with --json, on success
// and on failure. errors is always an array, never null.
type envelope struct {
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

func writeEnvelope(w io.Writer, e envelope) error {
	e.SchemaVersion = 1
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
