package gotest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// Options configures Run.
type Options struct {
	Packages []string // package patterns; empty means "./..."; none may start with "-"
	Run      string   // -run pattern, e.g. from Runs or RunPattern; empty runs every test
	// Count is -count, passed only when positive. Zero lets go test reuse
	// cached results, which parse the same; pass 1 to force a fresh run.
	Count   int
	Timeout time.Duration // -timeout; zero keeps go test's default
	Env     []string      // KEY=VALUE pairs added to Environ, e.g. RAPID_NOFAILFILE=1
	// Environ is the environment Env adds to. Nil means os.Environ(); pass a
	// filtered copy to keep variables away from go test and the tests.
	Environ []string
}

// ErrInvalidOptions is wrapped by Run's error for options it refuses.
var ErrInvalidOptions = errors.New("invalid go test options")

// Args returns the arguments Run passes to the go command.
func (o Options) Args() []string {
	args := []string{"test", "-json"}
	if o.Count > 0 {
		args = append(args, "-count="+strconv.Itoa(o.Count))
	}
	if o.Run != "" {
		args = append(args, "-run="+o.Run)
	}
	if o.Timeout > 0 {
		args = append(args, "-timeout="+o.Timeout.String())
	}
	if len(o.Packages) == 0 {
		return append(args, "./...")
	}
	return append(args, o.Packages...)
}

// RunPattern returns the -run pattern that selects the subtests of top-level
// test testName whose name starts with id, bare IDs ("ORD-F01") and their
// duplicates ("ORD-F01#01") included: ^TestX$/^ORD-F01([_#]|$). testName is
// a test function name; a "/" in it would add a level to the pattern. IDs
// bound deeper or only by attr are not selected.
//
// It anchors one level and the ID below it, which is all there is to anchor
// when a top-level test is named on its own. Given the full test names of a
// run, use Selector and Runs instead: they anchor every level, so an ID bound
// under a suite or by an attr is selected as ADR-0005 §2.3 asks.
func RunPattern(testName string, id obligation.ID) string {
	return "^" + regexp.QuoteMeta(testName) + "$/^" + regexp.QuoteMeta(id.String()) + "([_#]|$)"
}

// ToolError reports that go test could not run at all, as opposed to tests
// that failed or packages that did not build, which only show in the Report.
type ToolError struct {
	Args     []string // arguments to the go command
	ExitCode int      // -1 when go did not start or did not exit on its own
	Stderr   string   // what go wrote to standard error, at most MaxOutput bytes
	Err      error    // the cause, if any, e.g. wrapping exec.ErrNotFound
}

func (e *ToolError) Error() string {
	msg := "go " + strings.Join(e.Args, " ")
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	} else {
		msg += fmt.Sprintf(": exit status %d", e.ExitCode)
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(e.Stderr), "\n"); line != "" {
		msg += ": " + line
	}
	return msg
}

func (e *ToolError) Unwrap() error { return e.Err }

// Run runs `go test -json` in dir and parses its output as it arrives.
// Failing tests, failed builds and timeouts are not errors: go test exits
// non-zero, Report.ExitCode says so and the Report says why. Run returns a
// *ToolError when go test could not run at all: go is missing, it exited
// non-zero without reporting a single package (a bad flag), or its output
// could not be read. When ctx ends first it stops go test and the tests it
// started, and returns ctx's error, wrapped. Options it refuses, such as a
// package that would be read as a flag, wrap ErrInvalidOptions.
func Run(ctx context.Context, dir string, opts Options) (Report, error) {
	return run(ctx, dir, opts, execGo)
}

// environ returns the whole environment go test runs with.
func (o Options) environ() []string {
	env := o.Environ
	if env == nil {
		env = os.Environ()
	}
	return append(slices.Clip(env), o.Env...)
}

// execFunc runs the go command with args in dir and exactly env as its
// environment. exitCode is go's exit status; err is set only when go did
// not run or did not exit on its own. It is the seam unit tests replace
// with a canned stream.
type execFunc func(ctx context.Context, dir string, args, env []string, stdout, stderr io.Writer) (exitCode int, err error)

func run(ctx context.Context, dir string, opts Options, goCmd execFunc) (Report, error) {
	for _, p := range opts.Packages {
		if strings.HasPrefix(p, "-") {
			return Report{}, fmt.Errorf("%w: package %q would be read as a flag", ErrInvalidOptions, p)
		}
	}
	args := opts.Args()
	var stderr capture
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	pr, pw := io.Pipe()
	go func() {
		code, err := goCmd(ctx, dir, args, opts.environ(), pw, &stderr)
		_ = pw.Close() // always nil for a PipeWriter; Parse then sees EOF
		done <- result{code, err}
	}()
	rep, parseErr := Parse(pr)
	_, _ = io.Copy(io.Discard, pr) // after a parse error, let go finish writing
	res := <-done

	toolErr := func(err error) error {
		return &ToolError{Args: args, ExitCode: res.code, Stderr: stderr.b.String(), Err: err}
	}
	switch {
	case res.err != nil && ctx.Err() != nil:
		return Report{}, fmt.Errorf("go test: %w", context.Cause(ctx))
	case res.err != nil:
		return Report{}, toolErr(res.err)
	case parseErr != nil:
		return Report{}, toolErr(parseErr)
	case res.code != 0 && len(rep.Packages) == 0:
		return Report{}, toolErr(nil)
	}
	rep.ExitCode = res.code
	return rep, nil
}

// waitDelay bounds how long Run waits, once ctx ends, for go to exit after
// the interrupt and for its output to close.
const waitDelay = 5 * time.Second

// execGo runs the go command found in PATH.
func execGo(ctx context.Context, dir string, args, env []string, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // no shell: args come from Options.Args
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = waitDelay
	killGroup := ownProcessGroup(cmd)
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		killGroup()
		return -1, fmt.Errorf("run go: %w", context.Cause(ctx))
	case errors.As(err, &exitErr) && exitErr.Exited():
		return exitErr.ExitCode(), nil
	case err != nil:
		return -1, fmt.Errorf("run go: %w", err)
	}
	return 0, nil
}
