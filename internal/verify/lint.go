package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
)

const (
	// lintTool is golangci-lint's name on PATH, and lintMajor the major
	// version whose flags the ratchet uses.
	lintTool  = "golangci-lint"
	lintMajor = 2
	// maxLintOutput caps golangci-lint's JSON report; more is an error, so a
	// truncated report can never read as no new issues.
	maxLintOutput = 64 << 20
	// lintWaitDelay bounds how long golangci-lint may keep its output open
	// once it has exited or been stopped.
	lintWaitDelay = 5 * time.Second
)

// lintVersion matches the version in `golangci-lint version`, which reads
// "golangci-lint has version 2.13.2 built with go1.27.0 …".
var lintVersion = regexp.MustCompile(`version v?([0-9]+)\.([0-9]+)\.([0-9]+)`)

// lintReport is the part of golangci-lint's JSON report aval reads. Only the
// count matters: the rule is a ratchet, not a review.
type lintReport struct {
	Issues []struct {
		FromLinter string `json:"FromLinter"`
	} `json:"Issues"`
}

// ratchet runs golangci-lint with the .golangci.yml of the base commit, whose
// bytes phase 1 read from the object database, and --new-from-merge-base, so
// that only what the range adds counts (ADR-0005 §4, lint_new_issues). A base
// without a configuration switches the rule off; a run that does not finish
// leaves Ran false, which blocks.
//
// The error wraps ErrTool when golangci-lint is missing or is not version 2:
// the flags the ratchet needs are that version's.
func (c *collector) ratchet(ctx context.Context) (gate.Lint, error) {
	if c.baseLint == nil {
		return gate.Lint{}, nil
	}
	lint := gate.Lint{BaseConfig: true}
	if err := c.lintToolVersion(ctx); err != nil {
		return lint, err
	}
	cfg, err := c.writeBaseLint()
	if err != nil {
		return lint, err
	}
	args := []string{
		"run", "--config", cfg, "--new-from-merge-base", c.ev.Base,
		"--issues-exit-code=0", "--output.json.path", "stdout", "./...",
	}
	start := time.Now()
	// Only what golangci-lint wrote to standard output decides: its warnings
	// go to standard error, where they cannot break the report.
	out, _, code, err := c.lintRun(ctx, args...)
	if err != nil {
		return lint, err
	}
	var report lintReport
	// A decoder stops at the end of the first value: golangci-lint prints its
	// statistics after the report.
	decodeErr := json.NewDecoder(bytes.NewReader(out)).Decode(&report)
	lint.Ran = code == 0 && decodeErr == nil
	if lint.Ran {
		lint.NewIssues = len(report.Issues)
	}
	status := evidence.Fail
	switch {
	case !lint.Ran:
		status = evidence.NotRun
	case lint.NewIssues == 0:
		status = evidence.Pass
	}
	c.check("lint", lintTool+" "+quoteArgs(args), code, time.Since(start), status)
	return lint, nil
}

// writeBaseLint writes the base configuration inside the repository root, as
// .golangci.base-<base>.yml, immediately before the run and never earlier.
// golangci-lint anchors a configuration's own relative paths at the directory
// the file is in, so one outside the repository would silently void every
// path rule the base configuration has and report issues it excludes. The name
// is not the one the policy watches, and git never reported it, so it appears
// in no diff, no scope and no tamper finding; close removes it.
//
// The bytes are the ones phase 1 read from the trust base's tree, not a file on
// disk: this runs after every test of head, and one of them may have rewritten
// the extracted tree. The name is derived from the base, so a test can guess it,
// which is why the open refuses a symlink and truncates rather than appends:
// otherwise a test could point it at a file outside the repository and have aval
// write through it.
func (c *collector) writeBaseLint() (string, error) {
	name := filepath.Join(c.ev.Root, ".golangci.base-"+c.ev.Base+".yml")
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|noFollow, 0o600) //nolint:gosec // aval's own name below the repository root; symlinks are refused
	if err != nil {
		return "", fmt.Errorf("verify: write the base lint configuration: %w", err)
	}
	c.lintFile = name // recorded before the write, so close removes it either way
	_, err = f.Write(c.baseLint)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("verify: write the base lint configuration: %w", err)
	}
	return name, nil
}

// lintToolVersion checks that golangci-lint is on PATH and is version 2.
func (c *collector) lintToolVersion(ctx context.Context) error {
	out, stderr, _, err := c.lintRun(ctx, "version")
	if err != nil {
		return err
	}
	m := lintVersion.FindSubmatch(out)
	if m == nil {
		m = lintVersion.FindSubmatch(stderr)
		out = stderr
	}
	if m == nil {
		return fmt.Errorf("%w: %s version says %q", ErrTool, lintTool, bytes.TrimSpace(out))
	}
	if major, err := strconv.Atoi(string(m[1])); err != nil || major != lintMajor {
		return fmt.Errorf("%w: %s %s found, want version %d", ErrTool, lintTool, m[1], lintMajor)
	}
	return nil
}

// lintRun runs golangci-lint in the repository root and returns what it wrote
// to standard output and to standard error, each capped and kept apart so that
// a "level=warning" line cannot end up in the report aval decodes, and its
// exit status. A non-zero status is not an error: the caller decides.
func (c *collector) lintRun(ctx context.Context, args ...string) (stdout, stderr []byte, code int, err error) {
	cmd := exec.CommandContext(ctx, lintTool, args...) //nolint:gosec // no shell: a fixed command with aval's own arguments
	cmd.Dir = c.ev.Root
	cmd.Env = append(c.testEnv(), c.o.Env...)
	out, errs := capped{max: maxLintOutput}, capped{max: maxLintOutput}
	cmd.Stdout, cmd.Stderr = &out, &errs
	cmd.WaitDelay = lintWaitDelay
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return nil, nil, -1, fmt.Errorf("verify: %s: %w", lintTool, context.Cause(ctx))
	case errors.Is(err, exec.ErrNotFound):
		return nil, nil, -1, fmt.Errorf("%w: %s: %w", ErrTool, lintTool, err)
	case err != nil && !errors.As(err, &exit):
		return nil, nil, -1, fmt.Errorf("verify: %s: %w", lintTool, err)
	case out.truncated || errs.truncated:
		return nil, nil, -1, fmt.Errorf("verify: %s wrote more than %d bytes", lintTool, maxLintOutput)
	}
	return out.buf.Bytes(), errs.buf.Bytes(), cmd.ProcessState.ExitCode(), nil
}

// capped keeps the first max bytes written to it and drops the rest, so a
// runaway process cannot exhaust memory. It must not embed bytes.Buffer:
// io.Copy would use the promoted ReadFrom and skip the cap.
type capped struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.max - c.buf.Len(); n > room {
		p, c.truncated = p[:room], true
	}
	c.buf.Write(p)
	return n, nil
}

// quoteArgs renders arguments for the bundle's command field, quoting the ones
// that would not survive a copy and paste.
func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = a
		if strings.ContainsAny(a, " \t\"'") {
			quoted[i] = strconv.Quote(a)
		}
	}
	return strings.Join(quoted, " ")
}
