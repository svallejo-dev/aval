// Package verify gathers the evidence for one base..head range of a
// repository: it fills the gate's Input and, once the gate has decided,
// assembles the evidence bundle and leaves it where the workflow and the
// agent hooks look for it. It decides nothing itself (ADR-0005).
//
// # Phases
//
// The order of the phases is policy, not an implementation detail
// (ADR-0005 §1): a test of the pull request runs in the same job and may
// rewrite the working tree, .git/config or .git/info, so everything aval
// reads with git is read before any of head's code executes.
//
//  0. The working tree key the agent hooks compare against
//     (hook.CurrentKey), taken before verify writes a file of its own, so
//     that an edit made during the run leaves the status stale (§7).
//  1. Git only, in parallel. First wave: the base and head commits, the
//     base tree extracted from git objects to a temporary directory (base
//     policy, baseline, specs, declarations and .golangci.yml all come from
//     there), the range diff, head's specs and declarations and the module
//     path. Second wave, which needs the first: the scope of every commit
//     (it needs the base policy's globs), the base specs and declarations,
//     the fail-before worktree (overlay.Prepare) and openspec validation.
//  2. Head's own code. The full go test run first, because the fail-before
//     and the isolated regressions select tests by the names it reports
//     (§2.3), then those runs and the lint ratchet. Nothing here may feed
//     phase 1.
//  3. Assembling gate.Input, pure.
//
// Phase 2 stays serial: two go test processes over one module share the
// build cache and whatever the tests themselves hold, and a flake there
// would read as missing evidence.
package verify

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/svallejo-dev/aval/internal/baseline"
	"github.com/svallejo-dev/aval/internal/codeowners"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/gotest"
	"github.com/svallejo-dev/aval/internal/hook"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/overlay"
	"github.com/svallejo-dev/aval/internal/platform/git"
	"github.com/svallejo-dev/aval/internal/platform/gitenv"
	"github.com/svallejo-dev/aval/internal/scope"
	"github.com/svallejo-dev/aval/internal/testsource"
	"github.com/svallejo-dev/aval/internal/trace"
)

var (
	// ErrTool is wrapped when a tool aval needs is missing or is not the
	// version it needs: go, git 2.40 or newer, node and npm, golangci-lint 2.
	// The CLI maps it to exit code 3 (ADR-0005 §6).
	ErrTool = errors.New("verify: a required tool is missing or has the wrong version")
	// ErrUsage is wrapped by a commit range aval cannot use and by an invalid
	// base policy or baseline. The CLI maps it to exit code 2.
	ErrUsage = errors.New("verify: invalid input")
)

// EvidenceDir is where verify writes the bundle, relative to the repository
// root (ADR-0005 §7). The workflow uploads it as an artifact.
const EvidenceDir = ".aval/evidence"

// lintConfig is the golangci-lint configuration the ratchet reads from the
// base commit, and whose edit is tamper (ADR-0005 §4).
const lintConfig = ".golangci.yml"

// notCollected is the evidence ADR-0005 describes and v0 does not gather, so
// that its absence is explicit in every bundle instead of reading as a pass.
var notCollected = []string{"mutation", "rollback", "slo"}

// notValidated joins notCollected when the pull request touches openspec/ and
// the base has no policy to take the pinned OpenSpec version from, so that a
// skipped openspec validate never reads as a passing one.
const notValidated = "openspec_validate"

// Options configures Collect.
type Options struct {
	// Dir is any directory of the repository; Collect works from its root,
	// which must also be the Go module root. "" is the working directory.
	// Head's files are read from there, as in the CI checkout of the head
	// commit; locally that is the working tree, whose changes the hook key
	// covers.
	Dir string
	// Base is the merge base of the pull request with the target branch and
	// Head its head commit (ADR-0005 §1).
	Base, Head string
	// Approvals are the pull request's reviews, already judged (ADR-0005 §5).
	// Collect makes no network call: the gate command reads them.
	Approvals []evidence.Approval
	// Repo names the repository in the bundle, e.g. "svallejo-dev/aval".
	Repo string
	// AvalVersion is the build that gathered the evidence; "" is "devel".
	AvalVersion string
	// Concurrency bounds phase 1's readers; zero means runtime.GOMAXPROCS(0).
	// The test runs of phase 2 are serial whatever it says.
	Concurrency int
	// Timeout bounds the whole collection and TestTimeout is go test's
	// -timeout for every run. Zero leaves the first to ctx and the second to
	// go test's own default.
	Timeout, TestTimeout time.Duration
	// Env are KEY=VALUE pairs added to the environment of go test, of the
	// tests and of golangci-lint.
	Env []string
	// TempDir holds the base tree and the fail-before worktree. "" means
	// $RUNNER_TEMP when it is set, which GitHub Actions empties after every
	// job, else the operating system's temporary directory.
	TempDir string

	// validate is openspec.Validate, replaced in tests so that they never
	// install the OpenSpec CLI.
	validate func(ctx context.Context, repoRoot, version string) (openspec.Report, error)
}

// Evidence is what Collect gathered: what the gate decides on, and what it
// takes to write the bundle once it has.
type Evidence struct {
	// Input is everything the gate knows about the pull request.
	Input gate.Input
	// Base and Head are the full SHAs of the range's commits.
	Base, Head string
	// Changes are the IDs of the OpenSpec changes the range touches.
	Changes []string
	// Checks are the verifier runs, in the order they ran.
	Checks []evidence.Check
	// PolicySource says where the base policy came from, or why there is none.
	PolicySource string
	// Root is the repository root Collect worked from.
	Root string

	notCollected      []string
	repo, avalVersion string
	generatedAt       time.Time
	key               hook.Key // the working tree as it was before anything ran
}

// Collect gathers the evidence for o's range in the phase order of ADR-0005
// §1: everything git-derived first, then head's own code. The result is the
// gate's Input, filled; a failing test, a package that does not build and a
// missing fail-before are evidence, not errors.
//
// The error wraps ErrTool when a tool is missing or has the wrong version and
// ErrUsage for a range or a base policy aval cannot use.
func Collect(ctx context.Context, o Options) (*Evidence, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	c, err := newCollector(ctx, o)
	if err != nil {
		return nil, classify(err)
	}
	// Deferred as well as called, so that a panic does not leave a worktree
	// registered and a temporary directory behind; close does nothing twice.
	defer func() { _ = c.close() }()
	ev, err := c.collect(ctx)
	if cerr := c.close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, classify(err)
	}
	return ev, nil
}

// Bundle assembles the bundle for a verdict the gate reached from e.Input.
// verify never produces the verdict itself.
func (e *Evidence) Bundle(verdict evidence.Verdict) evidence.Bundle {
	return evidence.Bundle{
		SchemaVersion: evidence.SchemaVersion,
		Repo:          e.repo,
		Base:          e.Base,
		Head:          e.Head,
		AvalVersion:   cmp.Or(e.avalVersion, "devel"),
		GeneratedAt:   e.generatedAt,
		Mode:          string(e.Input.Mode()),
		Tier:          int(e.Input.Tier()),
		Changes:       e.Changes,
		Obligations:   e.Input.Obligations,
		Checks:        e.Checks,
		Scope:         e.Input.Scope,
		Tamper:        e.Input.Tamper,
		Approvals:     e.Input.Approvals,
		Verdict:       verdict,
		NotCollected:  slices.Clone(e.notCollected),
	}
}

// Save writes b to <root>/.aval/evidence/<head>.json and the status the agent
// hooks read, keyed by the working tree as it was when Collect started
// (ADR-0005 §7). It refuses a bundle that does not validate: a bundle nobody
// can read is worse than none.
func (e *Evidence) Save(b evidence.Bundle) error {
	if err := b.Validate(); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("verify: encode bundle: %w", err)
	}
	name := filepath.Join(e.Root, filepath.FromSlash(EvidencePath(b.Head)))
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	// Git ignores the directory: a committed bundle would change the key it
	// is stored under, and hook.CurrentKey already leaves it out.
	if err := os.WriteFile(filepath.Join(filepath.Dir(name), ".gitignore"), []byte("*\n"), 0o600); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if err := os.WriteFile(name, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	status := hook.Status{Key: e.key, Passed: b.Verdict.Result != evidence.ResultBlock, VerifiedAt: time.Now().UTC()}
	if err := hook.WriteStatus(e.Root, status); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}

// collector holds one collection's state: what each phase read, for the next
// one to use.
type collector struct {
	o  Options
	g  *git.Runner // hardened git, in the repository root
	ev *Evidence

	tmp     string // temporary root of the base tree and the worktree
	baseDir string // the base tree, extracted from git objects

	baseline  baseline.Baseline
	baseSpecs *openspec.Repo // nil when the base has no openspec/
	baseDecls []testsource.Declaration
	// baseLint is the content of the base .golangci.yml, nil when it has
	// none, and lintFile the copy the ratchet writes inside the repository.
	baseLint []byte
	lintFile string

	headSpecs *openspec.Repo
	headDecls []testsource.Declaration
	module    string // module path of head's go.mod

	changed []string // the paths base..head touches
	deltas  map[obligation.ID]deltaOf
	work    *overlay.Worktree // nil when no obligation needs fail-before

	report gotest.Report // the full run at head
	matrix trace.Matrix
	closed bool
}

func newCollector(ctx context.Context, o Options) (*collector, error) {
	dir := cmp.Or(o.Dir, ".")
	if err := git.New(dir).CheckVersion(ctx); err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	// The key comes first: it must describe the working tree before verify
	// writes the bundle or the status into it (ADR-0005 §7).
	root, key, err := hook.CurrentKey(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	c := &collector{
		o: o, g: git.New(root),
		ev: &Evidence{
			Root: root, Changes: []string{}, Checks: []evidence.Check{},
			PolicySource: "the base commit has no root aval.yaml",
			notCollected: slices.Clone(notCollected),
			repo:         o.Repo, avalVersion: o.AvalVersion,
			generatedAt: time.Now().UTC(), key: key,
		},
		baseline: baseline.Empty(),
	}
	if c.ev.Base, err = c.commit(ctx, o.Base); err != nil {
		return nil, err
	}
	if c.ev.Head, err = c.commit(ctx, o.Head); err != nil {
		return nil, err
	}
	if c.tmp, err = os.MkdirTemp(tempRoot(o.TempDir), "aval-verify-"); err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	c.baseDir = filepath.Join(c.tmp, "base-tree")
	return c, nil
}

func (c *collector) collect(ctx context.Context) (*Evidence, error) {
	if err := c.readGit(ctx); err != nil {
		return nil, err
	}
	if err := c.runHead(ctx); err != nil {
		return nil, err
	}
	c.assemble()
	return c.ev, nil
}

// close removes the fail-before worktree, the base configuration the ratchet
// wrote inside the repository and the temporary directory. It does nothing
// after the first call.
func (c *collector) close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	var errs []error
	if c.work != nil {
		errs = append(errs, c.work.Close())
	}
	if c.lintFile != "" {
		if err := os.Remove(c.lintFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(c.tmp); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("verify: clean up: %w", err)
	}
	return nil
}

// readGit runs phase 1: everything aval derives from git, in two waves,
// before any of head's code executes.
func (c *collector) readGit(ctx context.Context) error {
	first, ctx1 := c.group(ctx)
	first.Go(func() error { return c.readBaseTree(ctx1) })
	first.Go(func() error {
		var err error
		c.headSpecs, err = loadSpecs(ctx1, os.DirFS(c.ev.Root))
		if c.headSpecs == nil {
			c.headSpecs = &openspec.Repo{} // no openspec/ at head: no obligations
		}
		return err
	})
	first.Go(func() error {
		var err error
		c.headDecls, err = scanTests(c.ev.Root)
		return err
	})
	first.Go(func() error {
		var err error
		c.changed, err = c.diff(ctx1)
		return err
	})
	first.Go(func() error {
		var err error
		c.module, err = c.headModule()
		return err
	})
	if err := wait(first); err != nil {
		return err
	}

	second, ctx2 := c.group(ctx)
	second.Go(func() error {
		var err error
		c.ev.Input.Scope, err = c.classify(ctx2)
		return err
	})
	second.Go(func() error {
		var err error
		c.baseSpecs, err = loadSpecs(ctx2, os.DirFS(c.baseDir))
		return err
	})
	second.Go(func() error {
		var err error
		c.baseDecls, err = scanTests(c.baseDir)
		return err
	})
	second.Go(func() error { return c.validateSpecs(ctx2) })
	if err := wait(second); err != nil {
		return err
	}

	// Last, because it needs both trees: which changes the range touches, what
	// their deltas claim and what tier they carry at each end.
	changes, deltas, err := c.touched()
	if err != nil {
		return err
	}
	c.ev.Input.Changes, c.deltas = changes, deltas
	for _, ch := range changes {
		c.ev.Changes = append(c.ev.Changes, ch.ID)
	}
	return nil
}

// group returns an errgroup bounded by Options.Concurrency.
func (c *collector) group(ctx context.Context) (*errgroup.Group, context.Context) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cmp.Or(c.o.Concurrency, runtime.GOMAXPROCS(0)))
	return g, ctx
}

// wait returns the first error of g's goroutines. They only return aval's own
// errors, which already carry their context.
func wait(g *errgroup.Group) error {
	return g.Wait() //nolint:wrapcheck // the goroutines wrap their own errors
}

// readBaseTree extracts the base commit into a temporary directory and reads
// from it every input the gate takes from the base: the policy, the baseline,
// the lint configuration, and later the specs and declarations. Reading them
// from git objects, and now, is what keeps head from changing them.
func (c *collector) readBaseTree(ctx context.Context) error {
	if err := extractTree(ctx, c.g, c.ev.Base, c.baseDir); err != nil {
		return err
	}
	if c.ev.Input.Policy == nil {
		data, ok, err := c.baseFile("aval.yaml")
		if err != nil {
			return err
		}
		if ok {
			m, err := manifest.ParseRepo(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("%w: aval.yaml at %s: %w", ErrUsage, c.ev.Base, err)
			}
			c.ev.Input.Policy, c.ev.PolicySource = &m, "aval.yaml at "+c.ev.Base
		}
	}
	data, ok, err := c.baseFile(baseline.Path)
	if err != nil {
		return err
	}
	if ok {
		if c.baseline, err = baseline.Parse(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("%w: at %s: %w", ErrUsage, c.ev.Base, err)
		}
	}
	// The bytes, not the path: the temporary tree is still on disk when
	// head's tests run, and one of them could rewrite the file there.
	if data, ok, err := c.baseFile(lintConfig); err != nil {
		return err
	} else if ok {
		c.baseLint = data
	}
	return nil
}

// classify classifies every commit of the range with the base policy's globs.
// Without a policy no path belongs to a family (ADR-0005 §4, no_base_policy).
func (c *collector) classify(ctx context.Context) ([]evidence.Commit, error) {
	var paths manifest.Paths
	if p := c.ev.Input.Policy; p != nil {
		paths = p.Paths
	}
	commits, err := scope.Classify(ctx, c.ev.Root, c.ev.Base, c.ev.Head, paths)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	return commits, nil
}

// validateSpecs runs openspec validate, but only when the pull request
// touches openspec/ and the base pins a version to run (ADR-0005 §1).
func (c *collector) validateSpecs(ctx context.Context) error {
	if !slices.ContainsFunc(c.changed, func(f string) bool {
		return f == openspec.Dir || strings.HasPrefix(f, openspec.Dir+"/")
	}) {
		return nil
	}
	p := c.ev.Input.Policy
	if p == nil {
		// Only the base pins the version, and head's pin is not trusted.
		c.ev.notCollected = append(c.ev.notCollected, notValidated)
		return nil
	}
	validate := openspec.Validate
	if c.o.validate != nil {
		validate = c.o.validate
	}
	start := time.Now()
	report, err := validate(ctx, c.ev.Root, p.OpenSpec.Version)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	c.ev.Input.Validation = &report
	c.check("openspec validate", "openspec validate --all --strict --json", 0, time.Since(start), statusOf(report.Passed()))
	return nil
}

// assemble runs phase 3: it turns what the phases read into the rest of
// gate.Input. Everything here is pure.
func (c *collector) assemble() {
	in := &c.ev.Input
	in.Head = c.ev.Head
	in.Baseline = c.baseline
	in.Approvals = c.o.Approvals
	in.SpecFindings = c.newFindings()
	in.Tamper = c.tamper()
	in.UnboundFailures = unboundFailures(c.report)
	for _, o := range c.matrix.Orphans {
		if o.Kind == trace.OrphanRuntime {
			in.UndeclaredRuntime = append(in.UndeclaredRuntime, o.ID)
		}
	}
	for _, b := range c.matrix.BuildFailures {
		in.BuildFailures = append(in.BuildFailures, b.Package)
	}
}

// tamper collects every tampering signal: the declarations that changed
// outside their delta, and the edits to the files that decide the policy
// (ADR-0005 §4).
func (c *collector) tamper() []evidence.Finding {
	changed := make(map[obligation.ID]bool, len(c.deltas))
	for id := range c.deltas {
		changed[id] = true
	}
	out := testsource.Compare(c.baseDecls, c.headDecls, changed)
	for _, f := range c.changed {
		kind := evidence.PolicyEdited
		switch {
		case f == baseline.Path:
			kind = evidence.BaselineEdited
		// .gitattributes at any depth: it steers what git reports, so an edit
		// of one is an edit of the policy's own machinery.
		case f == "aval.yaml", f == lintConfig, strings.HasPrefix(f, ".github/"),
			slices.Contains(codeowners.Locations(), f), path.Base(f) == ".gitattributes":
		default:
			continue
		}
		out = append(out, evidence.Finding{Kind: kind, Detail: "the pull request edits " + f})
	}
	return out
}

// commit resolves rev to the full SHA of a commit.
func (c *collector) commit(ctx context.Context, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%w: %q is not a revision", ErrUsage, rev)
	}
	out, err := c.g.Run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	var gerr *git.Error
	if errors.As(err, &gerr) && gerr.ExitCode == 1 { // --quiet: exit 1 and no message
		return "", fmt.Errorf("%w: %q names no commit", ErrUsage, rev)
	}
	if err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// diff lists the paths the range touches, old and new names of a rename
// alike, with the submodules' own settings ignored.
func (c *collector) diff(ctx context.Context) ([]string, error) {
	out, err := c.g.Run(ctx, nil, "diff-tree", "-r", "-z", "--no-commit-id", "--no-renames",
		"--name-only", "--ignore-submodules=none", "--end-of-options", c.ev.Base, c.ev.Head)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	var paths []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			paths = append(paths, f)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

// headModule returns the module path of head's go.mod, which says which
// package a declaration belongs to. Without one nothing matches, and go test
// reports the setup failure.
func (c *collector) headModule() (string, error) {
	data, err := os.ReadFile(filepath.Join(c.ev.Root, "go.mod")) //nolint:gosec // the repository root aval was pointed at
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "module" {
			continue
		}
		if p, err := strconv.Unquote(f[1]); err == nil {
			return p, nil
		}
		return f[1], nil
	}
	return "", nil
}

// baseFile reads name from the extracted base tree. A missing file is not an
// error: a base may have no policy, no baseline and no lint configuration.
func (c *collector) baseFile(name string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(c.baseDir, filepath.FromSlash(name))) //nolint:gosec // a fixed name in aval's own temporary directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("verify: read %s at %s: %w", name, c.ev.Base, err)
	}
	return data, true, nil
}

// check appends one verifier run to the bundle's checks.
func (c *collector) check(name, command string, exitCode int, took time.Duration, status evidence.Status) {
	c.ev.Checks = append(c.ev.Checks, evidence.Check{
		Name: name, Command: command, ExitCode: exitCode,
		DurationMS: max(took.Milliseconds(), 0), Status: status,
	})
}

// loadSpecs reads the OpenSpec tree at the root of fsys, and returns nil for
// a repository without openspec/: a pull request may adopt aval before it
// writes a spec, and a base without specs is history, not a tree the pull
// request archived (openspec.CheckOptions.Base).
func loadSpecs(ctx context.Context, fsys fs.FS) (*openspec.Repo, error) {
	r, err := openspec.Load(ctx, fsys)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, manifest.ErrInvalid):
		return nil, fmt.Errorf("%w: %w", ErrUsage, err)
	case err != nil:
		return nil, fmt.Errorf("verify: %w", err)
	}
	return r, nil
}

// scanTests reads the obligation IDs the _test.go files under dir declare.
func scanTests(dir string) ([]testsource.Declaration, error) {
	decls, err := testsource.Scan(dir)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	return decls, nil
}

// testEnv is the environment go test and the tests run with: aval's own,
// without the repository variables a git hook exports, so that a test which
// runs git writes to its own repository and not to the caller's index.
func (c *collector) testEnv() []string { return gitenv.Clean(os.Environ()) }

// tempRoot returns the directory the base tree and the worktree go in.
func tempRoot(dir string) string { return cmp.Or(dir, os.Getenv("RUNNER_TEMP"), os.TempDir()) }

// statusOf turns a pass or fail into a status.
func statusOf(passed bool) evidence.Status {
	if passed {
		return evidence.Pass
	}
	return evidence.Fail
}

// source names where an obligation is defined, for the bundle.
func source(p string, line int, name string) string {
	return fmt.Sprintf("%s:%d %s", p, line, strings.TrimSpace(name))
}

// command renders a go test command line the way it was run.
func command(o gotest.Options) string { return "go " + strings.Join(o.Args(), " ") }

// classify marks err as a tool or a usage error, so that the CLI maps it to
// an exit code without knowing which package it came from (ADR-0005 §6).
func classify(err error) error {
	switch {
	case err == nil, errors.Is(err, ErrTool), errors.Is(err, ErrUsage):
		return err
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, git.ErrToolMissing),
		errors.Is(err, overlay.ErrToolMissing), errors.Is(err, openspec.ErrToolMissing):
		return fmt.Errorf("%w: %w", ErrTool, err)
	case errors.Is(err, scope.ErrInvalidRange), errors.Is(err, overlay.ErrRevision),
		errors.Is(err, manifest.ErrInvalid), errors.Is(err, baseline.ErrInvalid):
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	return err
}

// EvidencePath is where the bundle for commit head goes, relative to the
// repository root (ADR-0005 §7).
func EvidencePath(head string) string { return path.Join(EvidenceDir, head+".json") }
