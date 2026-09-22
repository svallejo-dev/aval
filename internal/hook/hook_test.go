package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestReadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stdin string
		want        input
		wantErr     bool
	}{
		{"post-tool-use", `{"session_id":"s","hook_event_name":"PostToolUse","cwd":"/r","tool_name":"Edit","tool_input":{"file_path":"/r/a_test.go","old_string":"x"},"tool_response":{"success":true}}`,
			input{Cwd: "/r", ToolName: "Edit", ToolInput: toolInput{FilePath: "/r/a_test.go"}}, false},
		{"stop", `{"hook_event_name":"Stop","cwd":"/r","stop_hook_active":true}`, input{Cwd: "/r", StopHookActive: true}, false},
		{"tool without a file", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, input{ToolName: "Bash"}, false},
		{"cursor stop", `{"hook_event_name":"stop","status":"completed","loop_count":2,"workspace_roots":["/r"]}`, input{}, false},
		{"copilot", `{"cwd":"/r","toolName":"edit","toolArgs":"{\"path\":\"/r/a_test.go\"}"}`, input{Cwd: "/r"}, false},
		{"garbage", `not json`, input{}, true},
		{"empty", ``, input{}, true},
		{"array", `[1]`, input{}, true},
		{"tool_input a string", `{"tool_name":"Edit","tool_input":"x"}`, input{}, true},
		{"stop_hook_active a string", `{"stop_hook_active":"true"}`, input{}, true},
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

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom") }

func TestRunFailsOpen(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	stdins := []io.Reader{panicReader{}, iotest.ErrReader(errors.New("boom"))}
	for _, s := range []string{"{", "", `{"cwd":` + quote(outside) + `,"tool_name":"Write","tool_input":{"file_path":"x_test.go"}}`} {
		stdins = append(stdins, strings.NewReader(s))
	}
	for _, e := range []Event{PostToolUse, Stop, Event("unknown")} {
		for _, stdin := range stdins {
			var out bytes.Buffer
			if Run(context.Background(), e, stdin, &out); out.Len() > 0 {
				t.Errorf("Run(%s, %v) wrote %q, want nothing", e, stdin, out.String())
			}
		}
	}
}

// TestRespondDeadline checks that a hook fails open at its deadline, here
// on a stdin that never ends.
func TestRespondDeadline(t *testing.T) {
	t.Parallel()
	stdin, w := io.Pipe()
	defer w.Close() // ends the read, and with it the goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if r := respond(ctx, Stop, stdin); r != nil || time.Since(start) > time.Second {
		t.Errorf("respond() = %+v after %v, want nil at the deadline", r, time.Since(start))
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
