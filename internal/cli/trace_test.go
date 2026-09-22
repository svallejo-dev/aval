package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const refundTest = `package refund

import "testing"

func TestRefund(t *testing.T) {
	t.Run("ORD-F01 refunds once", func(t *testing.T) {})
}
`

func TestTrace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		files      map[string]string // repository content; nil means no openspec/
		args       []string
		wantCode   int
		wantStdout []string // substrings
		wantStderr string   // substring; "" means stderr must be empty
	}{
		{
			name:       "unverified obligation and open question",
			files:      map[string]string{"openspec/specs/refunds/spec.md": spec("ORD-F02 Refund is logged", "ORD-O01 Who approves")},
			args:       []string{"--plain"},
			wantCode:   ExitFailed,
			wantStdout: []string{"Orphans\nunverified  ORD-F02  openspec/specs/refunds/spec.md:5  no test declares it", "Blocking\nORD-O01"},
			wantStderr: "aval: trace failed: 1 unverified obligation, 1 open question\n",
		},
		{
			name:       "a spec warning does not fail",
			files:      map[string]string{"openspec/specs/refunds/spec.md": spec("ORD-F01 Refund is idempotent across every retry of a client"), "refund/refund_test.go": refundTest},
			args:       []string{"--plain"},
			wantStdout: []string{"warn  openspec/specs/refunds/spec.md:5  name-length", "✓ 1 obligation traced"},
		},
		{
			name:       "no openspec directory",
			args:       []string{"--plain"},
			wantCode:   ExitUsage,
			wantStderr: "has no openspec/ directory",
		},
		{
			name: "invalid change manifest is usage",
			files: map[string]string{
				"openspec/specs/refunds/spec.md":        spec("ORD-F01 a"),
				"openspec/changes/add-limits/aval.yaml": "version: 9\n",
			},
			args:       []string{"--plain"},
			wantCode:   ExitUsage,
			wantStderr: "load specs:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), append([]string{"trace", "--dir", dir}, tt.args...), &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.wantCode, stdout.String(), stderr.String())
			}
			for _, want := range tt.wantStdout {
				assertOutput(t, "stdout", stdout.String(), want)
			}
			assertOutput(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

// TestTraceWithoutGo checks that --run-tests without a go command reports a
// missing tool. It is not parallel: it empties PATH.
func TestTraceWithoutGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"openspec/specs/refunds/spec.md": spec("ORD-F01 a")})
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"trace", "--run-tests", "--plain", "--dir", dir}, &stdout, &stderr); code != ExitTool {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, ExitTool, stderr.String())
	}
	assertOutput(t, "stderr", stderr.String(), "aval: run tests:")
}

func TestFindRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"openspec/specs/x/spec.md": "", "a/b/c.go": ""})
	for _, start := range []string{root, filepath.Join(root, "a", "b")} {
		if got, err := findRoot(start); err != nil || got != root {
			t.Errorf("findRoot(%s) = %q, %v; want %q", start, got, err, root)
		}
	}
	if got, err := findRoot(filepath.Join(root, "openspec", "specs")); err != nil || got != root {
		t.Errorf("findRoot inside openspec/ = %q, %v; want %q", got, err, root)
	}
	if _, err := findRoot(t.TempDir()); err == nil || !strings.Contains(err.Error(), "pass --dir") {
		t.Errorf("findRoot without openspec/ = %v, want an error that suggests --dir", err)
	}
}

// spec is a main spec whose requirements are names, each with one scenario;
// the n-th requirement header is on line 5+7n.
func spec(names ...string) string {
	var b strings.Builder
	b.WriteString("# refunds Specification\n\n## Requirements\n\n")
	for _, n := range names {
		b.WriteString("### Requirement: " + n + "\nThe system SHALL do it.\n\n#### Scenario: it works\n- **WHEN** asked\n- **THEN** it does\n\n")
	}
	return b.String()
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
