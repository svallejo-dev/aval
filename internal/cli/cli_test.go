package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"go.uber.org/goleak"

	"github.com/svallejo-dev/aval/internal/envelope"
	"github.com/svallejo-dev/aval/internal/ui"
)

// TestMain checks that no test of the command layer leaves a goroutine behind:
// the fake GitHub API of the gate's tests runs a server and an HTTP client, and
// a connection nobody closed would leak one goroutine per case.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

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
		{name: "json=1", args: []string{"version", "--json=1"}, wantCode: ExitOK, wantStdout: `"command": "version"`},
		{name: "json=true", args: []string{"version", "--json=true"}, wantCode: ExitOK, wantStdout: `"command": "version"`},
		{name: "json=false is plain", args: []string{"version", "--json=false"}, wantCode: ExitOK, wantStdout: "aval "},
		{name: "json=1 before a bad flag", args: []string{"version", "--json=1", "--nope"}, wantCode: ExitUsage, wantStdout: `"code": "usage"`},
		{name: "json=0 before a bad flag", args: []string{"version", "--json=0", "--nope"}, wantCode: ExitUsage, wantStderr: "aval: unknown flag: --nope"},
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
	if got.SchemaVersion != envelope.SchemaVersion || got.Command != "version" || !got.OK {
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
	SchemaVersion int              `json:"schemaVersion"`
	Command       string           `json:"command"`
	OK            bool             `json:"ok"`
	Data          json.RawMessage  `json:"data"`
	Errors        []envelope.Issue `json:"errors"`
}

func decodeEnvelope(t *testing.T, raw []byte) decodedEnvelope {
	t.Helper()
	var e decodedEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("not a JSON envelope: %v\n%s", err, raw)
	}
	return e
}

func TestBoolFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want bool
	}{
		{args: nil, want: false},
		{args: []string{"--json"}, want: true},
		{args: []string{"--json=1"}, want: true},
		{args: []string{"--json=t"}, want: true},
		{args: []string{"--json=TRUE"}, want: true},
		{args: []string{"--json=0"}, want: false},
		{args: []string{"--json=false"}, want: false},
		{args: []string{"--json=maybe"}, want: false},
		{args: []string{"--json", "--json=false"}, want: false},
		{args: []string{"--json=false", "--json"}, want: true},
		{args: []string{"--jsonx"}, want: false},
		{args: []string{"-json"}, want: false},
		{args: []string{"--", "--json"}, want: false},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			if got := boolFlag(tt.args, "json"); got != tt.want {
				t.Errorf("boolFlag(%q, json) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestErrorSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		terminal bool // stderr is a terminal
		want     ui.Mode
		color    bool
	}{
		{name: "stderr not a terminal", args: []string{"version", "--nope"}, want: ui.ModePlain},
		{name: "stderr on a terminal", args: []string{"version", "--nope"}, terminal: true, want: ui.ModeTUI, color: true},
		{name: "NO_COLOR", args: []string{"version", "--nope"}, env: map[string]string{"NO_COLOR": "1"}, terminal: true, want: ui.ModeTUI},
		{name: "CI", args: []string{"version", "--nope"}, env: map[string]string{"CI": "true"}, terminal: true, want: ui.ModePlain},
		{name: "json", args: []string{"version", "--json", "--nope"}, terminal: true, want: ui.ModeJSON},
		{name: "json=1", args: []string{"version", "--json=1", "--nope"}, want: ui.ModeJSON},
		{name: "json=false", args: []string{"version", "--json=false", "--nope"}, terminal: true, want: ui.ModeTUI, color: true},
		{name: "plain", args: []string{"version", "--plain", "--nope"}, terminal: true, want: ui.ModePlain},
		{name: "plain=true", args: []string{"version", "--plain=true", "--nope"}, terminal: true, want: ui.ModePlain},
		{name: "plain=0", args: []string{"version", "--plain=0", "--nope"}, terminal: true, want: ui.ModeTUI, color: true},
		{name: "json wins over plain", args: []string{"version", "--plain", "--json"}, terminal: true, want: ui.ModeJSON},
		{name: "flags after -- are arguments", args: []string{"version", "--", "--json", "--plain"}, terminal: true, want: ui.ModeTUI, color: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := errorSettings(tt.args, tt.terminal, func(key string) string { return tt.env[key] })
			if got.Mode != tt.want || got.Color != tt.color || got.Prompt {
				t.Errorf("errorSettings(%q, %v) = %+v, want mode %v, color %v and never a prompt", tt.args, tt.terminal, got, tt.want, tt.color)
			}
		})
	}
}
