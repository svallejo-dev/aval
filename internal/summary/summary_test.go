package summary

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const (
	baseSHA  = "1111111111111111111111111111111111111111"
	headSHA  = "2222222222222222222222222222222222222222"
	olderSHA = "3333333333333333333333333333333333333333" // an earlier head of the same pull request
	seamSHA  = "4444444444444444444444444444444444444444"
	featSHA  = "5555555555555555555555555555555555555555"
)

var (
	genAt    = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	reviewAt = time.Date(2026, 9, 24, 11, 30, 0, 0, time.UTC)
)

// passBundle is a change with nothing to say: every rule satisfied, no
// obligation of its own, no finding, no review.
func passBundle() evidence.Bundle {
	return evidence.Bundle{
		SchemaVersion: evidence.SchemaVersion,
		Repo:          "svallejo-dev/aval-sandbox",
		Base:          baseSHA,
		Head:          headSHA,
		AvalVersion:   "v0.1.0",
		GeneratedAt:   genAt,
		Mode:          "enforce",
		Tier:          0,
		Scope: []evidence.Commit{
			{SHA: headSHA, Family: evidence.FamilyDX, Paths: []string{"Makefile"}},
		},
		Verdict: evidence.Verdict{Result: evidence.ResultPass},
	}
}

// blockBundle is a blocked tier-2 change with one obligation of each strength,
// a mixed commit, a seam commit, a tampering signal, a rejected review and
// reasons of both effects.
func blockBundle() evidence.Bundle {
	return evidence.Bundle{
		SchemaVersion: evidence.SchemaVersion,
		Repo:          "svallejo-dev/aval-sandbox",
		Base:          baseSHA,
		Head:          headSHA,
		AvalVersion:   "v0.1.0",
		GeneratedAt:   genAt,
		Mode:          "enforce",
		Tier:          2,
		Changes:       []string{"add-refunds"},
		Obligations: []evidence.Obligation{
			{ID: "ORD-F01", Kind: "F", Source: "openspec/specs/refunds/spec.md#ORD-F01 Refund is idempotent",
				Delta: evidence.Added, Tests: []string{"TestRefunds/ORD-F01_second_refund_is_a_no-op"},
				Before: evidence.Fail, After: evidence.Pass, Strength: evidence.Strong},
			{ID: "ORD-F02", Kind: "F", Source: "openspec/specs/refunds/spec.md#ORD-F02 Refund needs a charge",
				Delta: evidence.Added, Tests: []string{"TestRefunds/ORD-F02_unknown_charge", "TestRefunds/ORD-F02_empty_charge"},
				Before: evidence.BuildFail, After: evidence.Pass, Strength: evidence.Weak,
				Note: "refund.New did not exist at the base"},
			{ID: "ORD-I01", Kind: "I", Source: "openspec/specs/refunds/spec.md#ORD-I01 Totals never go negative",
				Delta: evidence.Modified, Characterization: true, Tests: []string{"TestRefunds/ORD-I01_totals"},
				Before: evidence.Pass, After: evidence.Pass, Strength: evidence.Characterized},
			{ID: "ORD-N01", Kind: "N", Source: "openspec/specs/refunds/spec.md#ORD-N01 Never refund more than charged",
				Delta: evidence.Added, Tests: []string{"TestRefunds/ORD-N01_rejects_excess"},
				Before: evidence.Pass, After: evidence.Pass, Strength: evidence.None},
			{ID: "ORD-O01", Kind: "O", Source: "openspec/changes/add-refunds/specs/refunds/spec.md#ORD-O01 Partial refunds?",
				Delta: evidence.Added, Before: evidence.NotApply, After: evidence.NotRun, Strength: evidence.None,
				Note: "no bound test: an open question is resolved by a human"},
		},
		Checks: []evidence.Check{
			{Name: "go-test", Command: "go test -json ./...", ExitCode: 0, DurationMS: 4210, Status: evidence.Pass},
			{Name: "fail-before", Command: "go test -json -run ^TestRefunds$/^ORD-F01 ./internal/refund", ExitCode: 1, DurationMS: 71_400, Status: evidence.Fail},
			{Name: "golangci-lint", Command: "golangci-lint run --new-from-merge-base=main", ExitCode: 1, DurationMS: 9120, Status: evidence.Fail, Artifact: "lint.json"},
		},
		Scope: []evidence.Commit{
			{SHA: featSHA, Family: evidence.FamilyFeat, Paths: []string{"internal/refund/refund.go"}},
			{SHA: seamSHA, Family: evidence.FamilySeam, Paths: []string{"internal/platform/git/git.go"}},
			{SHA: headSHA, Family: evidence.FamilyMixed, Families: []evidence.Family{evidence.FamilyDX, evidence.FamilyFeat},
				Paths: []string{".golangci.yml", "internal/refund/refund.go", "internal/refund/refund_test.go", "Makefile", "docs/adr/0006.md"}},
		},
		Tamper: []evidence.Finding{
			{Kind: evidence.SkipAdded, ID: "ORD-N01", Detail: "t.Skip added to TestRefunds/ORD-N01_rejects_excess"},
			{Kind: evidence.PolicyEdited, Detail: ".golangci.yml"},
		},
		Approvals: []evidence.Approval{
			{Kind: evidence.ApprovalOverride, Actor: "@dev", CommitID: olderSHA, SubmittedAt: reviewAt.Add(-time.Hour),
				Reason: "flaky regression", Valid: false, Rejection: "review of an earlier commit"},
		},
		Verdict: evidence.Verdict{Result: evidence.ResultBlock, Reasons: []evidence.Reason{
			{Code: gate.CodeFailBeforeMissing, ID: "ORD-N01", Message: "no valid fail-before (before=pass, after=pass)"},
			{Code: gate.CodeMixedCommit, Message: "commit " + headSHA + " touches dx and feat paths"},
			{Code: gate.CodeOpenQuestion, ID: "ORD-O01", Message: "added open question: a human must resolve it"},
			{Code: gate.CodeSeamTouched, Message: "feat commit " + featSHA + " touches seam paths"},
			{Code: gate.CodeTamper, Message: "policy_edited: .golangci.yml"},
			{Code: gate.CodeTamper, ID: "ORD-N01", Message: "skip_added: t.Skip added to TestRefunds/ORD-N01_rejects_excess"},
			{Code: gate.CodeWeakEvidence, ID: "ORD-F02", Message: "weak fail-before evidence (before=build_fail): refund.New did not exist at the base"},
		}},
		NotCollected: []string{"mutation", "rollback", "slo"},
	}
}

// warnBundle is the same change after a CODEOWNER overrode it: the verdict is
// a warn, and every blocking reason still stands (ADR-0005 §5).
func warnBundle() evidence.Bundle {
	b := blockBundle()
	b.Tier = 3
	b.Verdict.Result = evidence.ResultWarn
	b.Approvals = append(b.Approvals,
		evidence.Approval{Kind: evidence.ApprovalHuman, Actor: "@lead", CommitID: headSHA, SubmittedAt: reviewAt, Valid: true},
		evidence.Approval{Kind: evidence.ApprovalOverride, Actor: "@lead", CommitID: headSHA, SubmittedAt: reviewAt,
			Reason: "payment outage: the refund fix ships now and the evidence lands tomorrow", Valid: true},
	)
	return b
}

// Strings a pull request can choose, each aimed at a different renderer.
const (
	mdInjection   = "**bold** [link](http://evil.example) `code` ~~strike~~ _em_ #heading"
	htmlInjection = `<script>alert("xss")</script><img src=x onerror=alert(1)>`
	pipeInjection = "col | umn || more | cells"
	ansiInjection = "\x1b[31mred\x1b[0m\x1b]0;window title\x07 \x1b[2J\x1b[1;1H"
	controlChars  = "nul\x00 bell\x07 back\bspace vtab\x0b tab\t cr\r lf\n end"
	bidiOverride  = "start \u202e gnidne \u2066 isolate \u2069 \u200b zero \ufeff width \u00ad soft"
	backticks     = "a ``` b `` c ` d"
)

// adversarialBundle fills every string a pull request controls with something
// aimed at the renderers: Markdown markup, HTML, pipes, terminal escapes,
// control characters, bidirectional overrides, backticks and a line no
// terminal can hold.
func adversarialBundle() evidence.Bundle {
	long := strings.Repeat("longidentifier", 40)
	return evidence.Bundle{
		SchemaVersion: evidence.SchemaVersion,
		Repo:          "svallejo-dev/" + htmlInjection,
		Base:          baseSHA,
		Head:          headSHA,
		AvalVersion:   "v0.1.0 " + ansiInjection,
		GeneratedAt:   genAt,
		Mode:          "enforce",
		Tier:          1,
		Changes:       []string{"add-" + pipeInjection, mdInjection},
		Obligations: []evidence.Obligation{
			{ID: "ORD-F01", Kind: "F", Source: "spec.md#ORD-F01 " + mdInjection, Delta: evidence.Added,
				Tests:  []string{"TestX/ORD-F01_" + pipeInjection, "TestX/ORD-F01_" + backticks, "TestX/" + long},
				Before: evidence.Fail, After: evidence.Pass, Strength: evidence.Strong,
				Note: mdInjection + " " + htmlInjection + " " + bidiOverride + " " + controlChars},
			{ID: "ORD-N02", Kind: "N", Source: "spec.md#ORD-N02", Delta: evidence.Added,
				Tests: []string{long}, Before: evidence.Pass, After: evidence.Pass, Strength: evidence.None,
				Note: long},
		},
		Checks: []evidence.Check{
			{Name: "go-test " + ansiInjection, Command: "go test " + pipeInjection + " " + backticks,
				ExitCode: -1, DurationMS: 0, Status: evidence.NotRun, Artifact: htmlInjection},
		},
		Scope: []evidence.Commit{
			{SHA: headSHA, Family: evidence.FamilyMixed, Families: []evidence.Family{evidence.FamilyDX, evidence.FamilyFeat},
				Paths: []string{pipeInjection, htmlInjection, bidiOverride, long, "Makefile", "docs/adr/0006.md"}},
		},
		Tamper: []evidence.Finding{
			{Kind: evidence.FingerprintChanged, ID: "ORD-F01", Detail: "fingerprint of " + mdInjection + " changed"},
		},
		Approvals: []evidence.Approval{
			{Kind: evidence.ApprovalOverride, Actor: "@" + htmlInjection, CommitID: olderSHA, SubmittedAt: reviewAt,
				Reason: "aval:override " + mdInjection, Valid: false, Rejection: "review of an earlier commit " + ansiInjection},
		},
		Verdict: evidence.Verdict{Result: evidence.ResultBlock, Reasons: []evidence.Reason{
			{Code: gate.CodeUnverified, ID: "ORD-N02", Message: pipeInjection + " " + htmlInjection},
			{Code: "code_" + mdInjection, ID: "ORD-F01", Message: bidiOverride},
		}},
		NotCollected: []string{"mutation " + pipeInjection, htmlInjection},
	}
}

// TestGolden pins both renderings and the one-line form of each fixture. Run
// `go test ./internal/summary -update` after an intended change and read the
// diff: these files are the contract with whoever reviews a blocked pull
// request.
func TestGolden(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		b    evidence.Bundle
	}{
		{"pass", passBundle()},
		{"block", blockBundle()},
		{"warn_override", warnBundle()},
		{"adversarial", adversarialBundle()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A fixture that is not a valid bundle would prove nothing:
			// the gate never renders one it did not build.
			if err := tc.b.Validate(); err != nil {
				t.Fatalf("fixture is not a valid bundle: %v", err)
			}
			md, text := Markdown(tc.b), Text(tc.b)
			assertGolden(t, tc.name+".md", md)
			assertGolden(t, tc.name+".txt", text)
			assertGolden(t, tc.name+".line", Line(tc.b)+"\n")
			assertInert(t, "markdown", md)
			assertInert(t, "text", text)
			assertNoHTML(t, md)
			assertTables(t, md)
			assertWidth(t, text)
		})
	}
}

// TestTruncationGolden renders a report that does not fit, so the cut is
// visible: the marker says the report was cut, and the commands to reproduce
// the run survive it because their room is reserved.
func TestTruncationGolden(t *testing.T) {
	t.Parallel()

	const tight = 1200 // bytes: enough for the verdict, not for the evidence
	d := build(blockBundle())
	md := d.render(block.markdown, tight)
	text := d.render(block.text, tight)
	assertGolden(t, "truncated.md", md)
	assertGolden(t, "truncated.txt", text)
	for name, got := range map[string]string{"markdown": md, "text": text} {
		if len(got) > tight {
			t.Errorf("%s is %d bytes, want at most %d", name, len(got), tight)
		}
		if !strings.Contains(got, "Cut here") {
			t.Errorf("%s was cut with no marker:\n%s", name, got)
		}
		if !strings.Contains(got, `aval gate --base "$base"`) {
			t.Errorf("%s lost the commands that reproduce the run:\n%s", name, got)
		}
	}
}

// TestRowsTruncated: a section with more rows than a step summary can carry
// shows the first maxRows and says how many it left out.
func TestRowsTruncated(t *testing.T) {
	t.Parallel()

	b := blockBundle()
	b.Obligations = make([]evidence.Obligation, 0, maxRows+50)
	for i := range maxRows + 50 {
		b.Obligations = append(b.Obligations, evidence.Obligation{
			ID: fmt.Sprintf("ORD-F%04d", i), Kind: "F", Source: "spec.md", Delta: evidence.Added,
			Tests:  []string{fmt.Sprintf("TestRefunds/ORD-F%04d_case", i)},
			Before: evidence.Fail, After: evidence.Pass, Strength: evidence.Strong,
		})
	}
	md := Markdown(b)
	if want := fmt.Sprintf("%d of %d obligations shown", maxRows, maxRows+50); !strings.Contains(md, want) {
		t.Errorf("markdown does not say the section was cut short, want %q", want)
	}
	if got, want := strings.Count(md, "| `ORD-F0"), maxRows; got != want {
		t.Errorf("markdown shows %d obligation rows, want %d", got, want)
	}
	if strings.Contains(md, "ORD-F0100") {
		t.Error("markdown shows a row past maxRows")
	}
}

// TestSizeCap: the step summary of a change with 5 000 obligations still fits
// in what GitHub accepts. A report GitHub rejects is a report nobody reads.
func TestSizeCap(t *testing.T) {
	t.Parallel()

	const n = 5000
	b := blockBundle()
	b.Obligations = make([]evidence.Obligation, 0, n)
	b.Verdict.Reasons = make([]evidence.Reason, 0, n)
	for i := range n {
		id := fmt.Sprintf("ORD-F%04d", i%10_000)
		b.Obligations = append(b.Obligations, evidence.Obligation{
			ID: id, Kind: "F", Source: "spec.md#" + id, Delta: evidence.Added,
			Tests:  []string{"TestRefunds/" + id + "_" + strings.Repeat("name", 30)},
			Before: evidence.Pass, After: evidence.Pass, Strength: evidence.None,
			Note: strings.Repeat("why this obligation has no evidence ", 20),
		})
		b.Verdict.Reasons = append(b.Verdict.Reasons, evidence.Reason{
			Code: gate.CodeFailBeforeMissing, ID: id, Message: strings.Repeat("no valid fail-before ", 20),
		})
	}
	for name, got := range map[string]string{"Markdown": Markdown(b), "Text": Text(b)} {
		if len(got) > maxBytes {
			t.Errorf("%s of %d obligations is %d bytes, want at most %d", name, n, len(got), maxBytes)
		}
		if !strings.Contains(got, `aval gate --base "$base"`) {
			t.Errorf("%s lost the commands that reproduce the run", name)
		}
	}
}

// TestDeterministic: the same bundle always renders identically, whoever built
// it. A summary that moves between runs cannot be diffed or cached.
func TestDeterministic(t *testing.T) {
	t.Parallel()

	for i := range 3 {
		if got, want := Markdown(blockBundle()), Markdown(blockBundle()); got != want {
			t.Fatalf("run %d: Markdown differs between two renderings of the same bundle", i)
		}
		if got, want := Text(blockBundle()), Text(blockBundle()); got != want {
			t.Fatalf("run %d: Text differs between two renderings of the same bundle", i)
		}
	}
}

func TestLine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		b    evidence.Bundle
		want string
	}{
		{"pass", passBundle(), "pass · tier 0 · no reasons"},
		{"block", blockBundle(), "block · tier 2 · 7 reasons · ORD-N01 fail_before_missing"},
		{"override", warnBundle(), "warn · tier 3 · 7 reasons · ORD-N01 fail_before_missing"},
		{"one warning only", func() evidence.Bundle {
			b := passBundle()
			b.Verdict = evidence.Verdict{Result: evidence.ResultWarn, Reasons: []evidence.Reason{
				{Code: gate.CodeWeakEvidence, ID: "ORD-F02", Message: "weak"},
			}}
			return b
		}(), "warn · tier 0 · 1 reason · ORD-F02 weak_evidence"},
		{"untrusted", func() evidence.Bundle {
			b := passBundle()
			b.Verdict = evidence.Verdict{Result: evidence.ResultBlock, Reasons: []evidence.Reason{
				{Code: "tamper\n" + ansiInjection, ID: "ORD-F01", Message: "x"},
			}}
			return b
		}(), "block · tier 0 · 1 reason · ORD-F01 tamper red"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Line(tc.b); got != tc.want {
				t.Errorf("Line() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClean(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, in, want string }{
		{"ansi colors", "\x1b[31mred\x1b[0m", "red"},
		{"osc title", "a\x1b]0;title\x07b", "ab"},
		{"unterminated csi swallows the rest", "a\x1b[31", "a"},
		{"control characters", "a\x00b\x07c\x1fd\x7fe", "abcde"},
		{"blanks collapse", "a\t\tb\n\nc  d", "a b c d"},
		{"bidi and zero width", "a\u202eb\u2066c\u200bd\ufeffe", "abcde"},
		{"invalid utf8", "a\xffb", "a\uFFFDb"},
		{"trimmed", "  a  ", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := clean(tc.in); got != tc.want {
				t.Errorf("clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUnknownReasonCode: a bundle from a newer aval, or a crafted one, carries
// a code this build does not know. It still gets a sentence, because the gate
// blocks on it.
func TestUnknownReasonCode(t *testing.T) {
	t.Parallel()

	if got := sentence("no_such_code"); !strings.Contains(got, "does not know") {
		t.Errorf("sentence of an unknown code = %q, want the fallback", got)
	}
	if got := sentence(gate.CodeTamper); got != wording[gate.CodeTamper] {
		t.Errorf("sentence(%q) = %q, want the wording of ADR-0005 §4", gate.CodeTamper, got)
	}
}

// assertInert checks that nothing a pull request wrote survives as something a
// terminal would act on.
func assertInert(t *testing.T, name, got string) {
	t.Helper()

	for i, r := range got {
		switch {
		case r == '\n':
		case unicode.IsControl(r):
			t.Errorf("%s holds the control character %U at byte %d", name, r, i)
		case unicode.Is(unicode.Cf, r):
			t.Errorf("%s holds the format character %U at byte %d: text could be reversed or hidden", name, r, i)
		case r == utf8.RuneError:
			// A replacement character is the intended result of
			// invalid UTF-8; it is inert.
		}
	}
}

// assertNoHTML checks that every "<" of the step summary is inside a code
// span, where a Markdown renderer escapes it instead of reading a tag. Prose
// turns "<" into an entity, and an identifier becomes a code span, so nothing
// a pull request wrote can open one. Stripping the spans also proves the
// fences are balanced: an identifier that closed its own span early would
// leave the rest of the report inside one.
func assertNoHTML(t *testing.T, md string) {
	t.Helper()

	stripped, ok := stripCodeSpans(md)
	if !ok {
		t.Errorf("a code span is never closed: an identifier escaped its fence\n%s", md)
	}
	if i := strings.Index(stripped, "<"); i >= 0 {
		t.Errorf("a raw %q outside a code span could be read as HTML: %q", "<", excerpt(stripped, i))
	}
}

// stripCodeSpans removes every Markdown code span, and every fenced block,
// from md: a run of n backticks opens one and the next run of exactly n closes
// it (CommonMark). It reports false when a span is never closed.
func stripCodeSpans(md string) (string, bool) {
	buf := make([]byte, 0, len(md))
	for i := 0; i < len(md); {
		if md[i] != '`' {
			buf = append(buf, md[i])
			i++
			continue
		}
		open := backtickRun(md, i)
		end, ok := closingRun(md, i+open, open)
		if !ok {
			return string(buf), false
		}
		i = end
	}
	return string(buf), true
}

// closingRun returns the index just past the first run of exactly n backticks
// at or after from.
func closingRun(md string, from, n int) (int, bool) {
	for i := from; i < len(md); {
		if md[i] != '`' {
			i++
			continue
		}
		run := backtickRun(md, i)
		if run == n {
			return i + run, true
		}
		i += run
	}
	return 0, false
}

// backtickRun returns how many backticks start at md[i].
func backtickRun(md string, i int) int {
	n := 0
	for i+n < len(md) && md[i+n] == '`' {
		n++
	}
	return n
}

// excerpt returns the text around index i, to point at what went wrong.
func excerpt(s string, i int) string {
	return s[max(i-40, 0):min(i+40, len(s))]
}

// assertTables checks that every row of every Markdown table has as many
// columns as its header: a cell that adds or drops a pipe would break the
// table, and a pipe is the one character a note could use to do it.
func assertTables(t *testing.T, md string) {
	t.Helper()

	want, rows := 0, 0
	for _, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(line, "|") {
			want, rows = 0, 0
			continue
		}
		got := unescapedPipes(line)
		switch {
		case want == 0:
			want, rows = got, 1
		case got != want:
			t.Errorf("table row %d has %d columns, want %d:\n%s", rows, got-1, want-1, line)
		default:
			rows++
		}
	}
}

// unescapedPipes counts the pipes of a line that GFM reads as column
// separators: a pipe escaped with a backslash is content, even inside a code
// span.
func unescapedPipes(line string) int {
	n, escaped := 0, false
	for _, r := range line {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '|':
			n++
		}
	}
	return n
}

// assertWidth checks that plain output fits the 80 columns of a CI log
// (ADR-0003), whatever length a pull request chose for a test name.
func assertWidth(t *testing.T, text string) {
	t.Helper()

	for i, line := range strings.Split(text, "\n") {
		if n := utf8.RuneCountInString(line); n > textWidth {
			t.Errorf("line %d is %d columns wide, want at most %d:\n%s", i+1, n, textWidth, line)
		}
	}
}

// assertGolden compares got with testdata/<name>, or rewrites it with -update.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	if *update {
		if err := os.WriteFile(filepath.Join("testdata", name), []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("output differs from testdata/%s (run with -update if intended)\ngot:\n%s", name, got)
	}
}
