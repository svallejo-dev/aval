// Package summary presents an evidence bundle to a human: the step summary the
// gate writes to $GITHUB_STEP_SUMMARY (ADR-0005 §7), the same report as plain
// text for plain mode (ADR-0003), and a one-line verdict.
//
// The package is pure formatting. It runs no git and no command, and reads no
// file, clock, environment variable or network: everything it says comes from
// the bundle it is given, so the same bundle always renders identically.
//
// Nothing in a bundle is trusted. An obligation's note, a test name, a
// review's reason and a check's command are all chosen by the pull request the
// gate is judging, so each of them is stripped of terminal escapes, control
// characters and bidirectional overrides, capped in length and escaped for the
// syntax it lands in. A note can neither add a column to a table nor inject
// HTML into the step summary.
package summary

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
)

const (
	// maxBytes is GitHub's limit for a step summary. A larger one is
	// rejected whole, so the report has to fit inside it.
	maxBytes = 1 << 20
	// alignWidth is the widest table aval lays out in aligned columns; a
	// wider one becomes one record per row. It is a layout choice, not a
	// cap: plain output is never wrapped, so a log stays greppable and no
	// identifier is ever split (ADR-0003).
	alignWidth = 80
	// maxRows is how many rows of any one section a report shows. Whoever
	// reads a step summary cannot use ten thousand rows anyway, and the
	// bundle artifact holds them all.
	maxRows = 100
	// maxTests and maxPaths cap the lists inside a single row.
	maxTests = 3
	maxPaths = 4
	// Caps on untrusted text, in runes: an identifier (a test name, a
	// command, a SHA), free prose (a note, a message, a rejection) and a
	// label, the short value of a closed set.
	maxIdent = 80
	maxProse = 200
	maxLabel = 32
	// shortSHA is how many characters of a commit a report abbreviates to.
	shortSHA = 12
)

// truncated is the marker that closes a report cut short by maxBytes.
const truncated = "Cut here: the report reached the size limit of a step summary. " +
	"The whole evidence bundle is in the run's artifacts."

// Markdown renders b as the GitHub Actions step summary, for
// $GITHUB_STEP_SUMMARY (ADR-0005 §7). The result is always under GitHub's
// 1 MiB limit for one.
func Markdown(b evidence.Bundle) string { return build(b).render(block.markdown, maxBytes) }

// Text renders b as the same report in plain text: no color, no box drawing
// and no line wider than 80 columns, for plain mode (ADR-0003).
func Text(b evidence.Bundle) string { return build(b).render(block.text, maxBytes) }

// Line renders b as one line, for a log or a hook:
//
//	block · tier 2 · 3 reasons · ORD-F01 fail_before_missing
//
// The result is the one judge settled on, so a line never reports a pass over
// reasons that block. The reason it names is the first blocking one, or the
// first of any effect when nothing blocks.
func Line(b evidence.Bundle) string {
	parts := []string{clean(string(judge(b).shown)), "tier " + strconv.Itoa(b.Tier)}
	if n := len(b.Verdict.Reasons); n == 0 {
		parts = append(parts, "no reasons")
	} else {
		parts = append(parts, plural(n, "reason"))
	}
	if r, ok := headline(b.Verdict.Reasons); ok {
		s := clamp(clean(r.Code), maxIdent)
		if id := clamp(clean(r.ID), maxIdent); id != "" {
			s = id + " " + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// headline picks the reason that explains the verdict best: the first one that
// blocks, or the first of any effect when none does.
func headline(rs []evidence.Reason) (evidence.Reason, bool) {
	if len(rs) == 0 {
		return evidence.Reason{}, false
	}
	for _, r := range rs {
		if gate.Effect(r.Code) == evidence.ResultBlock {
			return r, true
		}
	}
	return rs[0], true
}

// doc is the bundle reduced to blocks: what the report says and in what order,
// with every untrusted string already tagged and capped. Markdown and Text
// render the same doc, so the two can only differ in syntax.
type doc struct {
	blocks []block // the report, dropped from the end when it does not fit
	tail   []block // written last whatever happens: how to reproduce the run
}

// render writes every block with emit, which is block.markdown or block.text,
// and keeps the result under limit bytes. The tail and the cut marker are
// measured first and their room reserved, so the commands a reviewer needs
// survive even a bundle that would fill the budget on its own.
func (d doc) render(emit func(block, *out), limit int) string {
	tail, marker := &out{}, &out{}
	for _, b := range d.tail {
		emit(b, tail)
	}
	emit(aside{truncated}, marker)

	body := &out{limit: max(limit-tail.len()-marker.len()-1, 1)} // 1 for the blank line before the marker
	for _, b := range d.blocks {
		emit(b, body)
	}
	var o out
	s := body.String()
	o.write(s)
	if body.dropped {
		if !strings.HasSuffix(s, "\n\n") { // the cut landed inside a block
			o.write("\n")
		}
		o.write(marker.String())
	}
	o.write(tail.String())
	return strings.TrimRight(o.String(), "\n") + "\n"
}

// build reduces b to the blocks of a report. Every section is built the same
// way: a heading, then the rows, then a marker when there were more rows than
// a step summary can carry.
func build(b evidence.Bundle) doc {
	v := judge(b)
	blocks := make([]block, 0, 32)
	blocks = append(blocks, title(b, v)...)
	blocks = append(blocks, reasonBlocks(b.Verdict, v)...)
	blocks = append(blocks, obligationBlocks(b.Obligations)...)
	blocks = append(blocks, tamperBlocks(b.Tamper)...)
	blocks = append(blocks, scopeBlocks(b.Scope)...)
	blocks = append(blocks, approvalBlocks(b.Head, b.Approvals)...)
	blocks = append(blocks, checkBlocks(b.Checks)...)
	blocks = append(blocks, notCollectedBlocks(b.NotCollected)...)
	return doc{blocks: blocks, tail: reproduceBlocks(b)}
}

// title states the verdict, the mode and the tier, then where the evidence
// comes from. The verdict is the one judge settled on, not the one the bundle
// claims; the mode comes from the bundle, so it is escaped like any other
// untrusted value.
func title(b evidence.Bundle, v verdict) []block {
	h := heading{1, frag{
		fixed(symbol(v.shown) + " aval: "), label(string(v.shown)),
		fixed(" · tier " + strconv.Itoa(b.Tier) + " · "), label(b.Mode),
	}}
	lead := frag{
		strong("repo"), fixed(" "), code(b.Repo),
		fixed(" · "), strong("trust base"), fixed(" "), code(short(b.TrustBase)),
		fixed(" · "), strong("change base"), fixed(" "), code(short(b.ChangeBase)),
		fixed(" ("), label(b.BaseRef), fixed(")"),
		fixed(" → "), strong("head"), fixed(" "), code(short(b.Head)),
		fixed(" · "), strong("aval"), fixed(" "), code(b.AvalVersion),
		fixed(" · "), strong("generated"), fixed(" "), code(b.GeneratedAt.UTC().Format(time.RFC3339)),
	}
	blocks := []block{h, para{lead}}
	if len(b.Changes) > 0 {
		blocks = append(blocks, para{append(frag{strong("changes"), fixed(" ")}, codeList(b.Changes, maxRows)...)})
	}
	return blocks
}

// reasonBlocks lists the reasons grouped by what they do to the verdict, the
// blocking ones first: they are why a pull request cannot merge. When the
// bundle's own result is softer than its reasons, the disagreement is stated
// outright rather than smoothed over.
func reasonBlocks(vd evidence.Verdict, v verdict) []block {
	blocks := []block{heading{2, frag{fixed("Reasons" + countOf(len(vd.Reasons)))}}}
	if len(vd.Reasons) == 0 {
		return append(blocks, para{frag{fixed("None: every rule the gate applies is satisfied.")}})
	}
	var blocking, warning []evidence.Reason
	for _, r := range vd.Reasons {
		if gate.Effect(r.Code) == evidence.ResultBlock {
			blocking = append(blocking, r)
			continue
		}
		warning = append(warning, r)
	}
	switch {
	case v.overridden:
		blocks = append(blocks, para{frag{
			fixed("A valid override lowered the verdict to "), label(string(v.stated)),
			fixed("; the blocking reasons below still stand (ADR-0005 §5)."),
		}})
	case v.mismatch:
		blocks = append(blocks, para{frag{
			fixed("The bundle declares "), label(string(v.stated)),
			fixed(", but its own reasons justify " + string(v.floor) + ", which is what this summary reports: "),
			fixed("only a valid override lowers a block, and only to a warn (ADR-0005 §5)."),
		}})
	}
	blocks = append(blocks, reasonGroup("Blocking", blocking)...)
	return append(blocks, reasonGroup("Warnings", warning)...)
}

// verdict is what the report says about the outcome: the result it shows, what
// the bundle claimed, and what its reasons justify.
type verdict struct {
	shown  evidence.Result // what the report states in its heading
	stated evidence.Result // what the bundle claims
	floor  evidence.Result // the worst effect among the reasons
	// mismatch is set when the bundle claims a result softer than its own
	// reasons justify and no valid override explains it.
	mismatch bool
	// overridden is set when a valid override explains a warn that carries
	// blocking reasons (ADR-0005 §5).
	overridden bool
}

// judge compares what a bundle claims with what its reasons justify. A bundle
// is data, not testimony: a crafted one could claim a pass while carrying five
// blocking reasons, and a summary that repeated the claim would sign off on it.
// The reasons win, unless a valid override is there to explain the difference,
// which is the one thing ADR-0005 §5 lets lower a block, and only to a warn.
func judge(b evidence.Bundle) verdict {
	v := verdict{stated: b.Verdict.Result, shown: b.Verdict.Result, floor: floorOf(b.Verdict.Reasons)}
	v.overridden = v.floor == evidence.ResultBlock && v.stated == evidence.ResultWarn && overridden(b)
	if !v.overridden && rank(v.floor) > rank(v.stated) {
		v.shown, v.mismatch = v.floor, true
	}
	return v
}

// floorOf is the worst result the reasons themselves justify. It reads each
// code through gate.Effect, which blocks on any code it does not know.
func floorOf(rs []evidence.Reason) evidence.Result {
	worst := evidence.ResultPass
	for _, r := range rs {
		if e := gate.Effect(r.Code); rank(e) > rank(worst) {
			worst = e
		}
	}
	return worst
}

// rank orders the results from softest to hardest. A result aval does not know
// ranks hardest: the summary fails closed, like the gate.
func rank(r evidence.Result) int {
	switch r {
	case evidence.ResultPass:
		return 0
	case evidence.ResultWarn:
		return 1
	case evidence.ResultBlock:
		return 2
	}
	return 3
}

// overridden reports whether the bundle holds a review the gate would accept
// as an override: kind override, valid, of the head commit and with a reason
// (ADR-0005 §5).
func overridden(b evidence.Bundle) bool {
	return slices.ContainsFunc(b.Approvals, func(a evidence.Approval) bool {
		return a.Kind == evidence.ApprovalOverride && a.Valid &&
			a.CommitID == b.Head && strings.TrimSpace(a.Reason) != ""
	})
}

// reasonGroup renders one effect's reasons: one entry per code, carrying what
// the code means (ADR-0005 §4), and under it the obligation ID and the gate's
// own message of every reason with that code. The code alone would send a
// reviewer to the ADR, and the same sentence repeated under every ID would
// bury the messages.
func reasonGroup(name string, rs []evidence.Reason) []block {
	if len(rs) == 0 {
		return nil
	}
	shown := rs[:min(len(rs), maxRows)]
	items := make([]item, 0, len(shown))
	codes := make([]string, 0, len(shown))
	for _, r := range shown {
		// The gate sorts its reasons by code, so equal codes arrive
		// together; an unsorted bundle only gets more entries.
		if n := len(items); n == 0 || codes[n-1] != r.Code {
			items = append(items, item{f: frag{code(r.Code), fixed(" — "), fixed(sentence(r.Code))}})
			codes = append(codes, r.Code)
		}
		if sub := reasonDetail(r); len(sub) > 0 {
			last := &items[len(items)-1]
			last.subs = append(last.subs, sub)
		}
	}
	blocks := []block{heading{3, frag{fixed(name + countOf(len(rs)))}}, bullets{items}}
	return withMore(blocks, "reasons", len(shown), len(rs))
}

// reasonDetail is what one reason adds to its code: the obligation it is about
// and what the gate found. A reason with neither adds nothing.
func reasonDetail(r evidence.Reason) frag {
	var f frag
	if r.ID != "" {
		f = append(f, code(r.ID))
	}
	if m := prose(r.Message); m.s != "" {
		if len(f) > 0 {
			f = append(f, fixed(": "))
		}
		f = append(f, m)
	}
	return f
}

// obligationBlocks tabulates what was proved about each obligation of the
// delta: the delta itself, the tests bound to it, how they did before and
// after the change, and how strong that makes the evidence (ADR-0005 §2).
func obligationBlocks(obs []evidence.Obligation) []block {
	blocks := []block{heading{2, frag{fixed("Obligations" + countOf(len(obs)))}}}
	if len(obs) == 0 {
		return append(blocks, para{frag{fixed("None: the change touches no obligation.")}})
	}
	shown := obs[:min(len(obs), maxRows)]
	rows := make([][]frag, 0, len(shown))
	for _, o := range shown {
		rows = append(rows, []frag{
			{code(o.ID)},
			kindFrag(o.Kind),
			{label(string(o.Delta))},
			codeList(o.Tests, maxTests),
			{label(string(o.Before)), fixed(" → "), label(string(o.After))},
			{label(string(o.Strength))},
			{prose(o.Note)},
		})
	}
	header := []string{"ID", "kind", "delta", "bound tests", "before → after", "strength", "note"}
	blocks = append(blocks, table{header, rows})
	return withMore(blocks, "obligations", len(shown), len(obs))
}

// tamperBlocks lists the tampering signals. Any of them blocks whatever the
// tier (ADR-0005 §4), so they are never folded into a count.
func tamperBlocks(fs []evidence.Finding) []block {
	blocks := []block{heading{2, frag{fixed("Tamper" + countOf(len(fs)))}}}
	if len(fs) == 0 {
		return append(blocks, para{frag{fixed("None: no bound test and no protected file changed unexpectedly.")}})
	}
	shown := fs[:min(len(fs), maxRows)]
	rows := make([][]frag, 0, len(shown))
	for _, f := range shown {
		rows = append(rows, []frag{{label(string(f.Kind))}, {code(f.ID)}, {prose(f.Detail)}})
	}
	blocks = append(blocks, table{[]string{"signal", "ID", "detail"}, rows})
	return withMore(blocks, "signals", len(shown), len(fs))
}

// scopeBlocks shows only the commits whose family matters to the verdict: a
// mixed commit blocks and a seam commit warns (ADR-0005 §3b). Listing every
// commit would bury them.
func scopeBlocks(commits []evidence.Commit) []block {
	var shown []evidence.Commit
	var mixed, seam int
	for _, c := range commits {
		switch c.Family {
		case evidence.FamilyMixed:
			mixed++
		case evidence.FamilySeam:
			seam++
		default:
			continue
		}
		shown = append(shown, c)
	}
	blocks := []block{
		heading{2, frag{fixed("Scope")}},
		para{frag{fixed(fmt.Sprintf("%s in the range: %d mixed, %d seam. Only those are listed.",
			plural(len(commits), "commit"), mixed, seam))}},
	}
	if len(shown) == 0 {
		return blocks
	}
	kept := shown[:min(len(shown), maxRows)]
	rows := make([][]frag, 0, len(kept))
	for _, c := range kept {
		rows = append(rows, []frag{{code(short(c.SHA))}, {label(string(c.Family))}, codeList(c.Paths, maxPaths)})
	}
	blocks = append(blocks, table{[]string{"commit", "family", "paths"}, rows})
	return withMore(blocks, "commits", len(kept), len(shown))
}

// approvalBlocks lists every review the gate considered, valid or not, with
// the reason of an override and the rejection of a review that did not count
// (ADR-0005 §5). A review of an earlier commit is the usual rejection, so the
// commit column says which reviews are not of the head.
func approvalBlocks(head string, as []evidence.Approval) []block {
	blocks := []block{heading{2, frag{fixed("Approvals" + countOf(len(as)))}}}
	if len(as) == 0 {
		return append(blocks, para{frag{fixed("None: no review of this pull request was considered.")}})
	}
	shown := as[:min(len(as), maxRows)]
	rows := make([][]frag, 0, len(shown))
	for _, a := range shown {
		commit := frag{code(short(a.CommitID))}
		if a.CommitID != head {
			commit = append(commit, fixed(" (not head)"))
		}
		rows = append(rows, []frag{
			{label(string(a.Kind))}, {code(a.Actor)}, commit,
			{fixed(yesNo(a.Valid))}, {prose(a.Reason)}, {prose(a.Rejection)},
		})
	}
	header := []string{"kind", "actor", "commit", "valid", "reason", "rejection"}
	blocks = append(blocks, table{header, rows})
	return withMore(blocks, "approvals", len(shown), len(as))
}

// checkBlocks tabulates each verifier run with the command that produced it
// and how long it took, so a slow gate can be read from the summary alone.
func checkBlocks(cs []evidence.Check) []block {
	blocks := []block{heading{2, frag{fixed("Checks" + countOf(len(cs)))}}}
	if len(cs) == 0 {
		return append(blocks, para{frag{fixed("None: no verifier ran.")}})
	}
	shown := cs[:min(len(cs), maxRows)]
	rows := make([][]frag, 0, len(shown))
	for _, c := range shown {
		rows = append(rows, []frag{
			{code(c.Name)}, {label(string(c.Status))}, {fixed(strconv.Itoa(c.ExitCode))},
			{fixed(duration(c.DurationMS))}, {code(c.Command)}, {code(c.Artifact)},
		})
	}
	header := []string{"check", "status", "exit", "duration", "command", "artifact"}
	blocks = append(blocks, table{header, rows})
	return withMore(blocks, "checks", len(shown), len(cs))
}

// notCollectedBlocks names the evidence this version does not gather. Missing
// evidence has to be explicit, or a pass reads as more than it is.
func notCollectedBlocks(items []string) []block {
	blocks := []block{heading{2, frag{fixed("Not collected" + countOf(len(items)))}}}
	if len(items) == 0 {
		return append(blocks, para{frag{fixed("Nothing: the bundle reports no missing evidence.")}})
	}
	return append(blocks, para{codeList(items, maxRows)})
}

// reproduceBlocks gives the exact commands that produce this verdict again,
// with all three SHAs, because a reviewer who cannot reproduce a block has to
// take the gate's word for it. Both bases are named: the same head judged
// against another policy is another verdict (ADR-0005 §1).
func reproduceBlocks(b evidence.Bundle) []block {
	return []block{
		heading{2, frag{fixed("Reproduce")}},
		commands{[]frag{
			{fixed("trust="), code(b.TrustBase)},
			{fixed("change="), code(b.ChangeBase)},
			{fixed("head="), code(b.Head)},
			{fixed(`git fetch origin && git checkout "$head"`)},
			{fixed(`aval verify --trust-base "$trust" --change-base "$change" --head "$head"`)},
			{fixed(`aval gate --trust-base "$trust" --change-base "$change" --head "$head"`)},
		}},
	}
}

// withMore closes a section with a marker when it had more rows than the
// report shows, so a reviewer never mistakes a cut section for a short one.
func withMore(blocks []block, noun string, shown, total int) []block {
	if total <= shown {
		return blocks
	}
	return append(blocks, aside{fmt.Sprintf("%d of %d %s shown; the rest are in the evidence bundle.", shown, total, noun)})
}

// wording is the human sentence behind each reason code (ADR-0005 §4). A
// reviewer reads the sentence; the code is for grepping and for the contract.
var wording = map[string]string{
	gate.CodeSpecRule:          "a new error-severity spec rule finding at head",
	gate.CodeOpenSpecInvalid:   "openspec validate rejected the specs",
	gate.CodeOpenQuestion:      "an added or modified open question: a human has to resolve it",
	gate.CodeUnverified:        "an added or modified obligation with no test bound to it",
	gate.CodeFailBeforeMissing: "no valid evidence that the test failed before the change",
	gate.CodeAfterNotPassing:   "a bound test does not pass at head",
	gate.CodeRegression:        "an unbound test fails at head and is not in the base baseline",
	gate.CodeBuildFailed:       "a package does not build at head, or go test could not start",
	gate.CodeTamper:            "a tampering signal: a bound test or a protected file changed",
	gate.CodeUndeclaredRuntime: "a test carrying an obligation ID ran with no static declaration",
	gate.CodeMixedCommit:       "one commit touches dx and feat paths at once",
	gate.CodePremortemMissing:  "a change of tier 2 or above has no premortem.md",
	gate.CodePremortemUnmapped: "a premortem.md with no items, or an item citing no ID of its change",
	gate.CodeApprovalMissing:   "tier 3 needs a CODEOWNER's approval of the head commit",
	gate.CodeLintNewIssues:     "golangci-lint reports issues new since the merge base",
	gate.CodeAssumption:        "an added or modified assumption still to validate",
	gate.CodeSLOUnverified:     "an added or modified SLO, and this version does not measure SLOs",
	gate.CodeSpecWarning:       "a new warn-severity spec rule finding at head",
	gate.CodeSeamTouched:       "a feat commit touches seam paths",
	gate.CodeWeakEvidence:      "weak fail-before evidence: the failure at the base may not prove the change",
	gate.CodeNoBasePolicy:      "the base commit has no root aval.yaml, so the gate only observes",
	gate.CodeContractBreaking:  "a breaking change to a published contract",
	gate.CodeContractLint:      "a contract lint rule",
	gate.CodeChangeNotArchived: "a finished OpenSpec change that was not archived",
}

// sentence returns the human wording of a reason code. A code this build does
// not know still gets a sentence, because the gate blocks on it (gate.Effect)
// and a bare code would tell a reviewer nothing.
func sentence(code string) string {
	if s, ok := wording[code]; ok {
		return s
	}
	return "a reason code this version of aval does not know, which blocks"
}

// kindNames name the obligation kinds (ADR-0002). The letter alone is a
// contract with the specs, not an explanation for a reviewer.
var kindNames = map[string]string{
	"F": "F must",
	"N": "N must-not",
	"I": "I invariant",
	"S": "S SLO",
	"A": "A assumption",
	"O": "O open question",
}

// kindFrag names an obligation's kind, falling back to the bundle's own
// letter, escaped, for a kind this build does not know.
func kindFrag(kind string) frag {
	if name, ok := kindNames[kind]; ok {
		return frag{fixed(name)}
	}
	return frag{label(kind)}
}

// symbol pairs a result with a mark, so a verdict never depends on color
// alone (ADR-0003).
func symbol(r evidence.Result) string {
	switch r {
	case evidence.ResultPass:
		return "✓"
	case evidence.ResultWarn:
		return "!"
	case evidence.ResultBlock:
		return "✗"
	}
	return "?"
}

// codeList renders up to n untrusted identifiers as comma-separated code
// spans, and says how many it left out.
func codeList(items []string, n int) frag {
	if len(items) == 0 {
		return frag{fixed(dash)}
	}
	shown := items[:min(len(items), n)]
	f := make(frag, 0, 2*len(shown)+1)
	for i, it := range shown {
		if i > 0 {
			f = append(f, fixed(", "))
		}
		f = append(f, code(it))
	}
	if len(items) > len(shown) {
		f = append(f, fixed(fmt.Sprintf(" (+%d more)", len(items)-len(shown))))
	}
	return f
}

// duration renders a check's milliseconds the way a person reads them.
func duration(ms int64) string {
	switch {
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + "ms"
	case ms < 60_000:
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
	default:
		return fmt.Sprintf("%dm%02ds", ms/60_000, ms%60_000/1000)
	}
}

// short abbreviates a commit to shortSHA characters. The full SHAs are in the
// commands that reproduce the run.
func short(sha string) string {
	s := clean(sha)
	if i := runeIndex(s, shortSHA); i < len(s) {
		return s[:i]
	}
	return s
}

// countOf renders a section's size, and nothing at all when it is empty: an
// empty section says so in words.
func countOf(n int) string {
	if n == 0 {
		return ""
	}
	return " (" + strconv.Itoa(n) + ")"
}

// plural renders a count with its noun: "1 commit", "3 commits".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
