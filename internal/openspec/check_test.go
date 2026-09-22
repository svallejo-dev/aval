package openspec

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// spike holds the fixtures of spike S1 (ADR-0002); see its README.
var spike = os.DirFS("testdata/spike")

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(spike, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// spikeRepo is the repository the spike applied every negative and positive
// case to: refunds/spec-after.md as the main spec, add-refund-limits
// archived, and the case's delta as the active change named after the case.
func spikeRepo(t *testing.T, name string, delta []byte) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"openspec/specs/refunds/spec.md": {Data: fixture(t, "refunds/spec-after.md")},
		"openspec/changes/archive/2026-09-21-add-refund-limits/specs/refunds/spec.md": {
			Data: fixture(t, "changes/add-refund-limits/specs/refunds/spec.md"),
		},
		"openspec/changes/" + name + "/specs/refunds/spec.md": {Data: delta},
	}
}

func load(t *testing.T, fsys fstest.MapFS) *Repo {
	t.Helper()
	r, err := Load(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

func check(t *testing.T, fsys fstest.MapFS) []Finding {
	t.Helper()
	return load(t, fsys).Check(CheckOptions{})
}

// brief renders findings as "path:line severity rule".
func brief(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%s:%d %s %s", f.Path, f.Line, f.Severity, f.Rule))
	}
	return out
}

func verdict(fs []Finding) string {
	v := "accept"
	for _, f := range fs {
		switch f.Severity {
		case SeverityError:
			return "error"
		case SeverityWarn:
			v = "warn"
		}
	}
	return v
}

// readmeVerdicts reads the "aval expects" column of the spike README.
func readmeVerdicts(t *testing.T) map[string]string {
	t.Helper()
	row := regexp.MustCompile("^\\| `((?:negative|positive)/[a-z0-9-]+)` \\|.*\\| \\*\\*(error|warn|accept)\\*\\*[^|]*\\|$")
	out := make(map[string]string)
	for _, l := range strings.Split(string(fixture(t, "README.md")), "\n") {
		if m := row.FindStringSubmatch(l); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// archivedRepos is the tree before and after a pull request that archives
// the case's change: the head has the spec archive wrote and the change in
// changes/archive/.
func archivedRepos(t *testing.T, name string) (base, head fstest.MapFS) {
	t.Helper()
	base = spikeRepo(t, "unused", nil)
	delete(base, "openspec/changes/unused/specs/refunds/spec.md")
	head = maps.Clone(base)
	head["openspec/specs/refunds/spec.md"] = &fstest.MapFile{Data: fixture(t, name+"/spec-after.md")}
	head["openspec/changes/archive/2026-09-21-"+path.Base(name)+"/specs/refunds/spec.md"] = &fstest.MapFile{Data: fixture(t, name+"/delta.md")}
	return base, head
}

// TestSpikeCases: every negative and positive fixture gets the verdict the
// README's "aval expects" column gives it, through exactly these findings:
// while the change is active, and, when archive applied it, once the pull
// request has archived it.
func TestSpikeCases(t *testing.T) {
	t.Parallel()

	const (
		p    = "openspec/changes/%s/specs/refunds/spec.md:%d %s %s"
		a    = "openspec/changes/archive/2026-09-21-%s/specs/refunds/spec.md:%d %s %s"
		spec = "openspec/specs/refunds/spec.md:%d %s %s"
	)
	tests := map[string]struct {
		want     []string
		msg      string   // a fact the message must state
		archived []string // findings after the pull request archives the change
	}{
		"negative/bracket-id": {want: []string{fmt.Sprintf(p, "bracket-id", 3, SeverityError, RuleInvalidID)},
			msg:      `"[ORD-F04] Refund reason required" must start with an obligation ID`,
			archived: []string{fmt.Sprintf(a, "bracket-id", 3, SeverityError, RuleInvalidID), fmt.Sprintf(spec, 45, SeverityError, RuleInvalidID)}},
		"negative/duplicate-id": {want: []string{fmt.Sprintf(p, "duplicate-id", 3, SeverityError, RuleDuplicateID)},
			msg:      `ORD-F01 is already defined at openspec/specs/refunds/spec.md:8 as "ORD-F01 Refund is idempotent"`,
			archived: []string{fmt.Sprintf(a, "duplicate-id", 3, SeverityError, RuleDuplicateID), fmt.Sprintf(spec, 45, SeverityError, RuleDuplicateID)}},
		"negative/loose-header": {want: []string{fmt.Sprintf(p, "loose-header", 3, SeverityError, RuleLooseHeader)},
			msg:      `"###requirement:ORD-F06 Refund currency matches order"`,
			archived: []string{fmt.Sprintf(a, "loose-header", 3, SeverityError, RuleLooseHeader), fmt.Sprintf(spec, 45, SeverityError, RuleLooseHeader)}},
		"negative/modified-case-variant": {want: []string{fmt.Sprintf(p, "modified-case-variant", 3, SeverityError, RuleUnmatchedName)},
			msg: `ORD-F01 is "ORD-F01 Refund is idempotent" at openspec/specs/refunds/spec.md:8`},
		"negative/modified-drops-marker": {want: []string{fmt.Sprintf(p, "modified-drops-marker", 3, SeverityWarn, RuleDroppedMarker)},
			msg:      `marker that openspec/specs/refunds/spec.md:30 carries`,
			archived: []string{fmt.Sprintf(a, "modified-drops-marker", 3, SeverityWarn, RuleDroppedMarker)}},
		"negative/modified-drops-scenario": {want: []string{fmt.Sprintf(p, "modified-drops-scenario", 3, SeverityError, RuleDroppedScenario)},
			msg: `still has: "Different keys for same order", "Key reused after the window";`},
		"negative/modified-old-name-after-rename": {want: []string{fmt.Sprintf(p, "modified-old-name-after-rename", 3, SeverityError, RuleModifiedOldName)},
			msg: `use the new name "ORD-S01 Refund latency budget"`},
		"negative/non-requirement-h3": {want: []string{fmt.Sprintf(p, "non-requirement-h3", 6, SeverityError, RuleStrayHeading)},
			msg: `heading "### Notes" in ## ADDED Requirements`},
		"negative/renamed-id-change": {want: []string{fmt.Sprintf(p, "renamed-id-change", 4, SeverityError, RuleRenamedID)},
			msg:      `from ORD-S01 to ORD-S09`,
			archived: []string{fmt.Sprintf(a, "renamed-id-change", 4, SeverityError, RuleRenamedID)}},
		"negative/trailing-hash": {want: []string{fmt.Sprintf(p, "trailing-hash", 3, SeverityError, RuleTrailingHash)},
			msg:      `"ORD-N01 Refund never exceeds order total ##"`,
			archived: []string{fmt.Sprintf(a, "trailing-hash", 3, SeverityError, RuleTrailingHash), fmt.Sprintf(spec, 38, SeverityError, RuleTrailingHash)}},
		"negative/h1-in-requirement": {want: []string{fmt.Sprintf(p, "h1-in-requirement", 6, SeverityError, RuleStrayHeading)},
			msg: `heading "# Aside" in ## ADDED Requirements`},
		"negative/h1-between-requirements": {want: []string{fmt.Sprintf(p, "h1-between-requirements", 10, SeverityError, RuleStrayHeading)},
			msg:      `heading "# Receipts" in ## ADDED Requirements`,
			archived: []string{fmt.Sprintf(a, "h1-between-requirements", 10, SeverityError, RuleStrayHeading), fmt.Sprintf(spec, 52, SeverityError, RuleStrayHeading)}},
		"positive/c-sharp-name":          {},
		"positive/fenced-requirement":    {},
		"positive/renamed-then-modified": {},
	}

	verdicts := readmeVerdicts(t)
	for _, group := range []string{"negative", "positive"} {
		entries, err := fs.ReadDir(spike, group)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := group + "/" + e.Name()
			if _, ok := verdicts[name]; !ok {
				t.Errorf("%s has no row in the README's negative/positive table", name)
			}
			if _, ok := tests[name]; !ok {
				t.Errorf("%s has no test case", name)
			}
		}
	}
	if len(verdicts) != len(tests) {
		t.Errorf("README lists %d cases, the test has %d", len(verdicts), len(tests))
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := check(t, spikeRepo(t, path.Base(name), fixture(t, name+"/delta.md")))
			if !slices.Equal(brief(got), tt.want) {
				t.Errorf("findings:\n got %q\nwant %q", brief(got), tt.want)
			}
			if v := verdict(got); v != verdicts[name] {
				t.Errorf("verdict %s, README expects %s", v, verdicts[name])
			}
			if len(got) > 0 && !strings.Contains(got[0].Message, tt.msg) {
				t.Errorf("message %q does not contain %q", got[0].Message, tt.msg)
			}

			if _, err := fs.Stat(spike, name+"/spec-after.md"); err != nil {
				return // archive refused the change
			}
			base, head := archivedRepos(t, name)
			b, err := Load(context.Background(), base)
			if err != nil {
				t.Fatal(err)
			}
			got = load(t, head).Check(CheckOptions{Base: b})
			if !slices.Equal(brief(got), tt.archived) {
				t.Errorf("findings once archived:\n got %q\nwant %q", brief(got), tt.archived)
			}
			if v := verdict(got); v != verdicts[name] {
				t.Errorf("verdict once archived %s, README expects %s", v, verdicts[name])
			}
		})
	}
}

// TestSpikeBaseline: the spike's own change is clean before and after
// archiving, and carries its aval.yaml.
func TestSpikeBaseline(t *testing.T) {
	t.Parallel()

	change := "changes/add-refund-limits/specs/refunds/spec.md"
	before := fstest.MapFS{
		"openspec/specs/refunds/spec.md":                           {Data: fixture(t, "refunds/spec-before.md")},
		"openspec/changes/add-refund-limits/specs/refunds/spec.md": {Data: fixture(t, change)},
		"openspec/changes/add-refund-limits/aval.yaml":             {Data: fixture(t, "extra-files/aval.yaml")},
		"openspec/changes/add-refund-limits/premortem.md":          {Data: fixture(t, "extra-files/premortem.md")},
	}
	after := spikeRepo(t, "noop", nil)
	delete(after, "openspec/changes/noop/specs/refunds/spec.md")

	for name, fsys := range map[string]fstest.MapFS{"before": before, "after": after} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, err := Load(context.Background(), fsys)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Check(CheckOptions{}); len(got) != 0 {
				t.Errorf("unexpected findings: %v", got)
			}
			// Archived by the pull request: every delta rule against the base.
			if got := r.Check(CheckOptions{Base: load(t, before)}); len(got) != 0 {
				t.Errorf("unexpected findings against the base: %v", got)
			}
			if len(r.Changes) != 1 {
				t.Fatalf("got %d changes, want 1", len(r.Changes))
			}
			ch := r.Changes[0]
			if ch.ID != "add-refund-limits" || ch.Archived != (name == "after") || len(ch.Deltas) != 5 {
				t.Errorf("change = %s archived=%v with %d deltas", ch.ID, ch.Archived, len(ch.Deltas))
			}
			if name == "before" && (ch.Manifest == nil || ch.Manifest.Tier != 2 || ch.Manifest.Owner != "@payments-team") {
				t.Errorf("manifest = %+v, want tier 2 owned by @payments-team", ch.Manifest)
			}
			if name == "after" && ch.Manifest != nil {
				t.Errorf("manifest = %+v, want nil without aval.yaml", ch.Manifest)
			}
		})
	}
}

const (
	f01 = "## Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n#### Scenario: S1\n- ok\n"
	f02 = "## Requirements\n### Requirement: ORD-F02 Two\nThe system SHALL two.\n#### Scenario: S2\n- ok\n"
)

func added(names ...string) string {
	var b strings.Builder
	b.WriteString("## ADDED Requirements\n")
	for _, n := range names {
		b.WriteString("### Requirement: " + n + "\nThe system SHALL work.\n#### Scenario: Works\n- ok\n")
	}
	return b.String()
}

// TestCheckRules covers the cross-file rules and the single-header rules
// the spike fixtures do not reach.
func TestCheckRules(t *testing.T) {
	t.Parallel()

	const (
		specA  = "openspec/specs/a/spec.md"
		deltaA = "openspec/changes/c1/specs/a/spec.md"
		deltaB = "openspec/changes/c2/specs/a/spec.md"
		old    = "openspec/changes/archive/2026-01-01-old/specs/a/spec.md"
		newer  = "openspec/changes/archive/2026-02-02-new/specs/a/spec.md"
		f09    = "### Requirement: ORD-F09 Back\nThe system SHALL back.\n#### Scenario: S9\n- ok\n"
	)
	tests := []struct {
		name  string
		base  map[string]string // the pull request's base tree; nil for none
		files map[string]string
		want  []string
		msg   string // a fact the first finding's message must state
	}{
		{
			name:  "duplicate across main specs",
			files: map[string]string{specA: f01, "openspec/specs/b/spec.md": strings.Replace(f01, "One", "Uno", 1)},
			want:  []string{"openspec/specs/b/spec.md:2 error duplicate-id"},
			msg:   `ORD-F01 is already defined at openspec/specs/a/spec.md:2 as "ORD-F01 One"`,
		},
		{
			name:  "duplicate in one main spec",
			files: map[string]string{specA: f01 + strings.TrimPrefix(f01, "## Requirements\n")},
			want:  []string{specA + ":6 error duplicate-id"},
		},
		{
			name:  "duplicate between active changes",
			files: map[string]string{specA: f01, deltaA: added("ORD-F02 Two"), deltaB: added("ORD-F02 Deux")},
			want:  []string{deltaB + ":2 error duplicate-id"},
			msg:   "already defined at " + deltaA + ":2",
		},
		{
			name: "ADDED reuses a retired ID",
			files: map[string]string{specA: f01, deltaA: added("ORD-F09 Back"),
				old: "## REMOVED Requirements\n### Requirement: ORD-F09 Gone\n**Reason**: r\n"},
			want: []string{deltaA + ":2 error retired-id"},
			msg:  "reuses ORD-F09, which openspec/changes/archive/2026-01-01-old retired",
		},
		{
			name: "reference to a retired ID",
			files: map[string]string{specA: f01, deltaA: "## REMOVED Requirements\n- `### Requirement: ORD-F09 Gone`\n",
				old: "## REMOVED Requirements\n- `### Requirement: ORD-F09 Gone`\n"},
			want: []string{deltaA + ":2 error unknown-id"},
			msg:  `REMOVED "ORD-F09 Gone" refers to ORD-F09, which no main spec or active ADDED defines; openspec/changes/archive/2026-01-01-old retired it`,
		},
		{
			name:  "MODIFIED and RENAMED of undefined IDs",
			files: map[string]string{specA: f01, deltaA: "## MODIFIED Requirements\n### Requirement: ORD-F77 X\nThe system SHALL x.\n## RENAMED Requirements\n- FROM: `### Requirement: ORD-F78 A`\n- TO: `### Requirement: ORD-F78 B`\n"},
			want:  []string{deltaA + ":2 error unknown-id", deltaA + ":5 error unknown-id"},
		},
		{
			name:  "REMOVED under another title",
			files: map[string]string{specA: f01, deltaA: "## REMOVED Requirements\n### Requirement: ORD-F01 Uno\n"},
			want:  []string{deltaA + ":2 error unmatched-name"},
			msg:   `REMOVED "ORD-F01 Uno" matches no requirement of "a" exactly`,
		},
		{
			name:  "MODIFIED an ID of another capability",
			files: map[string]string{specA: f01, "openspec/changes/c1/specs/b/spec.md": "## MODIFIED Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n#### Scenario: S1\n- ok\n"},
			want:  []string{"openspec/changes/c1/specs/b/spec.md:2 error unmatched-name"},
		},
		{
			name: "MODIFIED an ID only an active ADDED defines",
			files: map[string]string{specA: f01, deltaA: added("ORD-F02 Two"),
				deltaB: "## MODIFIED Requirements\n### Requirement: ORD-F02 Two\nThe system SHALL two.\n#### Scenario: Works\n- ok\n"},
			want: []string{deltaB + ":2 error unmatched-name"},
		},
		{
			name: "MODIFIED after a chain of renames",
			files: map[string]string{specA: f01, deltaA: "## MODIFIED Requirements\n### Requirement: ORD-F01 Three\nThe system SHALL three.\n#### Scenario: S1\n- ok\n" +
				"## RENAMED Requirements\n- FROM: `### Requirement: ORD-F01 One`\n- TO: `### Requirement: ORD-F01 Two`\n" +
				"- FROM: `### Requirement: ORD-F01 Two`\n- TO: `### Requirement: ORD-F01 Three`\n"},
		},
		{
			name:  "MODIFIED keeps a repeated scenario only once",
			files: map[string]string{specA: f01 + "#### Scenario: S1\n- again\n", deltaA: "## MODIFIED Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n#### Scenario: S1\n- ok\n"},
			want:  []string{deltaA + ":2 error dropped-scenario"},
			msg:   `still has: "S1";`,
		},
		{
			name:  "archived changes are history",
			files: map[string]string{specA: f01, old: "## ADDED Requirements\n###requirement: Bad `name` ##\n## MODIFIED Requirements\n### Requirement: ORD-F55 Ghost\n"},
		},
		{
			name: "name length",
			files: map[string]string{
				specA: "## Requirements\n### Requirement: ORD-F01 " + strings.Repeat("\u00e9", 42) + "\nThe system SHALL x.\n#### Scenario: S\n- ok\n",
				deltaA: added("ORD-F02 "+strings.Repeat("\u00e9", 41), "ORD-F03 "+strings.Repeat("x", 42)) +
					"## MODIFIED Requirements\n### Requirement: ORD-F01 " + strings.Repeat("\u00e9", 42) + "\nThe system SHALL y.\n#### Scenario: S\n- ok\n",
				deltaB: "## RENAMED Requirements\n- FROM: `### Requirement: ORD-F01 " + strings.Repeat("\u00e9", 42) + "`\n- TO: `### Requirement: ORD-F01 " + strings.Repeat("\u00fc", 42) + "`\n",
			},
			want: []string{deltaA + ":6 warn name-length", deltaB + ":3 warn name-length", specA + ":2 warn name-length"},
			msg:  "has 50 characters; OpenSpec's conventions ask for fewer than 50",
		},
		{
			name: "header form and name",
			files: map[string]string{specA: "## Requirements\n" +
				"### requirement: ORD-F01 Lower\nThe system SHALL a.\n" +
				"###Requirement: ORD-F02 Tight\nThe system SHALL b.\n" +
				"### Requirement:  ORD-F03 Wide\nThe system SHALL c.\n" +
				"###\u00a0Requirement: ORD-F04 Nbsp\nThe system SHALL d.\n" +
				"### Requirement: ORD-F05 Tab\t#\nThe system SHALL e.\n" +
				"### Requirement: ORD-F06 Use `x`\nThe system SHALL f.\n" +
				"### Requirement: Refund is idempotent\nThe system SHALL g.\n" +
				"### Requirement: ORD-F07\nThe system SHALL h.\n" +
				"### Requirement: ORD-F1 Short\nThe system SHALL i.\n" +
				"### Requirement: ORD-F08 SDK for C#\nThe system SHALL j.\n"},
			want: []string{
				specA + ":2 error loose-header", specA + ":4 error loose-header", specA + ":6 error loose-header",
				specA + ":8 error loose-header", specA + ":10 error trailing-hash", specA + ":12 error backtick",
				specA + ":14 error invalid-id", specA + ":16 error invalid-id", specA + ":18 error invalid-id",
			},
			msg: `header "### requirement: ORD-F01 Lower" must start with exactly "### Requirement: "`,
		},
		{
			name: "stray headings",
			files: map[string]string{
				specA: "## Purpose\n### Background\n" + f01 + "### Notes\n```\n### Fenced\n```\n",
				deltaA: "## REMOVED Requirements\n### Why\n- `### Requirement: ORD-F01 One`\n" +
					"## RENAMED Requirements\n### How\n",
			},
			want: []string{deltaA + ":2 error stray-heading", deltaA + ":5 error stray-heading", specA + ":8 error stray-heading"},
		},
		{
			name: "unpaired renames",
			files: map[string]string{specA: f01 + strings.TrimPrefix(f02, "## Requirements\n"), deltaA: "## RENAMED Requirements\n" +
				"- TO: `### Requirement: ORD-F01 Lost`\n" +
				"- FROM: `### Requirement: ORD-F01 One`\n" +
				"- FROM: `### Requirement: ORD-F02 Two`\n" +
				"- TO: `### Requirement: ORD-F02 Dos`\n" +
				"- FROM: `### Requirement: ORD-F01 One`\n"},
			want: []string{deltaA + ":2 error unpaired-rename", deltaA + ":3 error unpaired-rename", deltaA + ":6 error unpaired-rename"},
			msg:  `RENAMED TO: "ORD-F01 Lost" has no matching FROM: line`,
		},
		{
			name: "loose RENAMED and REMOVED references",
			files: map[string]string{specA: f01 + strings.TrimPrefix(f02, "## Requirements\n"), deltaA: "## RENAMED Requirements\n" +
				"FROM: `###Requirement: ORD-F01 One`\n" +
				"* TO: `### Requirement: ORD-F01 Uno`\n" +
				"## REMOVED Requirements\n+ ### Requirement:  ORD-F02 Two\n"},
			want: []string{deltaA + ":2 error loose-header", deltaA + ":5 error loose-header"},
		},
		{
			name: "stray headings of every level",
			files: map[string]string{
				specA: "## Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n# Aside\n#### Scenario: S1\n- ok\n" +
					"### Requirement: ORD-F02 Two\nThe system SHALL two.\n#### Scenario: S2\n- ok\n## \n### Requirement: ORD-F03 Past a bare heading\n",
				deltaA: "## ADDED Requirements\n### Requirement: ORD-F04 Four\nThe system SHALL four.\n### \n#### Scenario: S4\n- ok\n" +
					"## \n### Requirement: ORD-F05 Five\nThe system SHALL five.\n#### Scenario: S5\n- ok\n##  \n### Requirement: ORD-F06 Hidden\n",
				deltaB: "# Delta for a\n## REMOVED Requirements\n- `### Requirement: ORD-F02 Two`\n",
			},
			want: []string{
				deltaA + ":4 error stray-heading", deltaA + ":7 error stray-heading", deltaA + ":12 error stray-heading",
				specA + ":4 error stray-heading", specA + ":11 error stray-heading",
			},
			msg: `heading "###" in ## ADDED Requirements is not a "### Requirement:" header`,
		},
		{
			name:  "main spec defines a retired ID",
			files: map[string]string{specA: f01 + f09, old: "## REMOVED Requirements\n### Requirement: ORD-F09 Gone\n"},
			want:  []string{specA + ":6 error retired-id"},
			msg:   `"ORD-F09 Back" reuses ORD-F09, which openspec/changes/archive/2026-01-01-old retired`,
		},
		{
			name: "an ID renamed away is retired, a kept one is not",
			files: map[string]string{specA: f01, deltaA: added("ORD-F05 Again"), old: "## RENAMED Requirements\n" +
				"- FROM: `### Requirement: ORD-F05 Five`\n- TO: `### Requirement: ORD-F06 Six`\n" +
				"- FROM: `### Requirement: ORD-F01 Uno`\n- TO: `### Requirement: ORD-F01 One`\n"},
			want: []string{deltaA + ":2 error retired-id"},
		},
		{
			name: "rename cycle",
			files: map[string]string{specA: f01, deltaA: "## RENAMED Requirements\n" +
				"- FROM: `### Requirement: ORD-F01 Two`\n- TO: `### Requirement: ORD-F01 Three`\n" +
				"- FROM: `### Requirement: ORD-F01 Three`\n- TO: `### Requirement: ORD-F01 Two`\n"},
			want: []string{deltaA + ":2 error unmatched-name", deltaA + ":4 error unmatched-name"},
		},
		{
			name: "retired ID reused by a change the pull request archived",
			base: map[string]string{specA: f01, old: "## REMOVED Requirements\n### Requirement: ORD-F09 Gone\n###requirement: ORD-F08 History\n"},
			files: map[string]string{specA: f01 + f09, newer: added("ORD-F09 Back"),
				old: "## REMOVED Requirements\n### Requirement: ORD-F09 Gone\n###requirement: ORD-F08 History\n"},
			want: []string{newer + ":2 error retired-id", specA + ":6 error retired-id"},
		},
		{
			name: "the same archive without a base is history",
			files: map[string]string{specA: f01 + f09, newer: added("ORD-F09 Back"),
				old: "## REMOVED Requirements\n### Requirement: ORD-F09 Gone\n###requirement: ORD-F08 History\n"},
			want: []string{specA + ":6 error retired-id"},
		},
		{
			name: "MODIFIED archived by the pull request is checked against the base spec",
			base: map[string]string{specA: f01 + // the base's own duplicate is not the pull request's finding
				"### Requirement: ORD-F01 Twin\nThe system SHALL twin.\n#### Scenario: T\n- ok\n"},
			files: map[string]string{specA: strings.ReplaceAll(f01, "S1", "S2"), newer: "## MODIFIED Requirements\n" + strings.ReplaceAll(strings.TrimPrefix(f01, "## Requirements\n"), "S1", "S2")},
			want:  []string{newer + ":2 error dropped-scenario"},
			msg:   `still has: "S1"`,
		},
	}
	mapFS := func(files map[string]string) fstest.MapFS {
		fsys := fstest.MapFS{}
		for name, data := range files {
			fsys[name] = &fstest.MapFile{Data: []byte(data)}
		}
		return fsys
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var opts CheckOptions
			if tt.base != nil {
				opts.Base = load(t, mapFS(tt.base))
			}
			got := load(t, mapFS(tt.files)).Check(opts)
			if !slices.Equal(brief(got), tt.want) {
				t.Errorf("findings:\n got %q\nwant %q\n%v", brief(got), tt.want, got)
			}
			if tt.msg != "" && (len(got) == 0 || !strings.Contains(got[0].Message, tt.msg)) {
				t.Errorf("first message does not contain %q: %v", tt.msg, got)
			}
		})
	}
}

func TestFindingString(t *testing.T) {
	t.Parallel()
	f := Finding{Severity: SeverityWarn, Rule: RuleNameLength, Path: "openspec/specs/a/spec.md", Line: 7, Message: "too long"}
	if got, want := f.String(), "openspec/specs/a/spec.md:7: warn: too long [name-length]"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
