// Package codeowners reads a GitHub CODEOWNERS file and answers who owns a
// path, with GitHub's semantics: patterns follow gitignore rules and the last
// matching pattern wins.
//
// Only individual users (@login) count as owners in v0. Teams (@org/team)
// need a token with read:org to expand, and emails need a user lookup, so both
// are accepted and ignored (ADR-0005 §5). A line that names only teams or
// emails still matches, so it still overrides earlier lines: the path ends up
// with no individual owner, never with a stale one.
package codeowners

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MaxSize is the size from which GitHub does not load a CODEOWNERS file at
// all ("must be under 3 MB").
const MaxSize = 3 << 20

// ErrTooLarge means the file is too large for GitHub to load, so GitHub
// assigns no code owners from it.
var ErrTooLarge = errors.New("CODEOWNERS file is 3 MB or larger")

// Locations returns where GitHub looks for a CODEOWNERS file, in search
// order. Only the first one that exists is used.
func Locations() []string {
	return []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}
}

// Rules is a parsed CODEOWNERS file. The zero value owns nothing.
type Rules struct {
	rules []rule
}

type rule struct {
	re     *regexp.Regexp
	owners []string // individual users, as written ("@login")
}

var (
	userRE = regexp.MustCompile(`^@[A-Za-z0-9][A-Za-z0-9_-]*$`)
	teamRE = regexp.MustCompile(`^@[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
	mailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)
)

// Parse reads a CODEOWNERS file. Like GitHub, it skips every line it cannot
// use instead of failing: a negated pattern (!), a character range ([ ]), a
// pattern that starts with an escaped # (GitHub documents that escaping # does
// not work), a segment that mixes ** with other characters, or an owner that
// is not a user, team or email. The only error is
// a file GitHub would not load.
func Parse(data []byte) (Rules, error) {
	if len(data) >= MaxSize {
		return Rules{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	var rs Rules
	for line := range strings.Lines(string(data)) {
		if r, ok := parseLine(strings.TrimRight(line, "\r\n")); ok {
			rs.rules = append(rs.rules, r)
		}
	}
	return rs, nil
}

func parseLine(line string) (rule, bool) {
	f := fields(line)
	if len(f) == 0 {
		return rule{}, false
	}
	pattern := f[0]
	if strings.HasPrefix(pattern, `\#`) || strings.HasPrefix(pattern, "!") || strings.ContainsAny(pattern, "[]") {
		return rule{}, false
	}
	re, err := compile(pattern)
	if err != nil {
		return rule{}, false
	}
	r := rule{re: re}
	for _, o := range f[1:] {
		switch {
		case teamRE.MatchString(o), mailRE.MatchString(o):
			// Not an individual user: ignored in v0.
		case userRE.MatchString(o):
			r.owners = append(r.owners, o)
		default:
			return rule{}, false
		}
	}
	return r, true
}

// fields splits a line into its pattern and owners. Unescaped spaces and tabs
// separate fields, and an unescaped # that starts a field opens a comment
// that runs to the end of the line. Escapes are kept for compile.
func fields(line string) []string {
	var (
		out     []string
		cur     strings.Builder
		escaped bool
	)
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			cur.WriteRune(r)
			escaped = true
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		case r == '#' && cur.Len() == 0:
			return out
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// compile turns a gitignore-style pattern into a regexp over slash-separated
// paths relative to the repository root:
//   - a leading / or a / in the middle anchors the pattern at the root;
//     otherwise it matches at any depth;
//   - a trailing / matches only directories, and so only what is below them;
//   - * and ? never cross a /, and ** alone in a segment spans any number of
//     directories; ** mixed with other characters is an error;
//   - a pattern ending in a bare * matches files directly inside that
//     directory only: docs/* does not own docs/a/b.md (GitHub's docs say so);
//   - any other pattern also owns everything below a directory it matches.
func compile(pattern string) (*regexp.Regexp, error) {
	anchored := strings.HasPrefix(pattern, "/")
	p := strings.TrimPrefix(pattern, "/")
	dirOnly := strings.HasSuffix(p, "/")
	segs := strings.Split(strings.TrimSuffix(p, "/"), "/")
	for _, seg := range segs {
		if seg == "" || (seg != "**" && strings.Contains(seg, "**")) {
			return nil, fmt.Errorf("pattern %q has an empty segment or a stray **", pattern)
		}
	}
	if !anchored && len(segs) == 1 {
		segs = append([]string{"**"}, segs...)
	}
	segs = slices.CompactFunc(segs, func(a, b string) bool { return a == "**" && b == "**" })

	var b strings.Builder
	b.WriteString(`\A`)
	needSlash := false
	last := len(segs) - 1
	for i, seg := range segs {
		switch {
		case seg == "**" && last == 0:
			b.WriteString(`.+`)
		case seg == "**" && i == 0:
			b.WriteString(`(?:.+/)?`)
		case seg == "**" && i == last:
			b.WriteString(`/.+`)
		case seg == "**":
			b.WriteString(`(?:/.+)?`)
		default:
			if needSlash {
				b.WriteByte('/')
			}
			needSlash = true
			if seg == "*" {
				b.WriteString(`[^/]+`)
			} else if err := writeGlob(&b, seg); err != nil {
				return nil, fmt.Errorf("pattern %q: %w", pattern, err)
			}
		}
	}
	switch {
	case dirOnly:
		b.WriteString(`/.+`) // what matched is a directory: own what is below it
	case segs[last] != "*" && segs[last] != "**":
		b.WriteString(`(?:/.*)?`) // the file itself, or everything below a directory
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	return re, nil
}

// writeGlob writes one path segment as a regexp: * and ? stay within the
// segment, and a backslash makes the next character literal.
func writeGlob(b *strings.Builder, seg string) error {
	escaped := false
	for _, r := range seg {
		switch {
		case escaped:
			b.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
		case r == '\\':
			escaped = true
		case r == '*':
			b.WriteString(`[^/]*`)
		case r == '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	if escaped {
		return errors.New("trailing backslash")
	}
	return nil
}

// Owners returns the individual users who own path, as written in the file
// ("@login"), from the last line whose pattern matches it. It returns none
// when no line matches, when that line lists no owners (which clears
// ownership), or when it lists only teams or emails. Paths are
// slash-separated, relative to the repository root, and case-sensitive.
func (r Rules) Owners(path string) []string {
	path = strings.TrimPrefix(path, "/")
	for i := len(r.rules) - 1; i >= 0; i-- {
		if r.rules[i].re.MatchString(path) {
			return slices.Clone(r.rules[i].owners)
		}
	}
	return nil
}
