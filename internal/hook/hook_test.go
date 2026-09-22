package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stdin string
		want        input
		wantErr     bool
	}{
		{"claude post-tool-use", `{"session_id":"s","hook_event_name":"PostToolUse","cwd":"/r","tool_name":"Edit","tool_input":{"file_path":"/r/a_test.go","old_string":"x"},"tool_response":{"success":true}}`,
			input{Cwd: "/r", Tool: "Edit", FilePath: "/r/a_test.go"}, false},
		{"claude stop", `{"hook_event_name":"Stop","cwd":"/r","stop_hook_active":true}`, input{Cwd: "/r", StopHookActive: true}, false},
		{"claude tool without a file", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, input{Tool: "Bash"}, false},
		{"cursor stop", `{"hook_event_name":"stop","status":"completed","loop_count":2,"workspace_roots":["/r","/o"]}`, input{Cwd: "/r", LoopCount: 2}, false},
		{"cursor afterFileEdit", `{"hook_event_name":"afterFileEdit","file_path":"/r/a_test.go","edits":[]}`, input{FilePath: "/r/a_test.go"}, false},
		{"copilot toolArgs string", `{"cwd":"/r","toolName":"edit","toolArgs":"{\"path\":\"/r/a_test.go\"}"}`, input{Cwd: "/r", Tool: "edit", FilePath: "/r/a_test.go"}, false},
		{"copilot toolArgs object", `{"toolName":"create","toolArgs":{"filePath":"a_test.go"}}`, input{Tool: "create", FilePath: "a_test.go"}, false},
		{"arguments not an object", `{"tool_name":"Edit","tool_input":"x","toolArgs":[1]}`, input{Tool: "Edit"}, false},
		{"empty object", `{}`, input{}, false},
		{"garbage", `not json`, input{}, true},
		{"empty", ``, input{}, true},
		{"array", `[1]`, input{}, true},
		{"stop_hook_active string", `{"stop_hook_active":"true"}`, input{}, true},
		{"loop_count string", `{"loop_count":"1"}`, input{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readInput(strings.NewReader(tt.stdin))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("readInput() = %+v, %v; want %+v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestRunFailsOpen(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	for _, e := range []Event{PostToolUse, Stop, Event("unknown")} {
		for _, stdin := range []string{"{", "", `{"cwd":` + quote(outside) + `,"tool_name":"Write","tool_input":{"file_path":"x_test.go"}}`} {
			var out bytes.Buffer
			if Run(context.Background(), e, strings.NewReader(stdin), &out); out.Len() > 0 {
				t.Errorf("Run(%s, %q) wrote %q, want nothing", e, stdin, out.String())
			}
		}
	}
	var out bytes.Buffer
	if Run(context.Background(), Stop, iotest.ErrReader(errors.New("boom")), &out); out.Len() > 0 {
		t.Errorf("Run() on a failing stdin wrote %q, want nothing", out.String())
	}
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // unreachable: every string encodes
	}
	return string(b)
}

// newRepo returns a git repository in a temporary directory with files,
// given by slash path, committed.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	gitT(t, dir, "init", "-q")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// gitT runs git in dir with an identity and without signing, whatever the
// user's configuration says.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	args = append([]string{"-C", dir, "-c", "user.name=aval", "-c", "user.email=aval@example.com", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil { //nolint:gosec // git with the test's own arguments
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
