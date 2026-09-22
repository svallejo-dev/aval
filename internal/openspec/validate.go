package openspec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	// ErrToolMissing is returned when node or npx is not on PATH. Callers map
	// it to exit code 3 (ADR-0004).
	ErrToolMissing = errors.New("openspec: node and npx are required")
	// ErrToolFailed is returned when the OpenSpec CLI ran but produced no
	// validation report aval can use: npx could not fetch the package, the
	// run timed out, the output has an unknown shape, or OpenSpec validated
	// another root than the repository.
	ErrToolFailed = errors.New("openspec: the OpenSpec CLI failed")
)

// RuleOpenSpec marks a finding that comes from `openspec validate`.
const RuleOpenSpec Rule = "openspec"

const (
	npmPackage       = "@fission-ai/openspec"
	reportVersion    = "1.0" // the "version" of validate's JSON report
	runTimeout       = 3 * time.Minute
	defaultWaitDelay = 5 * time.Second
	maxStdoutBytes   = 16 << 20
	maxStderrBytes   = 8 << 10
)

var (
	exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	// cliEnv turns off OpenSpec's telemetry and update check, and fetches
	// the package from the public npm registry: the environment beats any
	// .npmrc, so a repository cannot redirect the download.
	cliEnv = []string{"OPENSPEC_TELEMETRY=0", "OPENSPEC_NO_UPDATE_CHECK=1", "npm_config_registry=https://registry.npmjs.org/"}
)

// allowedInfo lists, word for word, the INFO messages that do not fail
// validation. Every other INFO does (ADR-0002, rule 4); a new entry needs
// an amendment to that rule.
var allowedInfo = []string{
	// A change that declares skip_specs and has no spec deltas: it has no
	// obligations to validate and nothing for archive to apply.
	"skip_specs is set in .openspec.yaml: change declares no spec-level behavior changes, zero deltas accepted",
}

func (is Issue) allowed() bool { return is.Level == "INFO" && slices.Contains(allowedInfo, is.Message) }

// Issue is one problem `openspec validate` reports.
type Issue struct {
	Level   string `json:"level"` // ERROR, WARNING or INFO
	Path    string `json:"path"`  // for a change, usually relative to its specs/ directory
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Item is the result for one change or spec.
type Item struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"` // "change" or "spec"
	Valid  bool    `json:"valid"`
	Issues []Issue `json:"issues"`
}

// Report is what `openspec validate --all --strict --json` found.
type Report struct {
	Root  string // the directory OpenSpec validated
	Items []Item
}

// Passed reports whether OpenSpec found nothing. Any issue fails, INFO
// included: OpenSpec keeps an item valid while announcing that archive will
// refuse it or that part of a delta is ignored (ADR-0002, rule 4). The only
// exceptions are the INFO messages in allowedInfo.
func (r Report) Passed() bool {
	for _, it := range r.Items {
		if !it.Valid || slices.ContainsFunc(it.Issues, func(is Issue) bool { return !is.allowed() }) {
			return false
		}
	}
	return true
}

// Findings returns every issue that fails validation as an error finding. A delta spec issue
// points at its file and line; any other issue points at the change
// directory or the spec file.
func (r Report) Findings() []Finding {
	var out []Finding
	for _, it := range r.Items {
		where := path.Join(Dir, "specs", it.ID, "spec.md")
		if it.Type == "change" {
			where = path.Join(Dir, "changes", it.ID)
		}
		if !it.Valid && len(it.Issues) == 0 {
			out = append(out, Finding{Severity: SeverityError, Rule: RuleOpenSpec, Path: where,
				Message: fmt.Sprintf("OpenSpec marks %s %s invalid without reporting an issue", it.Type, it.ID)})
		}
		for _, is := range it.Issues {
			if is.allowed() {
				continue
			}
			f := Finding{Severity: SeverityError, Rule: RuleOpenSpec, Path: where, Line: is.Line,
				Message: fmt.Sprintf("%s %s: %s", is.Level, is.Path, is.Message)}
			if it.Type == "change" {
				if strings.HasSuffix(is.Path, "/spec.md") {
					f.Path = path.Join(where, "specs", is.Path)
				} else {
					f.Line = 0 // the line belongs to a file aval cannot name
				}
			}
			out = append(out, f)
		}
	}
	return out
}

// Validate runs `openspec validate --all --strict --json` in repoRoot with
// OpenSpec at the exact version, through npx: the first run downloads the
// package from the public npm registry. OpenSpec's telemetry and update
// check are off. A report with failures is not an error: check
// Report.Passed. The run is bounded by ctx and by a timeout that covers the
// download.
//
// repoRoot must contain openspec/, and OpenSpec must report that it
// validated repoRoot itself: otherwise it may have picked a parent
// directory or a configured store, and Validate fails.
func Validate(ctx context.Context, repoRoot, version string) (Report, error) {
	return validate(ctx, execRunner{waitDelay: defaultWaitDelay}, repoRoot, version)
}

// runner is the seam between Validate and the operating system.
type runner interface {
	LookPath(file string) (string, error)
	// Run runs name in dir with env added to the environment. A non-zero
	// exit is not an error; err means the command could not run or finish.
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (output, error)
}

type output struct {
	stdout, stderr []byte
	code           int
}

func validate(ctx context.Context, run runner, repoRoot, version string) (Report, error) {
	if !exactVersion.MatchString(version) {
		return Report{}, fmt.Errorf("openspec: version %q must be exact, like 1.13.1", version)
	}
	root, err := canonicalRoot(repoRoot)
	if err != nil {
		return Report{}, err
	}
	for _, tool := range []string{"node", "npx"} {
		if _, err := run.LookPath(tool); err != nil {
			return Report{}, fmt.Errorf("%w: %w", ErrToolMissing, err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, err := run.Run(ctx, root, cliEnv, "npx", "-y", npmPackage+"@"+version, "validate", "--all", "--strict", "--json")
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrToolFailed, err)
	}
	report, err := parseReport(out.stdout)
	switch {
	case err != nil:
		return Report{}, fmt.Errorf("%w: exit status %d: %w%s", ErrToolFailed, out.code, err, withStderr(out.stderr))
	case report.Root != root:
		return Report{}, fmt.Errorf("%w: OpenSpec validated %q instead of %q", ErrToolFailed, report.Root, root)
	case out.code != 0 && report.Passed():
		return Report{}, fmt.Errorf("%w: exit status %d with a passing report%s", ErrToolFailed, out.code, withStderr(out.stderr))
	}
	return report, nil
}

// canonicalRoot resolves repoRoot the way OpenSpec reports it, absolute and
// without symlinks, and checks that it holds an openspec/ directory.
func canonicalRoot(repoRoot string) (string, error) {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("openspec: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("openspec: %w", err)
	}
	info, err := os.Stat(filepath.Join(root, Dir))
	if err != nil {
		return "", fmt.Errorf("openspec: %s has no %s/ directory: %w", root, Dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("openspec: %s/%s is not a directory", root, Dir)
	}
	return root, nil
}

// parseReport decodes validate's JSON report. OpenSpec prints a
// {"status": [...]} document instead when it cannot validate at all, for
// example with no openspec/ directory.
func parseReport(stdout []byte) (Report, error) {
	var raw struct {
		Version string `json:"version"`
		Root    struct {
			Path string `json:"path"`
		} `json:"root"`
		Items  *[]Item `json:"items"`
		Status []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return Report{}, fmt.Errorf("decode validate report: %w", err)
	}
	if len(raw.Status) > 0 {
		msgs := make([]string, len(raw.Status))
		for i, s := range raw.Status {
			msgs[i] = s.Message + " (" + s.Code + ")"
		}
		return Report{}, errors.New(strings.Join(msgs, "; "))
	}
	if raw.Version != reportVersion || raw.Items == nil {
		return Report{}, fmt.Errorf("unknown validate report (version %q, want %q)", raw.Version, reportVersion)
	}
	return Report{Root: raw.Root.Path, Items: *raw.Items}, nil
}

// withStderr appends what the command wrote to stderr to an error message.
func withStderr(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	return "\n" + s
}

// execRunner runs commands with os/exec. waitDelay bounds how long Run
// waits for the output pipes once the process has exited or been killed:
// npx starts node as a child that can keep them open.
type execRunner struct {
	waitDelay time.Duration
}

func (execRunner) LookPath(file string) (string, error) {
	p, err := exec.LookPath(file)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", file, err)
	}
	return p, nil
}

func (r execRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) (output, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // validate passes a fixed command and a version checked by exactVersion
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	stdout, stderr := &capped{max: maxStdoutBytes}, &capped{max: maxStderrBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = r.waitDelay
	err := cmd.Run()
	if ctx.Err() != nil {
		return output{}, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return output{}, fmt.Errorf("%s: %w", name, err)
	}
	if stdout.truncated {
		return output{}, fmt.Errorf("%s: output larger than %d bytes", name, maxStdoutBytes)
	}
	return output{stdout: stdout.buf.Bytes(), stderr: stderr.buf.Bytes(), code: cmd.ProcessState.ExitCode()}, nil
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
