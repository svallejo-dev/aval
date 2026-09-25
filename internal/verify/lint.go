package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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

// ratchet runs golangci-lint with the .golangci.yml of the base commit, which
// phase 1 extracted from git, and --new-from-merge-base, so that only what the
// range adds counts (ADR-0005 §4, lint_new_issues). A base without a
// configuration switches the rule off; a run that does not finish leaves
// Ran false, which blocks.
//
// The error wraps ErrTool when golangci-lint is missing or is not version 2:
// the flags the ratchet needs are that version's.
func (c *collector) ratchet(ctx context.Context) (gate.Lint, error) {
	if c.baseLint == "" {
		return gate.Lint{}, nil
	}
	lint := gate.Lint{BaseConfig: true}
	if err := c.lintToolVersion(ctx); err != nil {
		return lint, err
	}
	args := []string{
		"run", "--config", c.baseLint, "--new-from-merge-base", c.ev.Base,
		"--issues-exit-code=0", "--output.json.path", "stdout", "./...",
	}
	start := time.Now()
	out, code, err := c.lintRun(ctx, args...)
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

// lintToolVersion checks that golangci-lint is on PATH and is version 2.
func (c *collector) lintToolVersion(ctx context.Context) error {
	out, _, err := c.lintRun(ctx, "version")
	if err != nil {
		return err
	}
	m := lintVersion.FindSubmatch(out)
	if m == nil {
		return fmt.Errorf("%w: %s version says %q", ErrTool, lintTool, bytes.TrimSpace(out))
	}
	if major, err := strconv.Atoi(string(m[1])); err != nil || major != lintMajor {
		return fmt.Errorf("%w: %s %s found, want version %d", ErrTool, lintTool, m[1], lintMajor)
	}
	return nil
}

// lintRun runs golangci-lint in the repository root and returns what it wrote
// to standard output and standard error together, capped, and its exit
// status. A non-zero status is not an error: the caller decides.
func (c *collector) lintRun(ctx context.Context, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, lintTool, args...) //nolint:gosec // no shell: a fixed command with aval's own arguments
	cmd.Dir = c.ev.Root
	cmd.Env = append(c.testEnv(), c.o.Env...)
	var out capped
	out.max = maxLintOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = lintWaitDelay
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return nil, -1, fmt.Errorf("verify: %s: %w", lintTool, context.Cause(ctx))
	case errors.Is(err, exec.ErrNotFound):
		return nil, -1, fmt.Errorf("%w: %s: %w", ErrTool, lintTool, err)
	case err != nil && !errors.As(err, &exit):
		return nil, -1, fmt.Errorf("verify: %s: %w", lintTool, err)
	case out.truncated:
		return nil, -1, fmt.Errorf("verify: %s wrote more than %d bytes", lintTool, maxLintOutput)
	}
	return out.buf.Bytes(), cmd.ProcessState.ExitCode(), nil
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
