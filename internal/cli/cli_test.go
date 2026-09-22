package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version plain", args: []string{"version", "--plain"}, wantCode: ExitOK, wantStdout: "aval "},
		{name: "version default", args: []string{"version"}, wantCode: ExitOK, wantStdout: "aval "},
		{name: "unknown flag is usage", args: []string{"version", "--nope"}, wantCode: ExitUsage, wantStderr: "unknown flag"},
		{name: "unknown command is usage", args: []string{"nope"}, wantCode: ExitUsage, wantStderr: "unknown command"},
		{name: "json and plain conflict", args: []string{"version", "--json", "--plain"}, wantCode: ExitUsage, wantStderr: "aval:"},
		{name: "extra args are usage", args: []string{"version", "extra"}, wantCode: ExitUsage, wantStderr: "aval:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestVersionJSONEnvelope(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"version", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}

	var got struct {
		SchemaVersion int             `json:"schemaVersion"`
		Command       string          `json:"command"`
		OK            bool            `json:"ok"`
		Data          versionInfo     `json:"data"`
		Errors        json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != 1 || got.Command != "version" || !got.OK {
		t.Errorf("envelope = %+v, want schemaVersion 1, command version, ok true", got)
	}
	if got.Data.Version == "" || got.Data.Go == "" {
		t.Errorf("data = %+v, want version and go set", got.Data)
	}
	if string(got.Errors) != "[]" {
		t.Errorf("errors = %s, want an empty array, never null", got.Errors)
	}
}
