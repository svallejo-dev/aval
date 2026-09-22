package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // substring; "" means stdout must be empty
		wantStderr string // substring; "" means stderr must be empty
		anyStdout  bool   // skip the stdout check (help text)
	}{
		{name: "version plain", args: []string{"version", "--plain"}, wantCode: ExitOK, wantStdout: "aval "},
		{name: "version default", args: []string{"version"}, wantCode: ExitOK, wantStdout: "aval "},
		{name: "no command prints help", args: nil, wantCode: ExitOK, anyStdout: true},
		{name: "help flag", args: []string{"--help"}, wantCode: ExitOK, anyStdout: true},
		{name: "unknown flag", args: []string{"version", "--nope"}, wantCode: ExitUsage, wantStderr: "unknown flag: --nope"},
		{name: "unknown command", args: []string{"nope"}, wantCode: ExitUsage, wantStderr: `unknown command "nope"`},
		{name: "json and plain conflict", args: []string{"version", "--json", "--plain"}, wantCode: ExitUsage, wantStdout: `"code": "usage"`},
		{name: "extra args", args: []string{"version", "extra"}, wantCode: ExitUsage, wantStderr: `unknown command "extra"`},
		{name: "json without command", args: []string{"--json"}, wantCode: ExitUsage, wantStdout: `"code": "usage"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stdout %q, stderr %q)", code, tt.wantCode, stdout.String(), stderr.String())
			}
			if !tt.anyStdout {
				assertOutput(t, "stdout", stdout.String(), tt.wantStdout)
			}
			assertOutput(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

func assertOutput(t *testing.T, stream, got, want string) {
	t.Helper()
	if want == "" {
		if got != "" {
			t.Errorf("%s = %q, want it empty", stream, got)
		}
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("%s = %q, want it to contain %q", stream, got, want)
	}
}

// TestExitCodeMapping checks how command errors become exit codes, using a
// stub command that returns each kind of error.
func TestExitCodeMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{name: "explicit tool error", err: &ExitError{Code: ExitTool, Err: errors.New("openspec not found")}, wantCode: ExitTool, wantErr: "openspec not found"},
		{name: "plain error is a failure, not usage", err: errors.New("disk full"), wantCode: ExitFailed, wantErr: "disk full"},
		{name: "silent exit error prints nothing", err: &ExitError{Code: ExitFailed}, wantCode: ExitFailed, wantErr: ""},
		{name: "success", err: nil, wantCode: ExitOK, wantErr: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			root := newRootCmd(&stdout, &stderr)
			root.AddCommand(&cobra.Command{
				Use:  "stub",
				RunE: runE(func(*cobra.Command, []string) error { return tt.err }),
			})
			code := execute(context.Background(), root, []string{"stub"}, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}
			assertOutput(t, "stderr", stderr.String(), tt.wantErr)
		})
	}
}

func TestJSONErrorEnvelope(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.AddCommand(&cobra.Command{
		Use: "stub",
		RunE: runE(func(*cobra.Command, []string) error {
			return &ExitError{Code: ExitTool, Err: errors.New("openspec 1.12.0 found, want 1.13.1")}
		}),
	})
	code := execute(context.Background(), root, []string{"stub", "--json"}, &stdout, &stderr)
	if code != ExitTool {
		t.Fatalf("exit code = %d, want %d", code, ExitTool)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty: JSON mode reports errors in the envelope", stderr.String())
	}
	got := decodeEnvelope(t, stdout.Bytes())
	if got.OK || got.Command != "stub" || len(got.Errors) != 1 || got.Errors[0].Code != "tool" {
		t.Errorf("envelope = %+v, want ok=false, command=stub, one error with code tool", got)
	}
}

func TestVersionJSONEnvelope(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"version", "--json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	got := decodeEnvelope(t, stdout.Bytes())
	if got.SchemaVersion != 1 || got.Command != "version" || !got.OK {
		t.Errorf("envelope = %+v, want schemaVersion 1, command version, ok true", got)
	}
	var data versionInfo
	if err := json.Unmarshal(got.Data, &data); err != nil || data.Version == "" || data.Go == "" {
		t.Errorf("data = %s (err %v), want version and go set", got.Data, err)
	}
	if got.Errors == nil || len(got.Errors) != 0 {
		t.Errorf("errors = %v, want an empty array, never null", got.Errors)
	}
}

type decodedEnvelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Command       string          `json:"command"`
	OK            bool            `json:"ok"`
	Data          json.RawMessage `json:"data"`
	Errors        []envelopeError `json:"errors"`
}

func decodeEnvelope(t *testing.T, raw []byte) decodedEnvelope {
	t.Helper()
	var e decodedEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("not a JSON envelope: %v\n%s", err, raw)
	}
	return e
}
