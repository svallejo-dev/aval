package openspec

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// OpenSpec's regular expressions are JavaScript. Two of its tokens do not
// mean the same in Go, so they are spelled out: \s also matches Unicode
// spaces such as NBSP, and '.' stops at U+2028 and U+2029. A literal port
// would miss a header written with U+00A0 after "###", which OpenSpec reads.
const (
	sp  = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
	dot = `[^\n\r\x{2028}\x{2029}]`
)

// caseless spells an ASCII word as a case-insensitive pattern the way a
// JavaScript /i regular expression reads it. Go's (?i) folds Unicode too, so
// it would let U+017F (long s) stand for "s" where OpenSpec does not.
func caseless(word string) string {
	var b strings.Builder
	for _, r := range word {
		if lo, up := strings.ToLower(string(r)), strings.ToUpper(string(r)); lo != up {
			b.WriteString("[" + up + lo + "]")
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	return b.String()
}

var (
	fenceOpen  = regexp.MustCompile(`^` + sp + "*(`{3,}|~{3,})")
	fenceClose = regexp.MustCompile(`^` + sp + "*(`{3,}|~{3,})" + sp + `*$`)
	// requirementHeader is OpenSpec's /^###\s*Requirement:\s*(.+)\s*$/i, with
	// the text before the name captured so rule 1 can demand the canonical form.
	requirementHeader = regexp.MustCompile(`^(###` + sp + `*` + caseless("Requirement:") + sp + `*)(` + dot + `+)` + sp + `*$`)
	requirementsTitle = regexp.MustCompile(`^##` + sp + `+` + caseless("Requirements") + sp + `*$`)
	deltaTitle        = regexp.MustCompile(`^##` + sp + `+(` + dot + `+)$`)
	topHeading        = regexp.MustCompile(`^##` + sp)
	anyHeading        = regexp.MustCompile(`^#{1,6}` + sp)
	// upperHeading is a heading of level 1 to 3, bare ones included: OpenSpec's
	// main-spec reader ends a requirement there.
	upperHeading    = regexp.MustCompile(`^#{1,3}` + sp)
	scenarioHeading = regexp.MustCompile(`^####` + sp + `+`)
	scenarioEnd     = regexp.MustCompile(`^#{1,4}` + sp)
	scenarioPrefix  = regexp.MustCompile(`^` + caseless("Scenario:") + sp + `*`)
	h3Prefix        = regexp.MustCompile(`^###` + sp + `*`)
	closingHashes   = regexp.MustCompile(`[ \t]+#+[ \t]*$`)
	metadataLine    = regexp.MustCompile(`^\*\*[^*]+\*\*:`)
	// removedBullet and renameLine are case-sensitive in OpenSpec, unlike
	// requirementHeader.
	removedBullet = regexp.MustCompile(`^` + sp + `*[-*+]` + sp + "*`?" +
		`(###` + sp + `*Requirement:` + sp + `*)(` + dot + `+?)` + "`?" + sp + `*$`)
	renameLine = regexp.MustCompile(`^` + sp + `*[-*+]?` + sp + `*(FROM|TO):` + sp + "*`?" +
		`(###` + sp + `*Requirement:` + sp + `*)(` + dot + `+?)` + "`?" + sp + `*$`)
)

// canonicalHeader is the only header prefix aval accepts (ADR-0002, rule 1).
const canonicalHeader = "### Requirement: "

// maxNameRunes is where a requirement name stops being "under 50
// characters", as OpenSpec's conventions ask (ADR-0002, rule 1).
const maxNameRunes = 50

// isJSSpace reports whether JavaScript's String.prototype.trim removes r.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trimJS(s string) string { return strings.TrimFunc(s, isJSSpace) }

// normalizeName is OpenSpec's normalizeRequirementName: only a closing "#"
// run preceded by a blank is dropped, so "C#" keeps its "#".
func normalizeName(raw string) string { return trimJS(closingHashes.ReplaceAllString(raw, "")) }

// doc is one markdown file split into lines, with a mask of the lines inside
// fenced code blocks (the fence lines included).
type doc struct {
	path  string
	lines []string
	fence []bool
}

// newDoc reads src as OpenSpec does: a leading BOM is dropped and CRLF or a
// lone CR ends a line.
func newDoc(path string, src []byte) *doc {
	s := strings.TrimPrefix(string(src), "\ufeff")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	d := &doc{path: path, lines: strings.Split(s, "\n")}
	d.fence = make([]bool, len(d.lines))
	var marker byte
	var width int // 0 while no fence is open
	for i, l := range d.lines {
		if width == 0 {
			if m := fenceOpen.FindStringSubmatch(l); m != nil {
				marker, width = m[1][0], len(m[1])
				d.fence[i] = true
			}
			continue
		}
		d.fence[i] = true
		if m := fenceClose.FindStringSubmatch(l); m != nil && m[1][0] == marker && len(m[1]) >= width {
			width = 0
		}
	}
	return d
}

// is reports whether line i is outside a fence and matches re.
func (d *doc) is(i int, re *regexp.Regexp) bool { return !d.fence[i] && re.MatchString(d.lines[i]) }

// find returns the first line in [from, to) that is(re), or -1.
func (d *doc) find(from, to int, re *regexp.Regexp) int {
	for i := from; i < to; i++ {
		if d.is(i, re) {
			return i
		}
	}
	return -1
}

// block is the line range [header, end) of one requirement.
type block struct{ header, end int }

// blocks splits the section lines [from, to) into requirement blocks as
// OpenSpec's requirement-blocks.js does: a block runs from its header to the
// next one. (OpenSpec also ends a block at a bare "## " line; requirement
// stops reading there anyway.) It also returns every other heading of level 1
// to 3: the delta reader skips or folds them into the block above, while the
// main-spec reader ends the requirement there, so validate and archive, or
// archive and show, disagree about the requirement.
func (d *doc) blocks(from, to int) (bs []block, strays []int) {
	open := -1
	closeAt := func(end int) {
		if open >= 0 {
			bs = append(bs, block{open, end})
			open = -1
		}
	}
	for i := from; i < to; i++ {
		switch {
		case d.is(i, requirementHeader):
			closeAt(i)
			open = i
		case d.is(i, upperHeading):
			strays = append(strays, i)
		}
	}
	closeAt(to)
	return bs, strays
}

// requirement reads block b. A definition (a main spec requirement, an ADDED
// block) is also held to the name-length convention.
func (d *doc) requirement(b block, definition bool) (Requirement, []Finding) {
	m := requirementHeader.FindStringSubmatch(d.lines[b.header])
	q := d.newRequirement(b.header, m[2])
	end := d.find(b.header+1, b.end, upperHeading)
	if end < 0 {
		end = b.end
	}
	q.Text, q.Characterization = d.body(b.header+1, end)
	if q.Text == "" {
		q.Text = trimJS(h3Prefix.ReplaceAllString(d.lines[b.header], ""))
	}
	q.Scenarios = d.scenarios(b.header+1, end)
	return q, checkHeader(q, m[1], m[2], definition)
}

func (d *doc) newRequirement(line int, raw string) Requirement {
	q := Requirement{Name: normalizeName(raw), Path: d.path, Line: line + 1}
	if id, title, ok := obligation.FromRequirementName(q.Name); ok {
		q.ID, q.Title = id, title
	}
	return q
}

// body mirrors OpenSpec's extractRequirementBody over lines [from, to): the
// trimmed, non-blank lines before the first heading, where `**key**:`
// metadata lines count only if nothing else does.
func (d *doc) body(from, to int) (text string, characterization bool) {
	var captured, metadata []string
	for i := from; i < to; i++ {
		if d.fence[i] {
			continue
		}
		if anyHeading.MatchString(d.lines[i]) {
			break
		}
		t := trimJS(d.lines[i])
		switch {
		case t == "":
		case metadataLine.MatchString(t):
			metadata = append(metadata, t)
			if v, ok := strings.CutPrefix(t, "**aval**:"); ok && trimJS(v) == "characterization" {
				characterization = true
			}
		default:
			captured = append(captured, t)
		}
	}
	if len(captured) > 0 {
		return strings.Join(captured, "\n"), characterization
	}
	return strings.Join(metadata, "\n"), characterization
}

// scenarios reads the `#### ` headings of a requirement section [from, to).
// Each body runs to the next heading of level 4 or above; a heading with an
// empty body is not a scenario, as in OpenSpec. OpenSpec's main-spec reader
// would also count a `#####` heading placed directly under the requirement;
// aval, like OpenSpec's delta reader, does not.
func (d *doc) scenarios(from, to int) []Scenario {
	var out []Scenario
	for i := from; i < to; i++ {
		if !d.is(i, scenarioHeading) {
			continue
		}
		end := d.find(i+1, to, scenarioEnd)
		if end < 0 {
			end = to
		}
		text := trimJS(strings.Join(d.lines[i+1:end], "\n"))
		if text == "" {
			continue
		}
		name := closingHashes.ReplaceAllString(scenarioHeading.ReplaceAllString(d.lines[i], ""), "")
		name = trimJS(scenarioPrefix.ReplaceAllString(name, ""))
		out = append(out, Scenario{Name: name, Line: i + 1, Text: text})
	}
	return out
}

// strays reports headings of level 1 to 3 that are not requirement headers
// inside a requirement section (ADR-0002, rule 4).
func (d *doc) strays(lines []int, section string) []Finding {
	out := make([]Finding, 0, len(lines))
	for _, i := range lines {
		out = append(out, Finding{Severity: SeverityError, Rule: RuleStrayHeading, Path: d.path, Line: i + 1,
			Message: fmt.Sprintf("heading %q in %s is not a %q header: OpenSpec's readers disagree about where the requirements around it end",
				trimJS(d.lines[i]), section, strings.TrimSpace(canonicalHeader))})
	}
	return out
}

// checkHeader applies the single-header rules of ADR-0002, rule 1. prefix is
// the header text before the name and raw the name before normalization.
func checkHeader(q Requirement, prefix, raw string, definition bool) []Finding {
	var out []Finding
	add := func(s Severity, rule Rule, format string, args ...any) {
		out = append(out, q.finding(s, rule, fmt.Sprintf(format, args...)))
	}
	if prefix != canonicalHeader {
		add(SeverityError, RuleLooseHeader, "header %q must start with exactly %q: OpenSpec's main-spec reader does not see other forms",
			strings.TrimSpace(prefix+raw), canonicalHeader)
	}
	if closingHashes.MatchString(raw) {
		add(SeverityError, RuleTrailingHash, "requirement name %q ends in a closing \"#\" run, which archive copies into the main spec", trimJS(raw))
	}
	if strings.Contains(q.Name, "`") {
		add(SeverityError, RuleBacktick, "requirement name %q contains a backtick, which RENAMED uses as a delimiter", q.Name)
	}
	if q.ID.IsZero() {
		add(SeverityError, RuleInvalidID, "requirement name %q must start with an obligation ID (<CTX>-<K><NN>, e.g. ORD-F01) followed by a title", q.Name)
	}
	if n := utf8.RuneCountInString(q.Name); definition && n >= maxNameRunes {
		add(SeverityWarn, RuleNameLength, "requirement name %q has %d characters; OpenSpec's conventions ask for fewer than %d", q.Name, n, maxNameRunes)
	}
	return out
}

// parseSpec reads a main spec: the requirements of its `## Requirements`
// section, as OpenSpec's archive and validate read them.
func parseSpec(capability, path string, src []byte) (Spec, []Finding) {
	d := newDoc(path, src)
	s := Spec{Capability: capability, Path: path}
	start := d.find(0, len(d.lines), requirementsTitle)
	if start < 0 {
		return s, nil
	}
	end := d.find(start+1, len(d.lines), topHeading)
	if end < 0 {
		end = len(d.lines)
	}
	bs, strays := d.blocks(start+1, end)
	if end < len(d.lines) && trimJS(d.lines[end]) == "##" {
		// A bare "## " ends the section for archive but not for show.
		strays = append(strays, end)
	}
	found := d.strays(strays, "## Requirements")
	for _, b := range bs {
		q, f := d.requirement(b, true)
		s.Requirements = append(s.Requirements, q)
		found = append(found, f...)
	}
	return s, found
}

// deltaSections are the section titles of a delta spec, in the order
// OpenSpec's show reports them. OpenSpec compares titles with toLowerCase,
// which folds only ASCII letters to ASCII.
type deltaSection struct {
	op    Op
	title *regexp.Regexp
}

var deltaSections = []deltaSection{
	{Added, regexp.MustCompile(`^` + caseless("ADDED Requirements") + `$`)},
	{Modified, regexp.MustCompile(`^` + caseless("MODIFIED Requirements") + `$`)},
	{Removed, regexp.MustCompile(`^` + caseless("REMOVED Requirements") + `$`)},
	{Renamed, regexp.MustCompile(`^` + caseless("RENAMED Requirements") + `$`)},
}

func isDeltaTitle(title string) bool {
	return slices.ContainsFunc(deltaSections, func(s deltaSection) bool { return s.title.MatchString(title) })
}

// parseDelta reads a change's delta spec for capability. Sections with the
// same title (compared case-insensitively) are merged in document order, and
// the deltas come out as OpenSpec's show reports them: ADDED, MODIFIED,
// REMOVED, then RENAMED.
func parseDelta(capability, path string, src []byte) ([]Delta, []Finding) {
	d := newDoc(path, src)
	type section struct {
		title    string
		from, to int
	}
	var sections []section
	for i, l := range d.lines {
		if d.fence[i] {
			continue
		}
		if m := deltaTitle.FindStringSubmatch(l); m != nil {
			if n := len(sections); n > 0 {
				sections[n-1].to = i
			}
			sections = append(sections, section{title: trimJS(m[1]), from: i + 1, to: len(d.lines)})
		}
	}
	var deltas []Delta
	var found []Finding
	for i, s := range sections {
		// A bare "##" heading ends a delta section and hides what follows.
		if s.title == "" && i > 0 && isDeltaTitle(sections[i-1].title) {
			found = append(found, d.strays([]int{s.from - 1}, "a delta section")...)
		}
	}
	for _, op := range deltaSections {
		for _, s := range sections {
			if !op.title.MatchString(s.title) {
				continue
			}
			ds, f := d.deltaSection(op.op, capability, s.from, s.to)
			deltas = append(deltas, ds...)
			found = append(found, f...)
		}
	}
	return deltas, found
}

func (d *doc) deltaSection(op Op, capability string, from, to int) ([]Delta, []Finding) {
	bs, strays := d.blocks(from, to)
	found := d.strays(strays, "## "+string(op)+" Requirements")
	var deltas []Delta
	add := func(dl Delta, f []Finding) {
		dl.Op, dl.Capability = op, capability
		deltas = append(deltas, dl)
		found = append(found, f...)
	}
	switch op {
	case Added, Modified:
		for _, b := range bs {
			q, f := d.requirement(b, op == Added)
			add(Delta{Requirement: q}, f)
		}
	case Removed:
		// Header blocks and bullets, in document order.
		headers := make(map[int]block, len(bs))
		for _, b := range bs {
			headers[b.header] = b
		}
		for i := from; i < to; i++ {
			if b, ok := headers[i]; ok {
				q, f := d.requirement(b, false)
				add(Delta{Requirement: q}, f)
			} else if m := removedBullet.FindStringSubmatch(d.lines[i]); m != nil && !d.fence[i] {
				q := d.newRequirement(i, m[2])
				add(Delta{Requirement: q}, checkHeader(q, m[1], m[2], false))
			}
		}
	case Renamed:
		var pending *Requirement
		for i := from; i < to; i++ {
			m := renameLine.FindStringSubmatch(d.lines[i])
			if m == nil || d.fence[i] {
				continue
			}
			q := d.newRequirement(i, m[3])
			found = append(found, checkHeader(q, m[2], m[3], m[1] == "TO")...)
			switch {
			case m[1] == "FROM":
				if pending != nil {
					found = append(found, unpaired(*pending, "FROM", "TO"))
				}
				pending = &q
			case pending == nil:
				found = append(found, unpaired(q, "TO", "FROM"))
			default:
				add(Delta{From: *pending, To: q}, nil)
				pending = nil
			}
		}
		if pending != nil {
			found = append(found, unpaired(*pending, "FROM", "TO"))
		}
	}
	return deltas, found
}

func unpaired(q Requirement, side, missing string) Finding {
	return q.finding(SeverityError, RuleUnpairedRename,
		fmt.Sprintf("RENAMED %s: %q has no matching %s: line; write each FROM: line followed by its TO: line", side, q.Name, missing))
}
