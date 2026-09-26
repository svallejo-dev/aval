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
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const pinnedPackage = `{"name": "@fission-ai/openspec", "version": "1.13.1", "bin": {"openspec": "./bin/openspec.js"}}`

// fakeRunner stands in for npm and node. Its npm install writes pkgJSON as
// the installed package.json, and node answers with out and err.
type fakeRunner struct {
	missing string // the tool LookPath does not find
	pkgJSON string // "" installs nothing
	npmOut  output
	out     output
	err     error

	mu    sync.Mutex
	calls []call
}

type call struct {
	dir      string
	env      []string
	cmd      []string
	deadline time.Duration // how far away the call's deadline was
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if file == f.missing {
		return "", fmt.Errorf("look up %s: %w", file, exec.ErrNotFound)
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) (output, error) {
	c := call{dir: dir, env: env, cmd: append([]string{name}, args...)}
	if d, ok := ctx.Deadline(); ok {
		c.deadline = time.Until(d)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if name != "npm" {
		return f.out, f.err
	}
	if f.pkgJSON != "" {
		pkg := filepath.Join(args[slices.Index(args, "--prefix")+1], "node_modules", "@fission-ai", "openspec")
		err := errors.Join(
			os.MkdirAll(filepath.Join(pkg, "bin"), 0o750),
			os.WriteFile(filepath.Join(pkg, "package.json"), []byte(f.pkgJSON), 0o600),
			os.WriteFile(filepath.Join(pkg, "bin", "openspec.js"), nil, 0o600),
		)
		if err != nil {
			return output{}, fmt.Errorf("fake npm install: %w", err)
		}
	}
	return f.npmOut, nil
}

func (f *fakeRunner) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var cmds [][]string
	for _, c := range f.calls {
		cmds = append(cmds, c.cmd)
	}
	return cmds
}

// testEnviron is the parent environment of the tests: its npm_config_*
// variables, in any case, must never reach a child.
var testEnviron = []string{"PATH=/usr/bin", "npm_config_registry=http://127.0.0.1:9/", "NPM_CONFIG_IGNORE_SCRIPTS=false", "Npm_Config_Dry_Run=true", "HOME=/home/dev"}

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
		run     *fakeRunner // default: one that answers nothing
		passed  bool
		is      []error // the error wraps each of these; nil for no error
		msg     string  // the error names this
	}{
		{name: "clean", run: &fakeRunner{out: output{stdout: captured("json/validate-spec-after.json")}}, passed: true},
		{name: "INFO fails", run: &fakeRunner{out: output{stdout: captured("negative/modified-case-variant/validate.json")}}},
		{name: "ERROR fails", run: &fakeRunner{out: output{stdout: captured("negative/modified-drops-scenario/validate.json"), code: 1}}},
		{name: "allowed INFO passes", run: &fakeRunner{out: output{stdout: info("INFO", skipSpecs)}}, passed: true},
		{name: "allowed message at another level fails", run: &fakeRunner{out: output{stdout: info("WARNING", skipSpecs)}}},
		{name: "allowed message must match exactly", run: &fakeRunner{out: output{stdout: info("INFO", skipSpecs+".")}}},
		{name: "node missing", run: &fakeRunner{missing: "node"}, is: []error{ErrToolMissing, exec.ErrNotFound}, msg: "look up node"},
		{name: "npm missing", run: &fakeRunner{missing: "npm"}, is: []error{ErrToolMissing, exec.ErrNotFound}, msg: "look up npm"},
		{name: "range version", version: "^1.13.1", msg: `version "^1.13.1" must be exact`},
		{name: "tag version", version: "latest", msg: `version "latest" must be exact`},
		{name: "no openspec directory", root: t.TempDir(), is: []error{fs.ErrNotExist}, msg: "has no openspec/ directory"},
		{name: "openspec is a file", root: withFile, msg: "openspec is not a directory"},
		{name: "run fails", run: &fakeRunner{err: context.DeadlineExceeded}, is: []error{ErrToolFailed, context.DeadlineExceeded}},
		{
			name: "another root",
			run:  &fakeRunner{out: output{stdout: []byte(strings.ReplaceAll(string(fixture(t, "json/validate-spec-after.json")), "<ROOT>", "/parent"))}},
			is:   []error{ErrToolFailed},
			msg:  `OpenSpec validated "/parent" instead of "` + root + `"`,
		},
		{
			name: "no OpenSpec root",
			run:  &fakeRunner{out: output{stdout: []byte(noRoot), code: 1}},
			is:   []error{ErrToolFailed},
			msg:  "exit status 1: No OpenSpec root found from the current directory. (no_openspec_root)",
		},
		{
			name: "OpenSpec crashes",
			run:  &fakeRunner{out: output{stderr: []byte("SyntaxError: Unexpected token\n"), code: 1}},
			is:   []error{ErrToolFailed},
			msg:  "decode validate report: unexpected end of JSON input\nSyntaxError: Unexpected token",
		},
		{name: "unknown report version", run: &fakeRunner{out: output{stdout: []byte(`{"version": "2.0", "items": []}`)}}, is: []error{ErrToolFailed}, msg: `version "2.0"`},
		{name: "report without items", run: &fakeRunner{out: output{stdout: []byte(`{"version": "1.0"}`)}}, is: []error{ErrToolFailed}},
		{
			name: "failing exit with a passing report",
			run:  &fakeRunner{out: output{stdout: report("[]"), code: 1}},
			is:   []error{ErrToolFailed},
			msg:  "exit status 1 with a passing report",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := cmp.Or(tt.run, &fakeRunner{})
			run.pkgJSON = pinnedPackage
			cache := t.TempDir()
			v := cliTool{run: run, cacheDir: cache, environ: testEnviron}
			got, err := v.validate(context.Background(), cmp.Or(tt.root, root), cmp.Or(tt.version, "1.13.1"))

			for _, target := range tt.is {
				if !errors.Is(err, target) {
					t.Errorf("error %v does not wrap %v", err, target)
				}
			}
			if wantErr := tt.is != nil || tt.msg != ""; (err != nil) != wantErr {
				t.Fatalf("validate() error = %v, want error %v", err, wantErr)
			}
			ran := run.out.stdout != nil || run.out.stderr != nil || run.err != nil
			if len(run.calls) != map[bool]int{false: 0, true: 2}[ran] {
				t.Fatalf("calls = %q", run.commands())
			}
			if err != nil {
				if !strings.Contains(err.Error(), tt.msg) {
					t.Errorf("error %q does not contain %q", err, tt.msg)
				}
				return
			}
			if got.Passed() != tt.passed || got.Root != root {
				t.Errorf("Passed() = %v, root %q; want %v, %q", got.Passed(), got.Root, tt.passed, root)
			}

			npm, node := run.calls[0], run.calls[1]
			wantNpm := []string{"npm", "install", "--prefix", npm.dir, "--no-save", "--ignore-scripts", "--no-audit", "--no-fund",
				"--registry", "https://registry.npmjs.org/", "@fission-ai/openspec@1.13.1"}
			if !slices.Equal(npm.cmd, wantNpm) || filepath.Dir(npm.dir) != cache || npm.dir == filepath.Join(cache, "1.13.1") {
				t.Errorf("npm ran %q in %s, want %q in a new directory of %s", npm.cmd, npm.dir, wantNpm, cache)
			}
			if want := []string{"PATH=/usr/bin", "HOME=/home/dev"}; !slices.Equal(npm.env, want) {
				t.Errorf("npm env = %q, want %q", npm.env, want)
			}
			cli := filepath.Join(cache, "1.13.1", "node_modules", "@fission-ai", "openspec", "bin", "openspec.js")
			wantNode := []string{"node", cli, "validate", "--all", "--strict", "--json"}
			if !slices.Equal(node.cmd, wantNode) || node.dir != root {
				t.Errorf("node ran %q in %s, want %q in %s", node.cmd, node.dir, wantNode, root)
			}
			wantEnv := []string{"PATH=/usr/bin", "HOME=/home/dev", "OPENSPEC_TELEMETRY=0", "OPENSPEC_NO_UPDATE_CHECK=1"}
			if !slices.Equal(node.env, wantEnv) {
				t.Errorf("node env = %q, want %q", node.env, wantEnv)
			}
			if npm.deadline <= 0 || npm.deadline > installTimeout || node.deadline <= 0 || node.deadline > runTimeout {
				t.Errorf("deadlines in %v and %v, want within %v and %v", npm.deadline, node.deadline, installTimeout, runTimeout)
			}
		})
	}
}

// TestInstall: OpenSpec is installed once per version into the cache, only
// if npm installed exactly the pinned package, and never half-written.
func TestInstall(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	passing := []byte(strings.ReplaceAll(string(fixture(t, "json/validate-spec-after.json")), "<ROOT>", root))
	pkg := func(name, version, bin string) string {
		return `{"name": "` + name + `", "version": "` + version + `", "bin": ` + bin + `}`
	}
	tests := []struct {
		name    string
		pkgJSON string
		npmOut  output
		broken  bool // the cache already holds a broken install
		msg     string
	}{
		{name: "bin as a map", pkgJSON: pinnedPackage},
		{name: "bin as a string", pkgJSON: pkg("@fission-ai/openspec", "1.13.1", `"bin/openspec.js"`)},
		{name: "another package", pkgJSON: pkg("openspec-evil", "1.13.1", `{"openspec": "./bin/openspec.js"}`),
			msg: "npm installed something else: package.json names openspec-evil@1.13.1, want @fission-ai/openspec@1.13.1"},
		{name: "another version", pkgJSON: pkg("@fission-ai/openspec", "1.13.2", `{"openspec": "./bin/openspec.js"}`),
			msg: "package.json names @fission-ai/openspec@1.13.2"},
		{name: "bin outside the package", pkgJSON: pkg("@fission-ai/openspec", "1.13.1", `{"openspec": "../../../evil.js"}`),
			msg: `bin "../../../evil.js" is not a file of the package`},
		{name: "no openspec bin", pkgJSON: pkg("@fission-ai/openspec", "1.13.1", `{"other": "./bin/openspec.js"}`), msg: `bin "" is not`},
		{name: "missing bin file", pkgJSON: pkg("@fission-ai/openspec", "1.13.1", `{"openspec": "./bin/gone.js"}`), msg: "bin: "},
		{name: "npm installs nothing", msg: "read package.json"},
		{name: "npm fails", pkgJSON: pinnedPackage, npmOut: output{stderr: []byte("npm error code E404\n"), code: 1},
			msg: "npm install: exit status 1\nnpm error code E404"},
		{name: "broken cache", pkgJSON: pinnedPackage, broken: true, msg: "holds a broken OpenSpec install, delete it to reinstall"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cache := t.TempDir()
			if tt.broken {
				if err := os.MkdirAll(filepath.Join(cache, "1.13.1", "node_modules"), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			run := &fakeRunner{pkgJSON: tt.pkgJSON, npmOut: tt.npmOut, out: output{stdout: passing}}
			v := cliTool{run: run, cacheDir: cache, environ: testEnviron}
			_, err := v.validate(context.Background(), root, "1.13.1")
			if tt.msg == "" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := v.validate(context.Background(), root, "1.13.1"); err != nil {
					t.Fatal(err)
				}
				if cmds := run.commands(); len(cmds) != 3 || cmds[0][0] != "npm" || cmds[1][0] != "node" || cmds[2][0] != "node" {
					t.Errorf("calls = %q, want one install and two runs", cmds)
				}
				return
			}
			if !errors.Is(err, ErrToolFailed) || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("error = %v, want ErrToolFailed with %q", err, tt.msg)
			}
			for _, c := range run.commands() {
				if c[0] == "node" {
					t.Errorf("node ran after a failed install: %q", c)
				}
			}
			entries, err := os.ReadDir(cache)
			if err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{false: 0, true: 1}[tt.broken]; len(entries) != want {
				t.Errorf("cache holds %d entries, want %d: a failed install must leave nothing", len(entries), want)
			}
		})
	}

	t.Run("concurrent runs", func(t *testing.T) {
		t.Parallel()
		cache := t.TempDir()
		run := &fakeRunner{pkgJSON: pinnedPackage, out: output{stdout: passing}}
		v := cliTool{run: run, cacheDir: cache, environ: testEnviron}
		errs := make(chan error, 8)
		for range 8 {
			go func() {
				_, err := v.validate(context.Background(), root, "1.13.1")
				errs <- err
			}()
		}
		for range 8 {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		entries, err := os.ReadDir(cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "1.13.1" {
			t.Errorf("cache holds %v, want only 1.13.1", entries)
		}
	})
}

func TestCleanEnv(t *testing.T) {
	t.Parallel()
	if got, want := cleanEnv(testEnviron), []string{"PATH=/usr/bin", "HOME=/home/dev"}; !slices.Equal(got, want) {
		t.Errorf("cleanEnv = %q, want %q", got, want)
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

// TestValidatorSeam checks the seam other packages hold: a Run of their own is
// what runs, its report and its error come back untouched, and a zero
// Validator is Validate itself — the real path, which a caller's private seam
// could never reach.
func TestValidatorSeam(t *testing.T) {
	t.Parallel()

	want := Report{Root: "/repo", Items: []Item{{ID: "add-refunds", Type: "change", Valid: true}}}
	var gotRoot, gotVersion string
	fake := Validator{Run: func(_ context.Context, root, version string) (Report, error) {
		gotRoot, gotVersion = root, version
		return want, nil
	}}
	got, err := fake.Validate(t.Context(), "/repo", "1.13.1")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Validate through Run = %+v, %v; want %+v", got, err, want)
	}
	if gotRoot != "/repo" || gotVersion != "1.13.1" {
		t.Errorf("Run got (%q, %q), want (%q, %q)", gotRoot, gotVersion, "/repo", "1.13.1")
	}

	boom := errors.New("boom")
	failing := Validator{Run: func(context.Context, string, string) (Report, error) { return Report{}, boom }}
	if _, err := failing.Validate(t.Context(), "/repo", "1.13.1"); !errors.Is(err, boom) {
		t.Errorf("Validate through a failing Run = %v, want %v", err, boom)
	}

	// The zero value runs Validate: an inexact version is refused before
	// anything is installed, so this reaches the real path without node or npm.
	_, err = Validator{}.Validate(t.Context(), t.TempDir(), "1.13")
	if err == nil || !strings.Contains(err.Error(), "must be exact") {
		t.Errorf("zero Validator with an inexact version = %v, want Validate's own refusal", err)
	}
}
