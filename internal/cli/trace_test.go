package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/trace"
	"github.com/svallejo-dev/aval/internal/ui"
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
			name:  "a spec warning does not fail, and untested rows are not traced",
			files: map[string]string{"openspec/specs/refunds/spec.md": spec("ORD-F01 Refund is idempotent across every retry of a client", "ORD-S01 Fast answer"), "refund/refund_test.go": refundTest},
			args:  []string{"--plain"},
			wantStdout: []string{
				"warn  openspec/specs/refunds/spec.md:5  name-length",
				"ORD-S01  S     untested  Fast answer ",
				"✓ 1 obligation traced, 1 untested\n",
			},
		},
		{
			name: "title and characterization",
			files: map[string]string{
				"openspec/specs/refunds/spec.md": strings.Replace(spec("ORD-F01 Refund is idempotent"), "The system", "**aval**: characterization\nThe system", 1),
				"refund/refund_test.go":          refundTest,
			},
			args:       []string{"--plain"},
			wantStdout: []string{"ID       KIND  STATUS  TITLE                                    SPEC", "ORD-F01  F     traced  Refund is idempotent (characterization)  openspec/"},
		},
		{
			name:       "no openspec directory",
			args:       []string{"--plain"},
			wantCode:   ExitUsage,
			wantStderr: "has no openspec/specs/ or openspec/changes/ directory",
		},
		{
			name:       "a Go package named openspec is not an OpenSpec tree",
			files:      map[string]string{"openspec/openspec.go": "package openspec\n"},
			args:       []string{"--plain"},
			wantCode:   ExitUsage,
			wantStderr: "has no openspec/specs/ or openspec/changes/ directory",
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

// TestTraceFailures pins every reason to fail, each one alone.
func TestTraceFailures(t *testing.T) {
	t.Parallel()
	untested := trace.Row{ID: "ORD-S01", Kind: "S", Status: trace.Untested, Runtime: evidence.Pass}
	tests := []struct {
		name string
		data traceData
		want []string
	}{
		{name: "clean", data: traceData{Matrix: trace.Matrix{Rows: []trace.Row{untested}}}},
		{name: "spec warning", data: traceData{Findings: []specFinding{{Severity: openspec.SeverityWarn}}}},
		{name: "spec error", data: traceData{Findings: []specFinding{{Severity: openspec.SeverityError}}}, want: []string{"1 spec error"}},
		{
			name: "undeclared runtime binding of an S obligation",
			data: traceData{Matrix: trace.Matrix{Rows: []trace.Row{untested}, Orphans: []trace.Orphan{{Kind: trace.OrphanRuntime, ID: "ORD-S01"}}}},
			want: []string{"1 undeclared runtime binding"},
		},
		{
			name: "a failing test",
			data: traceData{Matrix: trace.Matrix{Rows: []trace.Row{{ID: "ORD-F01", Runtime: evidence.Pass, Tests: []trace.Test{{Runtime: evidence.Pass}, {Runtime: evidence.Fail}}}}}},
			want: []string{"1 failing obligation"},
		},
		{
			name: "a failing obligation",
			data: traceData{Matrix: trace.Matrix{Rows: []trace.Row{{ID: "ORD-F01", Runtime: evidence.BuildFail}}}},
			want: []string{"1 failing obligation"},
		},
		{
			name: "packages that did not build",
			data: traceData{Matrix: trace.Matrix{BuildFailures: []trace.BuildFailure{{Package: "./..."}, {Package: "x"}}}},
			want: []string{"2 package build failures"},
		},
		{
			name: "orphans and open questions",
			data: traceData{Matrix: trace.Matrix{Orphans: []trace.Orphan{{Kind: trace.OrphanUnverified}, {Kind: trace.OrphanTest}}, Blocking: []string{"ORD-O01"}}},
			want: []string{"1 unverified obligation", "1 orphan test", "1 open question"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := traceFailures(tt.data); !slices.Equal(got, tt.want) {
				t.Errorf("traceFailures = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSettings checks that stdout and stderr each follow their own terminal:
// `aval trace 2>err.log` at a terminal styles the table, never the log.
func TestSettings(t *testing.T) {
	t.Parallel()
	g := globalFlags{}
	noEnv := func(string) string { return "" }
	out, errs := g.settings(noEnv, true, false, true)
	if out.Mode != ui.ModeTUI || !out.Color || errs.Mode != ui.ModePlain || errs.Color || errs.Prompt {
		t.Errorf("stdout at a terminal, stderr to a file: %+v, %+v; want tui with color, then plain", out, errs)
	}
	out, errs = g.settings(noEnv, false, true, true)
	if out.Mode != ui.ModePlain || errs.Mode != ui.ModeTUI || !errs.Color || errs.Prompt {
		t.Errorf("stdout to a pipe, stderr at a terminal: %+v, %+v; want plain, then tui with color and no prompt", out, errs)
	}
}

func TestModulePath(t *testing.T) {
	t.Parallel()
	for gomod, want := range map[string]string{
		"module example.com/shop\n\ngo 1.22\n":  "example.com/shop",
		"// x\nmodule \"example.com/q\" // c\n": "example.com/q",
		"go 1.22\n":                             "",
		"":                                      "",
	} {
		if got := modulePath([]byte(gomod)); got != want {
			t.Errorf("modulePath(%q) = %q, want %q", gomod, got, want)
		}
	}
}

func TestFindRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"openspec/specs/x/spec.md": "", "a/b/c.go": "", "internal/openspec/openspec.go": "package openspec"})
	for _, start := range []string{root, filepath.Join(root, "a", "b"), filepath.Join(root, "internal", "openspec")} {
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
