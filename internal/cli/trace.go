package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/envelope"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/testsource"
	"github.com/svallejo-dev/aval/internal/trace"
	"github.com/svallejo-dev/aval/internal/ui"
)

// traceFlags are the flags of `aval trace`.
type traceFlags struct {
	dir      string
	runTests bool
}

// traceData is the data payload of `aval trace --json`.
type traceData struct {
	Root string `json:"root"` // absolute path of the repository root
	trace.Matrix
	Findings []specFinding `json:"findings"` // spec rule violations, by path and line
}

// specFinding is an openspec.Finding in the JSON payload.
type specFinding struct {
	Severity openspec.Severity `json:"severity"`
	Rule     openspec.Rule     `json:"rule"`
	Path     string            `json:"path"`
	Line     int               `json:"line"`
	Message  string            `json:"message"`
}

func newTraceCmd(g *globalFlags) *cobra.Command {
	var f traceFlags
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Trace every obligation of the specs to the tests that declare it",
		Long: "trace lists each obligation the OpenSpec specs define with the Go tests that " +
			"declare its ID, and reports obligations without tests, tests without obligations, " +
			"open questions and spec rule violations. It fails when any of them is found. " +
			"With --run-tests it also runs go test and shows how each bound test did.",
		Args: cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			data, err := collectTrace(cmd.Context(), f)
			if err != nil {
				return err
			}
			r := traceResult(data)
			if err := g.printer(cmd).Print(r); err != nil {
				return fmt.Errorf("print trace: %w", err)
			}
			if len(r.Issues) > 0 {
				return &ExitError{Code: ExitFailed} // Print reported why
			}
			return nil
		}),
	}
	fl := cmd.Flags()
	fl.StringVar(&f.dir, "dir", "", "repository root (default: the nearest directory up from here that contains openspec/)")
	fl.BoolVar(&f.runTests, "run-tests", false, "run go test ./... and show the outcome of each bound test")
	return cmd
}

// collectTrace reads the specs and the tests of the repository and builds
// its matrix, running the tests first when f asks for it.
func collectTrace(ctx context.Context, f traceFlags) (traceData, error) {
	root, err := repoRoot(f.dir)
	if err != nil {
		return traceData{}, usageError(err)
	}
	repo, err := openspec.Load(ctx, os.DirFS(root))
	if err != nil {
		if errors.Is(err, manifest.ErrInvalid) {
			return traceData{}, usageError(fmt.Errorf("load specs: %w", err))
		}
		return traceData{}, fmt.Errorf("load specs: %w", err)
	}
	decls, err := testsource.Scan(root)
	if err != nil {
		return traceData{}, fmt.Errorf("scan tests: %w", err)
	}
	var rt *trace.Runtime
	if f.runTests {
		if rt, err = runTests(ctx, root); err != nil {
			return traceData{}, err
		}
	}

	data := traceData{Root: root, Matrix: trace.Build(repo, decls, rt), Findings: []specFinding{}}
	for _, fd := range repo.Check(openspec.CheckOptions{}) {
		data.Findings = append(data.Findings, specFinding(fd))
	}
	return data, nil
}

// runTests runs go test ./... in root. Its module path, read from root's
// go.mod, tells which package each declaration is; without a go.mod no
// declaration matches, and go test reports the setup failure.
func runTests(ctx context.Context, root string) (*trace.Runtime, error) {
	r, err := gotest.Run(ctx, root, gotest.Options{})
	if err != nil {
		err = fmt.Errorf("run tests: %w", err)
		if errors.Is(err, exec.ErrNotFound) {
			return nil, &ExitError{Code: ExitTool, Err: err}
		}
		return nil, err
	}
	gomod, err := fs.ReadFile(os.DirFS(root), "go.mod")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	return &trace.Runtime{Report: r, Module: modulePath(gomod)}, nil
}

// modulePath returns the path of the module directive of a go.mod, quoted
// or not, or "" when there is none.
func modulePath(gomod []byte) string {
	for line := range strings.Lines(string(gomod)) {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "module" {
			continue
		}
		if p, err := strconv.Unquote(f[1]); err == nil {
			return p
		}
		return f[1]
	}
	return ""
}

// repoRoot returns the absolute path of dir or, when dir is empty, of the
// nearest directory from the working directory up that holds an OpenSpec
// tree.
func repoRoot(dir string) (string, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve --dir: %w", err)
		}
		if !isRoot(abs) {
			return "", fmt.Errorf("%s has no %s/specs/ or %s/changes/ directory", dir, openspec.Dir, openspec.Dir)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("find the repository root: %w", err)
	}
	return findRoot(wd)
}

// findRoot walks up from start to the first directory that holds an
// OpenSpec tree.
func findRoot(start string) (string, error) {
	for dir := start; ; {
		if isRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s/specs/ or %s/changes/ directory in %s or above it: run aval inside a repository or pass --dir", openspec.Dir, openspec.Dir, start)
		}
		dir = parent
	}
}

// isRoot reports whether dir holds an OpenSpec tree: openspec/specs/ or
// openspec/changes/. A Go package named openspec is not one.
func isRoot(dir string) bool {
	for _, sub := range []string{"specs", "changes"} {
		if fi, err := os.Stat(filepath.Join(dir, openspec.Dir, sub)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// traceResult renders d as a table of obligations followed by the sections
// that explain a failure, and records why the trace failed, if it did.
func traceResult(d traceData) ui.Result {
	lines := []ui.Line{
		{{Text: "Root ", Tone: ui.ToneMuted}, {Text: d.Root}},
		{},
		{{Text: "Obligations", Tone: ui.ToneTitle}},
	}
	lines = append(lines, ui.Table(obligationHeader(d.RanTests), obligationRows(d.Matrix))...)
	lines = section(lines, "Spec findings", findingRows(d.Findings))
	lines = section(lines, "Orphans", orphanRows(d.Orphans))
	lines = section(lines, "Test warnings", warningRows(d.Warnings))
	lines = section(lines, "Build failures", buildFailureRows(d.BuildFailures))
	lines = section(lines, "Blocking", blockingRows(d.Rows))

	r := ui.Result{Command: "trace", Data: d, Lines: lines}
	if reasons := traceFailures(d); len(reasons) > 0 {
		r.Issues = []envelope.Issue{{Code: errorCode(ExitFailed), Message: "trace failed: " + strings.Join(reasons, ", ")}}
		return r
	}
	counts := make(map[trace.Status]int)
	for _, row := range d.Rows {
		counts[row.Status]++
	}
	summary := "✓ " + plural(counts[trace.Traced], "obligation") + " traced"
	if n := counts[trace.Untested]; n > 0 {
		summary += fmt.Sprintf(", %d untested", n)
	}
	r.Lines = append(r.Lines, ui.Line{}, ui.Line{{Text: summary, Tone: ui.ToneSuccess}})
	return r
}

// traceFailures says why d fails the trace: spec errors, orphans of any kind,
// open questions, obligations with a test that failed or did not build, and
// packages that did not build or set up. Warnings never fail it.
func traceFailures(d traceData) []string {
	var errs, failing int
	for _, f := range d.Findings {
		if f.Severity == openspec.SeverityError {
			errs++
		}
	}
	for _, r := range d.Rows {
		if failed(r.Runtime) || slices.ContainsFunc(r.Tests, func(t trace.Test) bool { return failed(t.Runtime) }) {
			failing++
		}
	}
	orphans := make(map[trace.OrphanKind]int)
	for _, o := range d.Orphans {
		orphans[o.Kind]++
	}
	var reasons []string
	for _, c := range []struct {
		n    int
		noun string
	}{
		{errs, "spec error"},
		{orphans[trace.OrphanUnverified], "unverified obligation"},
		{orphans[trace.OrphanTest], "orphan test"},
		{orphans[trace.OrphanRuntime], "undeclared runtime binding"},
		{len(d.Blocking), "open question"},
		{failing, "failing obligation"},
		{len(d.BuildFailures), "package build failure"},
	} {
		if c.n > 0 {
			reasons = append(reasons, plural(c.n, c.noun))
		}
	}
	return reasons
}

func failed(s evidence.Status) bool { return s == evidence.Fail || s == evidence.BuildFail }

func obligationHeader(ranTests bool) []string {
	if ranTests {
		return []string{"ID", "KIND", "STATUS", "RUN", "TITLE", "SPEC", "TESTS"}
	}
	return []string{"ID", "KIND", "STATUS", "TITLE", "SPEC", "TESTS"}
}

func obligationRows(m trace.Matrix) [][]ui.Span {
	rows := make([][]ui.Span, 0, len(m.Rows))
	for _, r := range m.Rows {
		row := []ui.Span{{Text: r.ID}, {Text: r.Kind}, {Text: string(r.Status), Tone: statusTone(r.Status)}}
		if m.RanTests {
			row = append(row, ui.Span{Text: string(r.Runtime), Tone: runtimeTone(r.Runtime)})
		}
		title := r.Title
		if r.Characterization {
			title += " (characterization)"
		}
		rows = append(rows, append(row, ui.Span{Text: title}, ui.Span{Text: place(r.Path, r.Line)}, ui.Span{Text: testsCell(r.Tests)}))
	}
	return rows
}

func findingRows(findings []specFinding) [][]ui.Span {
	rows := make([][]ui.Span, 0, len(findings))
	for _, f := range findings {
		tone := ui.ToneWarning
		if f.Severity == openspec.SeverityError {
			tone = ui.ToneError
		}
		rows = append(rows, []ui.Span{{Text: string(f.Severity), Tone: tone}, {Text: place(f.Path, f.Line)}, {Text: string(f.Rule)}, {Text: f.Message}})
	}
	return rows
}

func orphanRows(orphans []trace.Orphan) [][]ui.Span {
	rows := make([][]ui.Span, 0, len(orphans))
	for _, o := range orphans {
		where, what := place(o.Path, o.Line), "no test declares it"
		switch o.Kind {
		case trace.OrphanTest:
			what = o.Test + " declares an ID no spec defines"
		case trace.OrphanRuntime:
			where, what = o.Package, o.Test+" ran bound to it with no declaration in the source"
		}
		rows = append(rows, []ui.Span{{Text: string(o.Kind), Tone: ui.ToneError}, {Text: o.ID}, {Text: where}, {Text: what}})
	}
	return rows
}

func warningRows(warnings []trace.Warning) [][]ui.Span {
	rows := make([][]ui.Span, 0, len(warnings))
	for _, w := range warnings {
		rows = append(rows, []ui.Span{{Text: string(w.Kind), Tone: ui.ToneWarning}, {Text: w.Package}, {Text: w.Test}, {Text: w.Detail}})
	}
	return rows
}

// buildFailureRows shows each package that did not build with the first line
// of its explanation.
func buildFailureRows(failures []trace.BuildFailure) [][]ui.Span {
	rows := make([][]ui.Span, 0, len(failures))
	for _, f := range failures {
		why := f.FailedBuild
		for line := range strings.Lines(f.Output) {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				why = line
				break
			}
		}
		rows = append(rows, []ui.Span{{Text: string(evidence.BuildFail), Tone: ui.ToneError}, {Text: f.Package}, {Text: why}})
	}
	return rows
}

func blockingRows(obligations []trace.Row) [][]ui.Span {
	var rows [][]ui.Span
	for _, r := range obligations {
		if r.Status == trace.Blocking {
			rows = append(rows, []ui.Span{{Text: r.ID, Tone: ui.ToneError}, {Text: place(r.Path, r.Line)}, {Text: r.Title + " (open question: a human must resolve it)"}})
		}
	}
	return rows
}

// section appends a titled table of rows after a blank line; no rows, no section.
func section(lines []ui.Line, title string, rows [][]ui.Span) []ui.Line {
	if len(rows) == 0 {
		return lines
	}
	lines = append(lines, ui.Line{}, ui.Line{{Text: title, Tone: ui.ToneTitle}})
	return append(lines, ui.Table(nil, rows)...)
}

// testsCell lists the declarations of an obligation, with their outcome when
// the tests ran.
func testsCell(tests []trace.Test) string {
	if len(tests) == 0 {
		return "-"
	}
	cells := make([]string, 0, len(tests))
	for _, t := range tests {
		c := place(t.File, t.Line)
		if t.Runtime != "" {
			c += " " + string(t.Runtime)
		}
		cells = append(cells, c)
	}
	return strings.Join(cells, ", ")
}

func statusTone(s trace.Status) ui.Tone {
	switch s {
	case trace.Traced:
		return ui.ToneSuccess
	case trace.Untested:
		return ui.ToneWarning
	}
	return ui.ToneError
}

func runtimeTone(s evidence.Status) ui.Tone {
	switch s {
	case evidence.Pass:
		return ui.ToneSuccess
	case evidence.Fail, evidence.BuildFail:
		return ui.ToneError
	}
	return ui.ToneWarning
}

func place(path string, line int) string { return path + ":" + strconv.Itoa(line) }

// plural spells n things: "1 orphan test", "2 orphan tests".
func plural(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return strconv.Itoa(n) + " " + noun
}
