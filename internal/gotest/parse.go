package gotest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/obligation"
)

// MaxOutput is how many bytes of output one test, one package or
// Report.Other keeps, and MaxReportOutput how many they all keep together.
// Output past either limit is dropped and marked as truncated.
const (
	MaxOutput       = 64 << 10
	MaxReportOutput = 16 << 20
)

// maxLine bounds one line of the stream. test2json splits long output lines,
// so real events stay far below it.
const maxLine = 1 << 20

// attrKey is the t.Attr key that binds a test to an obligation.
const attrKey = "aval.req"

// event is one test2json event, as written by `go test -json`.
type event struct {
	Action      string
	Package     string
	Test        string
	Output      string
	OutputType  string
	Key         string
	Value       string
	ImportPath  string // build events only
	FailedBuild string
}

// Parse reads a `go test -json` stream, lines that are not events included.
// It keys every event by package and test, never by position, so parallel
// tests and several packages may interleave. It fails only if r does.
func Parse(r io.Reader) (Report, error) {
	p := &parser{pkgs: map[string]*pkgState{}, builds: map[string]*capture{}, claimed: map[string]bool{},
		budget: MaxReportOutput}
	p.other.budget = &p.budget
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		p.line(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		return Report{}, fmt.Errorf("read go test -json stream: %w", err)
	}
	return p.report(), nil
}

type parser struct {
	pkgs       map[string]*pkgState
	pkgOrder   []*pkgState
	builds     map[string]*capture // build output by ImportPath
	buildOrder []string
	claimed    map[string]bool // build output some package's FailedBuild named
	other      capture
	budget     int // output bytes the report may still keep
}

type pkgState struct {
	pkg       Package
	ended     bool
	out       capture
	tests     map[string]*testState
	testOrder []*testState
	bindings  map[string][]Binding // memoized by test name
	budget    *int
}

// endStatus maps the actions that end a test to its status.
var endStatus = map[string]evidence.Status{"pass": evidence.Pass, "fail": evidence.Fail, "skip": evidence.Skipped}

type testState struct {
	name    string
	status  evidence.Status // worst of the runs that ended
	started int
	ended   int
	attrs   []string
	out     capture
}

func (p *parser) line(b []byte) {
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	var e event
	if err := json.Unmarshal(b, &e); err != nil || e.Action == "" {
		p.other.add(string(b) + "\n")
		return
	}
	switch {
	case e.Action == "build-output":
		c, ok := p.builds[e.ImportPath]
		if !ok {
			c = &capture{budget: &p.budget}
			p.builds[e.ImportPath] = c
			p.buildOrder = append(p.buildOrder, e.ImportPath)
		}
		c.add(e.Output)
	case e.Action == "build-fail":
		// The package's own fail event carries it as FailedBuild.
	case e.Package == "":
		p.other.add(e.Output)
	case e.Test == "":
		p.packageEvent(p.pkg(e.Package), e)
	default:
		p.pkg(e.Package).test(e.Test).event(e)
	}
}

func (p *parser) pkg(name string) *pkgState {
	ps, ok := p.pkgs[name]
	if !ok {
		ps = &pkgState{pkg: Package{Name: name}, tests: map[string]*testState{}, bindings: map[string][]Binding{},
			out: capture{budget: &p.budget}, budget: &p.budget}
		p.pkgs[name] = ps
		p.pkgOrder = append(p.pkgOrder, ps)
	}
	return ps
}

func (p *parser) packageEvent(ps *pkgState, e event) {
	switch e.Action {
	case "output":
		ps.pkg.NoTestFiles = ps.pkg.NoTestFiles || strings.HasSuffix(e.Output, "\t[no test files]\n")
		// cmd/go adds " [no tests to run]" to the summary only after this line.
		ps.pkg.NoTestsToRun = ps.pkg.NoTestsToRun || e.Output == "testing: warning: no tests to run\n"
		if e.OutputType != "frame" {
			ps.out.add(e.Output)
		}
	case "pass":
		ps.pkg.Status, ps.ended = evidence.Pass, true
	case "skip":
		ps.pkg.Status, ps.ended = evidence.Skipped, true
	case "fail":
		ps.pkg.Status, ps.ended = evidence.Fail, true
		if e.FailedBuild != "" {
			ps.pkg.Status, ps.pkg.FailedBuild = evidence.BuildFail, e.FailedBuild
			if c, ok := p.builds[e.FailedBuild]; ok {
				ps.out.add(c.b.String())
				p.claimed[e.FailedBuild] = true
			}
		}
	}
}

func (ps *pkgState) test(name string) *testState {
	ts, ok := ps.tests[name]
	if !ok {
		ts = &testState{name: name, out: capture{budget: ps.budget}}
		ps.tests[name] = ts
		ps.testOrder = append(ps.testOrder, ts)
	}
	return ts
}

func (ts *testState) event(e event) {
	switch e.Action {
	case "run":
		ts.started++
	case "output":
		if e.OutputType != "frame" {
			ts.out.add(e.Output)
		}
	case "attr":
		if e.Key == attrKey {
			ts.attrs = append(ts.attrs, e.Value)
		}
	case "pass", "fail", "skip":
		ts.ended++
		ts.status = worse(ts.status, endStatus[e.Action])
		if ts.status == evidence.Pass {
			ts.out.release()
		}
	}
}

func (p *parser) report() Report {
	var r Report
	w := warner{seen: map[string]bool{}}
	for _, ps := range p.pkgOrder {
		pkg := ps.pkg
		if !ps.ended {
			pkg.Status = evidence.NotRun
		}
		clean := true
		for _, ts := range ps.testOrder {
			status := ts.status
			if status == "" || ts.started > ts.ended {
				status = worse(status, evidence.NotRun)
			}
			clean = clean && (status == evidence.Pass || status == evidence.Skipped)
			r.Tests = append(r.Tests, TestOutcome{
				Package: pkg.Name, Name: ts.name, Status: status,
				Bindings: ps.bind(ts.name, &w),
				Output:   ts.out.b.String(), Truncated: ts.out.truncated,
			})
		}
		pkg.FailedOutsideTests = pkg.Status == evidence.Fail && clean
		pkg.Output, pkg.Truncated = ps.out.b.String(), ps.out.truncated
		r.Packages = append(r.Packages, pkg)
	}
	for _, path := range p.buildOrder {
		if !p.claimed[path] {
			p.other.add(p.builds[path].b.String())
		}
	}
	r.Other, r.Warnings = p.other.b.String(), w.list
	return r
}

// bind returns the bindings of test name: its nearest observed ancestor's,
// plus the ID in its outermost ID segment and its aval.req attrs, each owned
// by the test unless an ancestor already binds that ID.
func (ps *pkgState) bind(name string, w *warner) []Binding {
	if b, ok := ps.bindings[name]; ok {
		return b
	}
	var out []Binding
	if parent, ok := ps.parent(name); ok {
		out = slices.Clone(ps.bind(parent, w))
	}
	add := func(id obligation.ID) {
		if !slices.ContainsFunc(out, func(b Binding) bool { return b.ID == id }) {
			out = append(out, Binding{ID: id, Owner: name})
		}
	}
	if id, ok := nameID(name, func(kind WarningKind, prefix, segment string) {
		w.warn(Warning{Kind: kind, Package: ps.pkg.Name, Test: name, Detail: segment}, prefix)
	}); ok {
		add(id)
	}
	for _, v := range ps.tests[name].attrs {
		id, err := obligation.Parse(v)
		if err != nil {
			w.warn(Warning{Kind: InvalidAttr, Package: ps.pkg.Name, Test: name, Detail: v}, name)
			continue
		}
		add(id)
	}
	ps.bindings[name] = out
	return out
}

// parent returns the longest proper "/"-prefix of name that is a test seen
// in the stream. A "/" inside a t.Run name makes a level with no test.
func (ps *pkgState) parent(name string) (string, bool) {
	for i := strings.LastIndexByte(name, '/'); i > 0; i = strings.LastIndexByte(name[:i], '/') {
		if _, ok := ps.tests[name[:i]]; ok {
			return name[:i], true
		}
	}
	return "", false
}

// idPrefix finds what could be an ID at the start of a segment;
// obligation.Parse decides whether it is one.
var idPrefix = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[A-Z][0-9]+`)

// nameID returns the ID of the outermost segment of name that carries one.
// It reports, with the name up to the segment, IDs in deeper segments and
// segments that start with an ID but fail to bind it.
func nameID(name string, warn func(kind WarningKind, prefix, segment string)) (obligation.ID, bool) {
	var found obligation.ID
	end := 0
	for seg := range strings.SplitSeq(name, "/") {
		end += len(seg)
		prefix := name[:end]
		end++
		id, ok := obligation.FromTestSegment(seg)
		if !ok {
			if _, err := obligation.Parse(idPrefix.FindString(seg)); err == nil {
				warn(SuspiciousSegment, prefix, seg)
			}
			continue
		}
		switch {
		case found.IsZero():
			found = id
		case id != found:
			warn(NestedID, prefix, seg)
		}
	}
	return found, !found.IsZero()
}

// warner collects warnings once per kind, package and name prefix, so the
// subtests below a bad segment do not repeat it.
type warner struct {
	list []Warning
	seen map[string]bool
}

func (w *warner) warn(wa Warning, prefix string) {
	key := strings.Join([]string{string(wa.Kind), wa.Package, prefix, wa.Detail}, "\x00")
	if !w.seen[key] {
		w.seen[key] = true
		w.list = append(w.list, wa)
	}
}

// capture keeps the first MaxOutput bytes written to it, cut at a rune
// boundary, and no more than its budget allows, when it has one.
type capture struct {
	b         strings.Builder
	truncated bool
	budget    *int // shared by every capture of a report
}

func (c *capture) add(s string) {
	if c.truncated {
		return
	}
	room := MaxOutput - c.b.Len()
	if c.budget != nil {
		room = min(room, *c.budget)
	}
	if len(s) > room {
		for room > 0 && !utf8.RuneStart(s[room]) {
			room--
		}
		s, c.truncated = s[:room], true
	}
	c.b.WriteString(s)
	if c.budget != nil {
		*c.budget -= len(s)
	}
}

// release drops what c kept and returns it to the budget.
func (c *capture) release() {
	if c.budget != nil {
		*c.budget += c.b.Len()
	}
	c.b.Reset()
	c.truncated = false
}

// Write lets a capture collect a command's standard error. It never fails,
// so a chatty command is never blocked or killed by it.
func (c *capture) Write(p []byte) (int, error) {
	c.add(string(p))
	return len(p), nil
}
