package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// envelope is the JSON contract every command emits with --json, on success
// and on failure. errors is always an array, never null.
type envelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Command       string          `json:"command"`
	OK            bool            `json:"ok"`
	Data          any             `json:"data"`
	Errors        []envelopeError `json:"errors"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// errorCodes names each exit code in the envelope.
var errorCodes = map[int]string{
	ExitFailed: "failed",
	ExitUsage:  "usage",
	ExitTool:   "tool",
}

// writeEnvelope writes a successful envelope carrying data.
func writeEnvelope(w io.Writer, command string, data any) error {
	return encodeEnvelope(w, envelope{
		SchemaVersion: 1, Command: command, OK: true, Data: data, Errors: []envelopeError{},
	})
}

// writeErrorEnvelope writes a failed envelope to stdout. If that fails there
// is no JSON channel left, so it falls back to plain text on stderr.
func writeErrorEnvelope(stdout, stderr io.Writer, command string, exitCode int, cause error) {
	code, ok := errorCodes[exitCode]
	if !ok {
		code = "unknown"
	}
	e := envelope{
		SchemaVersion: 1, Command: command, OK: false, Data: nil,
		Errors: []envelopeError{{Code: code, Message: cause.Error()}},
	}
	if err := encodeEnvelope(stdout, e); err != nil {
		fmt.Fprintln(stderr, "aval:", cause)
	}
}

func encodeEnvelope(w io.Writer, e envelope) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(e); err != nil {
		return fmt.Errorf("encode %s envelope: %w", e.Command, err)
	}
	return nil
}
