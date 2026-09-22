package gotest

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
)

func TestOptionsArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		opts Options
		want []string
	}{
		{Options{}, []string{"test", "-json", "./..."}}, // cached results are welcome
		{Options{Count: -2}, []string{"test", "-json", "./..."}},
		{Options{Count: 1}, []string{"test", "-json", "-count=1", "./..."}},
		{
			Options{Packages: []string{"./a", "./b"}, Run: "^TestX$", Count: 3, Timeout: 90 * time.Second},
			[]string{"test", "-json", "-count=3", "-run=^TestX$", "-timeout=1m30s", "./a", "./b"},
		},
	}
	for _, tt := range tests {
		if got := tt.opts.Args(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%+v.Args() = %q, want %q", tt.opts, got, tt.want)
		}
	}
}

func TestRunPattern(t *testing.T) {
	t.Parallel()
	if got, want := RunPattern("TestA.B", id(t, "ORD-F01")), `^TestA\.B$/^ORD-F01([_#]|$)`; got != want {
		t.Errorf("RunPattern = %q, want %q", got, want)
	}
	// go test matches each "/"-separated part against one name level.
	parts := strings.Split(RunPattern("TestOrder", id(t, "ORD-N01")), "/")
	top, sub := regexp.MustCompile(parts[0]), regexp.MustCompile(parts[1])
	if !top.MatchString("TestOrder") || top.MatchString("TestOrderNested") {
		t.Errorf("%s must select TestOrder only", top)
	}
	for name, want := range map[string]bool{
		"ORD-N01": true, "ORD-N01#01": true, "ORD-N01_x": true, "ORD-N01_x#02": true,
		"ORD-N010": false, "ORD-N01:_x": false, "XORD-N01": false, "ORD-N02": false,
	} {
		if got := sub.MatchString(name); got != want {
			t.Errorf("%s matches %q = %v, want %v", sub, name, got, want)
		}
	}
}

// fake returns an execFunc that checks its call and replays a canned run.
func fake(t *testing.T, stdout, stderr string, code int, err error) execFunc {
	return func(_ context.Context, dir string, args, env []string, out, errOut io.Writer) (int, error) {
		if want := (Options{Packages: []string{"./p"}}).Args(); dir != "dir" || !reflect.DeepEqual(args, want) {
			t.Errorf("exec(%q, %q), want (dir, %q)", dir, args, want)
		}
		if !reflect.DeepEqual(env, []string{"A=1", "K=V"}) {
			t.Errorf("env = %q, want Environ then Env: [A=1 K=V]", env)
		}
		_, _ = io.WriteString(out, stdout)
		_, _ = io.WriteString(errOut, stderr)
		return code, err
	}
}

func TestRun(t *testing.T) {
	t.Parallel()
	opts := Options{Packages: []string{"./p"}, Env: []string{"K=V"}, Environ: []string{"A=1"}}
	failing := fixture(t, "fail")

	r, err := run(t.Context(), "dir", opts, fake(t, failing, "", 1, nil))
	if err != nil || r.ExitCode != 1 || len(r.Packages) != 1 || r.Status(id(t, "ORD-F01")) != evidence.Fail {
		t.Errorf("failing tests: run = %+v, %v; want exit 1, ORD-F01 fail and no error", r.Packages, err)
	}
	if r, err := run(t.Context(), "dir", opts, fake(t, fixture(t, "pass"), "", 0, nil)); err != nil || r.ExitCode != 0 ||
		r.Status(id(t, "ORD-F05")) != evidence.Pass {
		t.Errorf("passing tests: run error = %v, exit %d", err, r.ExitCode)
	}

	notFound := &exec.Error{Name: "go", Err: exec.ErrNotFound}
	tests := []struct {
		name   string
		exec   execFunc
		code   int
		stderr string
		cause  error
	}{
		{"go missing", fake(t, "", "", -1, notFound), -1, "", exec.ErrNotFound},
		{"bad flag", fake(t, "", "invalid value \"x\" for flag -count\nusage: go test\n", 2, nil), 2, "invalid value", nil},
		{"unreadable output", fake(t, strings.Repeat("x", maxLine+1)+"\n"+failing, "", 1, nil), 1, "", bufio.ErrTooLong},
	}
	for _, tt := range tests {
		_, err := run(t.Context(), "dir", opts, tt.exec)
		var te *ToolError
		if !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a *ToolError", tt.name, err)
			continue
		}
		if te.ExitCode != tt.code || !strings.Contains(te.Stderr, tt.stderr) || !strings.Contains(te.Error(), tt.stderr) ||
			(tt.cause != nil) != errors.Is(err, tt.cause) || !strings.HasPrefix(te.Error(), "go test -json") {
			t.Errorf("%s: ToolError = %+v (%q), want exit %d, stderr %q, cause %v", tt.name, te, te, tt.code, tt.stderr, tt.cause)
		}
	}
}

func TestRunRejectsFlagsAsPackages(t *testing.T) {
	t.Parallel()
	opts := Options{Packages: []string{"./a", "-exec=evil.sh"}}
	_, err := run(t.Context(), "dir", opts, func(context.Context, string, []string, []string, io.Writer, io.Writer) (int, error) {
		t.Error("go ran with a flag among the packages")
		return 0, nil
	})
	if !errors.Is(err, ErrInvalidOptions) || !strings.Contains(err.Error(), "-exec=evil.sh") {
		t.Errorf("err = %v, want ErrInvalidOptions naming the package", err)
	}
}

func TestRunCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := run(ctx, "dir", Options{}, func(ctx context.Context, _ string, _, _ []string, _, _ io.Writer) (int, error) {
		return -1, ctx.Err()
	})
	var te *ToolError
	if !errors.Is(err, context.Canceled) || errors.As(err, &te) {
		t.Errorf("err = %v, want context.Canceled and no *ToolError", err)
	}
}

func TestRunGoMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Run(t.Context(), t.TempDir(), Options{})
	var te *ToolError
	if !errors.As(err, &te) || !errors.Is(err, exec.ErrNotFound) || te.ExitCode != -1 {
		t.Errorf("Run without go = %v, want a *ToolError wrapping exec.ErrNotFound", err)
	}
}

// TestRunFixtureModule really runs go test on the fixture module, so the
// selection facts the fixtures rely on hold with the current toolchain.
// fixtureModule is the module the fixtures come from, and fixtureEnv keeps
// the caller's workspace and flags out of runs in it, as regen.sh does.
var (
	fixtureModule = filepath.Join("testdata", "fixturemod")
	fixtureEnv    = []string{"GOWORK=off", "GOFLAGS=-mod=readonly"}
)

func TestRunFixtureModule(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	t.Parallel()
	dir, env := fixtureModule, fixtureEnv

	r, err := Run(t.Context(), dir, Options{Packages: []string{"./pass"}, Run: RunPattern("TestOrder", id(t, "ORD-N01")), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := owners(r), map[string][]string{"ORD-N01": {"TestOrder/ORD-N01", "TestOrder/ORD-N01#01"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Obligations() = %v, want %v: the pattern must select the #01 duplicate", got, want)
	}
	if s := r.Status(id(t, "ORD-N01")); s != evidence.Pass || r.ExitCode != 0 {
		t.Errorf("Status(ORD-N01) = %s, exit %d; want pass, 0", s, r.ExitCode)
	}

	r, err = Run(t.Context(), dir, Options{Packages: []string{"./pass"}, Run: RunPattern("TestOrder", id(t, "ORD-F99")), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if s := r.Status(id(t, "ORD-F99")); s != evidence.NotRun || r.ExitCode != 0 || len(r.Packages) != 1 || !r.Packages[0].NoTestsToRun {
		t.Errorf("missing ID: Status = %s, exit %d, packages %+v; want not_run, 0, no tests to run", s, r.ExitCode, r.Packages)
	}
}
