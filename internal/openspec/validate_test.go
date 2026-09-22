package openspec

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRunner stands in for npx and records how it was called.
type fakeRunner struct {
	missing string // the tool LookPath does not find
	out     output
	err     error

	ran      bool
	dir      string
	env      []string
	cmd      []string
	deadline time.Duration // how far away the run's deadline was
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if file == f.missing {
		return "", fmt.Errorf("look up %s: %w", file, exec.ErrNotFound)
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) (output, error) {
	f.ran, f.dir, f.env, f.cmd = true, dir, env, append([]string{name}, args...)
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return f.out, f.err
}

// repoRoot creates a repository with an openspec/ directory and returns its
// path as OpenSpec reports it: absolute and without symlinks.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, Dir), 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidate(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	rooted := func(s string) []byte { return []byte(strings.ReplaceAll(s, "<ROOT>", root)) }
	captured := func(name string) []byte { return rooted(string(fixture(t, name))) }
	report := func(items string) []byte {
		return rooted(`{"version": "1.0", "root": {"path": "<ROOT>", "source": "nearest"}, "items": ` + items + `}`)
	}
	info := func(level, msg string) []byte {
		return report(`[{"id": "c", "type": "change", "valid": true, "issues": [{"level": "` + level + `", "path": "file", "message": "` + msg + `"}]}]`)
	}
	const skipSpecs = "skip_specs is set in .openspec.yaml: change declares no spec-level behavior changes, zero deltas accepted"
	noRoot := `{"status": [{"severity": "error", "code": "no_openspec_root", "message": "No OpenSpec root found from the current directory."}]}`
	withFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(withFile, Dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		root    string // default: root
		version string
		run     fakeRunner
		passed  bool
		is      []error // the error wraps each of these; nil for no error
		msg     string  // the error names this
	}{
		{name: "clean", run: fakeRunner{out: output{stdout: captured("json/validate-spec-after.json")}}, passed: true},
		{name: "INFO fails", run: fakeRunner{out: output{stdout: captured("negative/modified-case-variant/validate.json")}}},
		{name: "ERROR fails", run: fakeRunner{out: output{stdout: captured("negative/modified-drops-scenario/validate.json"), code: 1}}},
		{name: "allowed INFO passes", run: fakeRunner{out: output{stdout: info("INFO", skipSpecs)}}, passed: true},
		{name: "allowed message at another level fails", run: fakeRunner{out: output{stdout: info("WARNING", skipSpecs)}}},
		{name: "allowed message must match exactly", run: fakeRunner{out: output{stdout: info("INFO", skipSpecs+".")}}},
		{name: "node missing", run: fakeRunner{missing: "node"}, is: []error{ErrToolMissing, exec.ErrNotFound}, msg: "look up node"},
		{name: "npx missing", run: fakeRunner{missing: "npx"}, is: []error{ErrToolMissing, exec.ErrNotFound}, msg: "look up npx"},
		{name: "range version", version: "^1.13.1", msg: `version "^1.13.1" must be exact`},
		{name: "tag version", version: "latest", msg: `version "latest" must be exact`},
		{name: "no openspec directory", root: t.TempDir(), is: []error{fs.ErrNotExist}, msg: "has no openspec/ directory"},
		{name: "openspec is a file", root: withFile, msg: "openspec is not a directory"},
		{name: "run fails", run: fakeRunner{err: context.DeadlineExceeded}, is: []error{ErrToolFailed, context.DeadlineExceeded}},
		{
			name: "another root",
			run:  fakeRunner{out: output{stdout: []byte(strings.ReplaceAll(string(fixture(t, "json/validate-spec-after.json")), "<ROOT>", "/parent"))}},
			is:   []error{ErrToolFailed},
			msg:  `OpenSpec validated "/parent" instead of "` + root + `"`,
		},
		{
			name: "no OpenSpec root",
			run:  fakeRunner{out: output{stdout: []byte(noRoot), code: 1}},
			is:   []error{ErrToolFailed},
			msg:  "exit status 1: No OpenSpec root found from the current directory. (no_openspec_root)",
		},
		{
			name: "npx cannot fetch the package",
			run:  fakeRunner{out: output{stderr: []byte("npm error 404 Not Found\n"), code: 1}},
			is:   []error{ErrToolFailed},
			msg:  "decode validate report: unexpected end of JSON input\nnpm error 404 Not Found",
		},
		{name: "unknown report version", run: fakeRunner{out: output{stdout: []byte(`{"version": "2.0", "items": []}`)}}, is: []error{ErrToolFailed}, msg: `version "2.0"`},
		{name: "report without items", run: fakeRunner{out: output{stdout: []byte(`{"version": "1.0"}`)}}, is: []error{ErrToolFailed}},
		{
			name: "failing exit with a passing report",
			run:  fakeRunner{out: output{stdout: report("[]"), code: 1}},
			is:   []error{ErrToolFailed},
			msg:  "exit status 1 with a passing report",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := tt.run
			got, err := validate(context.Background(), &run, cmp.Or(tt.root, root), cmp.Or(tt.version, "1.13.1"))

			for _, target := range tt.is {
				if !errors.Is(err, target) {
					t.Errorf("error %v does not wrap %v", err, target)
				}
			}
			if wantErr := tt.is != nil || tt.msg != ""; (err != nil) != wantErr {
				t.Fatalf("validate() error = %v, want error %v", err, wantErr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), tt.msg) {
					t.Errorf("error %q does not contain %q", err, tt.msg)
				}
				if run.ran != (tt.run.out.stdout != nil || tt.run.out.stderr != nil || tt.run.err != nil) {
					t.Errorf("npx ran = %v", run.ran)
				}
				return
			}
			if got.Passed() != tt.passed || got.Root != root {
				t.Errorf("Passed() = %v, root %q; want %v, %q", got.Passed(), got.Root, tt.passed, root)
			}
			want := []string{"npx", "-y", "@fission-ai/openspec@1.13.1", "validate", "--all", "--strict", "--json"}
			if !slices.Equal(run.cmd, want) || run.dir != root {
				t.Errorf("ran %q in %s, want %q in %s", run.cmd, run.dir, want, root)
			}
			wantEnv := []string{"OPENSPEC_TELEMETRY=0", "OPENSPEC_NO_UPDATE_CHECK=1", "npm_config_registry=https://registry.npmjs.org/"}
			if !slices.Equal(run.env, wantEnv) {
				t.Errorf("env = %q, want %q", run.env, wantEnv)
			}
			if run.deadline <= 0 || run.deadline > runTimeout {
				t.Errorf("deadline in %v, want within %v", run.deadline, runTimeout)
			}
		})
	}
}

// TestReportFromCaptures reads every validate output the spike captured:
// OpenSpec calls some of them valid, and aval fails every one with an issue.
func TestReportFromCaptures(t *testing.T) {
	t.Parallel()

	const delta = "openspec/changes/%s/specs/refunds/spec.md"
	failing := map[string][]string{
		"negative/modified-case-variant": {fmt.Sprintf(delta, "modified-case-variant") +
			`: error: INFO refunds/spec.md: Archive would refuse this delta: refunds MODIFIED failed for header "### Requirement: ORD-F01 refund is idempotent" - not found [openspec]`},
		"negative/non-requirement-h3": {fmt.Sprintf(delta, "non-requirement-h3") +
			`:6: error: INFO refunds/spec.md: Header "### Notes" in ADDED Requirements is not a "### Requirement:" header and is ignored by validation. Use "### Requirement: Notes" if it should be validated as a requirement. [openspec]`},
		"negative/modified-drops-scenario": {fmt.Sprintf(delta, "modified-drops-scenario") +
			`: error: ERROR refunds/spec.md: MODIFIED "ORD-F01 Refund is idempotent" omits scenario(s) the current spec still has: "Different keys for same order", "Key reused after the window". Copy them into the MODIFIED block (a MODIFIED requirement replaces the whole block, so archive refuses to drop them). [openspec]`},
		"negative/modified-old-name-after-rename": {fmt.Sprintf(delta, "modified-old-name-after-rename") +
			`: error: ERROR refunds/spec.md: MODIFIED references old name from RENAMED. Use new header for "ORD-S01 Refund latency budget" [openspec]`},
	}

	var files []string
	for _, pattern := range []string{"*/*/validate.json", "*/*/validate-spec-after.json", "json/validate-*.json", "extra-files/validate*.json"} {
		matches, err := fs.Glob(spike, pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) != 22 {
		t.Errorf("found %d captured reports, want 22: %q", len(files), files)
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, err := parseReport(fixture(t, name))
			if err != nil {
				t.Fatal(err)
			}
			want := failing[strings.TrimSuffix(name, "/validate.json")]
			if r.Passed() != (want == nil) {
				t.Errorf("Passed() = %v, want %v", r.Passed(), want == nil)
			}
			var got []string
			for _, f := range r.Findings() {
				got = append(got, f.String())
			}
			if !slices.Equal(got, want) {
				t.Errorf("findings:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestReportFindings(t *testing.T) {
	t.Parallel()
	r := Report{Items: []Item{
		{ID: "refunds", Type: "spec", Valid: false, Issues: []Issue{{Level: "WARNING", Path: "requirements[0].scenarios", Line: 9, Message: "no scenario"}}},
		{ID: "c1", Type: "change", Valid: false, Issues: []Issue{{Level: "ERROR", Path: "file", Line: 4, Message: "no deltas"}}},
		{ID: "c2", Type: "change", Valid: false},
		{ID: "c3", Type: "change", Valid: true, Issues: []Issue{{Level: "INFO", Path: "file", Message: allowedInfo[0]}}},
	}}
	want := []string{
		"openspec/specs/refunds/spec.md:9: error: WARNING requirements[0].scenarios: no scenario [openspec]",
		"openspec/changes/c1: error: ERROR file: no deltas [openspec]",
		"openspec/changes/c2: error: OpenSpec marks change c2 invalid without reporting an issue [openspec]",
	}
	var got []string
	for _, f := range r.Findings() {
		got = append(got, f.String())
	}
	if !slices.Equal(got, want) || r.Passed() {
		t.Errorf("findings:\n got %q\nwant %q (Passed %v)", got, want, r.Passed())
	}
	if !(Report{Items: []Item{{Valid: true}}}).Passed() || !(Report{}).Passed() || !(Report{Items: r.Items[3:]}).Passed() {
		t.Error("a report with no issues, or only allowed INFO, must pass")
	}
}

// TestExecRunner runs the real seam on sh, so it needs no Node.
func TestExecRunner(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := t.TempDir()

	t.Run("output and exit status", func(t *testing.T) {
		t.Parallel()
		out, err := execRunner{waitDelay: time.Second}.Run(context.Background(), dir, []string{"AVAL_PROBE=on"},
			"sh", "-c", `printf '%s %s' "$AVAL_PROBE" "$(pwd -P)"; printf oops >&2; exit 3`)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if string(out.stdout) != "on "+resolved || string(out.stderr) != "oops" || out.code != 3 {
			t.Errorf("got stdout %q stderr %q code %d", out.stdout, out.stderr, out.code)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := execRunner{}.Run(ctx, dir, nil, "sleep", "10")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Errorf("err = %v after %v, want a prompt deadline error", err, time.Since(start))
		}
	})
	t.Run("a child keeps the pipes open", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		_, err := execRunner{waitDelay: 100 * time.Millisecond}.Run(context.Background(), dir, nil, "sh", "-c", "sleep 5 & printf started")
		if !errors.Is(err, exec.ErrWaitDelay) || time.Since(start) > 3*time.Second {
			t.Errorf("err = %v after %v, want exec.ErrWaitDelay well before the child exits", err, time.Since(start))
		}
	})
	t.Run("output cap", func(t *testing.T) {
		t.Parallel()
		_, err := execRunner{}.Run(context.Background(), dir, nil, "sh", "-c", fmt.Sprintf("head -c %d /dev/zero", maxStdoutBytes+1))
		if err == nil || !strings.Contains(err.Error(), "output larger than") {
			t.Errorf("err = %v, want the output cap", err)
		}
	})
	t.Run("missing tool", func(t *testing.T) {
		t.Parallel()
		if _, err := (execRunner{}).LookPath("aval-no-such-tool"); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("LookPath error = %v, want exec.ErrNotFound", err)
		}
	})
}

func TestCapped(t *testing.T) {
	t.Parallel()
	c := &capped{max: 5}
	for _, p := range []string{"abc", "def", "g"} {
		if n, err := c.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if c.buf.String() != "abcde" || !c.truncated {
		t.Errorf("kept %q, truncated %v", c.buf.String(), c.truncated)
	}
}
