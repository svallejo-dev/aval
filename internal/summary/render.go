package summary

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// out is text under construction, capped in size. Writing through it is how a
// report stays inside GitHub's step summary limit: once the limit is reached
// nothing more is added and dropped remembers it, so the renderer can say the
// report was cut instead of emitting half a table.
type out struct {
	buf     []byte
	limit   int // bytes; 0 means unlimited
	dropped bool
}

// write adds every part or none of them. A table row is written in one call,
// so a size cut always lands between rows and never inside one.
func (o *out) write(parts ...string) {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	if n == 0 {
		return
	}
	if o.dropped || (o.limit > 0 && len(o.buf)+n > o.limit) {
		o.dropped = true
		return
	}
	for _, p := range parts {
		o.buf = append(o.buf, p...)
	}
}

func (o *out) len() int       { return len(o.buf) }
func (o *out) String() string { return string(o.buf) }

// cellKind says what a piece of a line is, which is what decides how it has
// to be escaped.
type cellKind uint8

const (
	kindFixed  cellKind = iota // aval's own words: nothing to escape
	kindStrong                 // aval's own words, emphasised in Markdown
	kindProse                  // untrusted prose: a note, a message, a status
	kindCode                   // untrusted identifier: a test name, a command, a SHA
)

// cell is one piece of a line: its text and how that text must be handled.
type cell struct {
	kind cellKind
	s    string
}

// frag is a run of cells rendered as one piece of text: a whole line, one
// bullet, or one column of a table row.
type frag []cell

// fixed and strong carry aval's own words. Everything else comes from the
// bundle, so it is cleaned and capped as it enters the report: prose for free
// text, label for the short values of a closed set (a status, a family) that
// an unvalidated bundle could still fill with anything, and code for an
// identifier.
func fixed(s string) cell  { return cell{kindFixed, s} }
func strong(s string) cell { return cell{kindStrong, s} }
func prose(s string) cell  { return cell{kindProse, clamp(clean(s), maxProse)} }
func label(s string) cell  { return cell{kindProse, clamp(clean(s), maxLabel)} }
func code(s string) cell   { return cell{kindCode, clamp(clean(s), maxIdent)} }

// empty reports whether f would render as nothing.
func (f frag) empty() bool {
	for _, c := range f {
		if c.s != "" {
			return false
		}
	}
	return true
}

// escaping says where a fragment lands, which is what decides whether a pipe
// has to be escaped: inside a table cell it would add a column, and GFM reads
// it as a separator even inside a code span.
type escaping bool

const (
	inline escaping = false // a paragraph, a bullet or a heading
	inCell escaping = true  // one cell of a table row
)

func (f frag) markdown(where escaping) string {
	var o out
	for _, c := range f {
		switch c.kind {
		case kindFixed:
			o.write(c.s)
		case kindStrong:
			o.write("**", c.s, "**")
		case kindProse:
			o.write(escapeMarkdown(c.s, where))
		case kindCode:
			o.write(codeSpan(c.s, where))
		}
	}
	return o.String()
}

func (f frag) text() string {
	var o out
	for _, c := range f {
		o.write(c.s)
	}
	return o.String()
}

// dash is what an absent value renders as, so an empty cell reads as "nothing
// here" instead of as a rendering bug.
const dash = "—"

// block is one piece of a report. Every block renders in both syntaxes, so
// Markdown and Text cannot drift apart in content or in order.
type block interface {
	markdown(*out)
	text(*out)
}

// heading opens a section. Level 1 is the report's title.
type heading struct {
	level int
	f     frag
}

func (h heading) markdown(o *out) {
	o.write(strings.Repeat("#", h.level), " ", h.f.markdown(inline), "\n\n")
}

// text underlines level 1 and 2 and ends a deeper heading with a colon, so a
// CI log keeps the outline without box drawing.
func (h heading) text(o *out) {
	s := h.f.text()
	switch h.level {
	case 1:
		o.write(s, "\n", strings.Repeat("=", width(s)), "\n\n")
	case 2:
		o.write(s, "\n", strings.Repeat("-", width(s)), "\n\n")
	default:
		o.write(s, ":\n\n")
	}
}

// para is one line of prose.
type para struct{ f frag }

func (p para) markdown(o *out) { o.write(p.f.markdown(inline), "\n\n") }
func (p para) text(o *out)     { o.write(p.f.text(), "\n\n") }

// bullets is a list of items, each with its own nested sub-items.
type bullets struct{ items []item }

// item is one entry of a list: its line, and the lines nested under it.
type item struct {
	f    frag
	subs []frag
}

func (b bullets) markdown(o *out) {
	for _, it := range b.items {
		o.write("- ", it.f.markdown(inline), "\n")
		for _, s := range it.subs {
			o.write("  - ", s.markdown(inline), "\n")
		}
	}
	o.write("\n")
}

func (b bullets) text(o *out) {
	for _, it := range b.items {
		o.write("- ", it.f.text(), "\n")
		for _, s := range it.subs {
			o.write("  - ", s.text(), "\n")
		}
	}
	o.write("\n")
}

// table is a header of aval's own words and rows of untrusted columns.
type table struct {
	header []string
	rows   [][]frag
}

// column returns row's i-th column, or a dash when the row is short or the
// column empty: a table missing a cell would break its own shape.
func column(row []frag, i int) frag {
	if i < len(row) && !row[i].empty() {
		return row[i]
	}
	return frag{fixed(dash)}
}

func (t table) markdown(o *out) {
	o.write("| ", strings.Join(t.header, " | "), " |\n|", strings.Repeat(" --- |", len(t.header)), "\n")
	for _, r := range t.rows {
		cells := make([]string, len(t.header))
		for i := range cells {
			cells[i] = column(r, i).markdown(inCell)
		}
		o.write("| ", strings.Join(cells, " | "), " |\n")
	}
	o.write("\n")
}

// text lays the table out in aligned columns when the whole table fits in
// alignWidth, and as one record per row when it does not. Widths are display
// widths, not rune counts, so a CJK note or a combining mark cannot push a
// column out of line.
func (t table) text(o *out) {
	rows := make([][]string, 0, len(t.rows))
	widths := make([]int, len(t.header))
	for i, h := range t.header {
		widths[i] = width(h)
	}
	for _, r := range t.rows {
		cells := make([]string, len(t.header))
		for i := range cells {
			cells[i] = column(r, i).text()
			widths[i] = max(widths[i], width(cells[i]))
		}
		rows = append(rows, cells)
	}
	total := 2 * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	if total <= alignWidth {
		t.aligned(o, rows, widths)
		return
	}
	t.records(o, rows)
}

func (t table) aligned(o *out, rows [][]string, widths []int) {
	o.write(padded(t.header, widths), "\n")
	for _, r := range rows {
		o.write(padded(r, widths), "\n")
	}
	o.write("\n")
}

// records writes one row as its first column followed by one indented
// label: value line per remaining column. Nothing is wrapped: a plain report
// keeps one line per value so a log stays greppable (ADR-0003), and a long
// note is worth more whole than split across lines.
func (t table) records(o *out, rows [][]string) {
	for _, r := range rows {
		lines := make([]string, 0, 2*len(r))
		lines = append(lines, r[0], "\n")
		for i := 1; i < len(r); i++ {
			lines = append(lines, "  ", t.header[i], ": ", r[i], "\n")
		}
		o.write(lines...)
		o.write("\n")
	}
}

// padded joins cells two spaces apart, padding each to its column's width.
// The last cell is not padded, so no line carries trailing blanks.
func padded(cells []string, widths []int) string {
	var o out
	for i, c := range cells {
		o.write(c)
		if i < len(cells)-1 {
			o.write(strings.Repeat(" ", widths[i]-width(c)+2))
		}
	}
	return o.String()
}

// commands is a block of shell lines to copy and run. Nothing in it is
// escaped, so its content is only ever cleaned text.
type commands struct{ lines []frag }

func (c commands) markdown(o *out) {
	body := make([]string, 0, len(c.lines))
	for _, l := range c.lines {
		body = append(body, l.text(), "\n")
	}
	f := fence(body)
	o.write(f, "sh\n")
	o.write(body...)
	o.write(f, "\n\n")
}

func (c commands) text(o *out) {
	for _, l := range c.lines {
		o.write("  ", l.text(), "\n")
	}
	o.write("\n")
}

// fence returns a run of backticks longer than any run inside body, so the
// content of a fenced block can never close it early.
func fence(body []string) string {
	run, longest := 0, 2
	for _, s := range body {
		for _, r := range s {
			if r == '`' {
				run++
				longest = max(longest, run)
				continue
			}
			run = 0
		}
	}
	return strings.Repeat("`", longest+1)
}

// aside is a remark about the report itself, such as a section cut short. It
// carries aval's own words only.
type aside struct{ s string }

func (a aside) markdown(o *out) { o.write("> ", a.s, "\n\n") }
func (a aside) text(o *out)     { o.write("... ", a.s, "\n\n") }

// escByte starts a terminal escape sequence.
const escByte = 0x1b

// clean makes one safe line out of an untrusted string: it drops terminal
// escape sequences, control characters, bidirectional overrides and
// zero-width marks, replaces invalid UTF-8, turns every blank into a space
// and collapses runs of them. So a test name cannot move a terminal's cursor,
// hide itself, reverse the text around it or break a line in two.
func clean(s string) string {
	o := out{buf: make([]byte, 0, len(s))}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == escByte:
			i += escapeLen(s[i:])
			continue
		case r == utf8.RuneError && size == 1:
			o.write(string(utf8.RuneError))
		case r == '\t', r == '\n', r == '\v', r == '\f', r == '\r':
			o.write(" ")
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			// Dropped: C0 and C1 controls, DEL, the bidirectional
			// overrides and isolates, zero-width marks and the BOM.
		default:
			o.write(s[i : i+size])
		}
		i += size
	}
	return strings.Join(strings.Fields(o.String()), " ")
}

// escapeLen returns the length of the terminal escape sequence at the start of
// s, which begins with ESC: a CSI sequence up to its final byte, an OSC
// sequence up to BEL or ST, or the two bytes of a short escape. An unfinished
// sequence swallows the rest of the string, exactly as it would swallow the
// rest of a terminal line.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == escByte {
				return min(i+2, len(s))
			}
		}
	default:
		return 2
	}
	return len(s)
}

// mdMarkers is every character Markdown gives an inline meaning to that a
// backslash can take away.
const mdMarkers = "\\`*[]~"

// mdLeading are the characters that would open a block if one came first on a
// line.
const mdLeading = "-+=.#"

// GFM's extended autolinks need no delimiters at all: bare text is turned
// into a link as soon as it holds "://", starts a word with "www." or looks
// like an email address. Written as numeric character references the same
// glyphs reach the reader and no link is made.
const (
	schemeSep = "://"
	wwwPrefix = "www."
)

// escapeMarkdown makes untrusted prose inert in GitHub Flavored Markdown:
//   - &, < and > become entities, so no HTML can be injected;
//   - every inline marker is backslash-escaped, so no emphasis, code span,
//     link or strikethrough can be opened;
//   - "://", a leading "www." and "@" become character references, so no
//     extended autolink can form: a note must never put a clickable link on
//     the page where a human decides an override;
//   - in a table cell a pipe is escaped too, so a note can never add a column.
//
// An underscore between two word characters is left alone: GFM opens no
// emphasis there, and escaping it would turn every test name into a thicket of
// backslashes.
func escapeMarkdown(s string, where escaping) string {
	var o out
	var prev rune
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		next, _ := utf8.DecodeRuneInString(s[i+size:])
		www := (r == 'w' || r == 'W') && !isWord(prev) && hasPrefixFold(s[i:], wwwPrefix)
		switch {
		case r == '&':
			o.write("&amp;")
		case r == '<':
			o.write("&lt;")
		case r == '>':
			o.write("&gt;")
		case r == '@':
			o.write("&#64;") // an email address would autolink
		case r == ':' && strings.HasPrefix(s[i:], schemeSep):
			o.write(":&#47;&#47;")
			i += len(schemeSep)
			prev = '/'
			continue
		case www:
			o.write(s[i:i+len(wwwPrefix)-1], "&#46;")
			i += len(wwwPrefix)
			prev = '.'
			continue
		case r == '_' && isWord(prev) && isWord(next):
			o.write("_")
		case r == '|' && where == inline:
			o.write("|")
		case r == '_', r == '|', strings.ContainsRune(mdMarkers, r), i == 0 && strings.ContainsRune(mdLeading, r):
			o.write("\\", string(r))
		default:
			o.write(s[i : i+size])
		}
		prev = r
		i += size
	}
	return o.String()
}

// hasPrefixFold reports whether s starts with prefix, ignoring case: GFM
// matches "www." however it is capitalised.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// isWord reports whether r is a character an underscore can sit inside without
// opening emphasis.
func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// codeSpan wraps an untrusted identifier in a Markdown code span. The fence is
// one backtick longer than the longest run inside s, so the identifier cannot
// close the span early. In a table cell a pipe is escaped even inside the span,
// which is how GFM lets a cell hold one; outside a table it is left alone,
// because a code span there keeps a backslash literal.
func codeSpan(s string, where escaping) string {
	if s == "" {
		return ""
	}
	if where == inCell {
		s = strings.ReplaceAll(s, "|", `\|`)
	}
	run, longest := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	f := strings.Repeat("`", longest+1)
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return f + pad + s + pad + f
}

// clamp cuts s to at most n runes, marking the cut with an ellipsis, so one
// enormous note cannot crowd the rest of the report out of the budget.
func clamp(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n-1 {
			return s[:i] + "…"
		}
		count++
	}
	return s
}

// width is how many terminal cells s occupies: East Asian characters take two,
// a combining mark none, so a column lines up where a rune count would not.
func width(s string) int { return ansi.StringWidth(s) }

// runeIndex returns the byte index where s's n-th rune starts, or len(s).
func runeIndex(s string, n int) int {
	count := 0
	for i := range s {
		if count == n {
			return i
		}
		count++
	}
	return len(s)
}
