package openspec

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// render prints what the reader extracted from a requirement.
func render(q Requirement) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %q id=%s title=%q text=%q", q.Line, q.Name, q.ID, q.Title, q.Text)
	if q.Characterization {
		b.WriteString(" characterization")
	}
	for _, s := range q.Scenarios {
		fmt.Fprintf(&b, " [%d %q %q]", s.Line, s.Name, s.Text)
	}
	return b.String()
}

func TestParseSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		src    string
		want   []string
		strays []int
	}{
		{
			name: "BOM and CRLF",
			src:  "\ufeff# t\r\n\r\n## Requirements\r\n### Requirement: ORD-F01 One\r\nThe system SHALL one\r\nacross lines.\r\n\r\n#### Scenario: S1 ##\r\n- a\r\n- b\r\n",
			want: []string{`4 "ORD-F01 One" id=ORD-F01 title="One" text="The system SHALL one\nacross lines." [8 "S1" "- a\n- b"]`},
		},
		{
			name: "lone CR",
			src:  "## Requirements\r### Requirement: ORD-F01 One\rThe system SHALL one.\r",
			want: []string{`2 "ORD-F01 One" id=ORD-F01 title="One" text="The system SHALL one."`},
		},
		{
			name: "fences",
			src: "## Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n" +
				"~~~\n### Requirement: ORD-F99 In tildes\n```\n### Requirement: ORD-F98 Backticks do not close tildes\n~~~\n" +
				"#### Scenario: S1\n````md\n#### Scenario: Not one\n```\n### Requirement: ORD-F97 Three backticks do not close four\n````\n- ok\n" +
				"### Requirement: ORD-F02 Two\n```\n### Requirement: ORD-F96 An unclosed fence hides the rest\n",
			want: []string{
				`2 "ORD-F01 One" id=ORD-F01 title="One" text="The system SHALL one." [9 "S1" "` +
					"````md\\n#### Scenario: Not one\\n```\\n### Requirement: ORD-F97 Three backticks do not close four\\n````\\n- ok" + `"]`,
				`16 "ORD-F02 Two" id=ORD-F02 title="Two" text="Requirement: ORD-F02 Two"`,
			},
		},
		{
			name: "scenarios and metadata",
			src: "## Requirements\n### Requirement: ORD-F01 One\n**Reason**: only metadata\n" +
				"#### Scenario: Empty\n#### Edge case\n- body\n##### Detail\n- deep\n#### scenario:   Lower ####\n- x\n" +
				"### Notes\n#### Scenario: Under notes\n- y\n",
			want:   []string{`2 "ORD-F01 One" id=ORD-F01 title="One" text="**Reason**: only metadata" [5 "Edge case" "- body\n##### Detail\n- deep"] [9 "Lower" "- x"]`},
			strays: []int{11},
		},
		{
			name: "characterization marker",
			src: "## Requirements\n" +
				"### Requirement: ORD-I01 Kept\n  **aval**:characterization  \nThe system SHALL a.\n" +
				"### Requirement: ORD-I02 Wrong case\n**AVAL**: characterization\nThe system SHALL b.\n" +
				"### Requirement: ORD-I03 Typo\n**aval**: characterisation\nThe system SHALL c.\n" +
				"### Requirement: ORD-I04 In a scenario\nThe system SHALL d.\n#### Scenario: S\n**aval**: characterization\n" +
				"### Requirement: ORD-I05 Fenced\n```\n**aval**: characterization\n```\nThe system SHALL e.\n",
			want: []string{
				`2 "ORD-I01 Kept" id=ORD-I01 title="Kept" text="The system SHALL a." characterization`,
				`5 "ORD-I02 Wrong case" id=ORD-I02 title="Wrong case" text="The system SHALL b."`,
				`8 "ORD-I03 Typo" id=ORD-I03 title="Typo" text="The system SHALL c."`,
				`11 "ORD-I04 In a scenario" id=ORD-I04 title="In a scenario" text="The system SHALL d." [13 "S" "**aval**: characterization"]`,
				`15 "ORD-I05 Fenced" id=ORD-I05 title="Fenced" text="The system SHALL e."`,
			},
		},
		{
			name: "headers OpenSpec reads and the ones it does not",
			src: "## requirements\n" +
				"###\u00a0Requirement: ORD-F01 Nbsp\nThe system SHALL a.\n" +
				"### REQUIREMENT: ORD-F02 Upper   \nThe system SHALL b.\n" +
				"### Requirement: ORD-F03 Line\u2028separator\nThe system SHALL c.\n" +
				"### Requirement: [ORD-F04] Bracket\nThe system SHALL d.\n" +
				"### Requirement: ORD-F05 Next line\u0085\nThe system SHALL e.\n",
			want: []string{
				`2 "ORD-F01 Nbsp" id=ORD-F01 title="Nbsp" text="The system SHALL a."`,
				`4 "ORD-F02 Upper" id=ORD-F02 title="Upper" text="The system SHALL b."`,
				`8 "[ORD-F04] Bracket" id= title="" text="The system SHALL d."`,
				`10 "ORD-F05 Next line\u0085" id=ORD-F05 title="Next line\u0085" text="The system SHALL e."`, // JavaScript's trim keeps U+0085
			},
		},
		{
			name: "only the Requirements section",
			src:  "## Purpose\n### Requirement: ORD-F09 Before\n## Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n## Other\n### Requirement: ORD-F02 After\n",
			want: []string{`4 "ORD-F01 One" id=ORD-F01 title="One" text="The system SHALL one."`},
		},
		{
			name: "no Requirements section",
			src:  "## Requirement\u017f\n### Requirement: ORD-F01 Long s\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, found := parseSpec("a", "spec.md", []byte(tt.src))
			var got []string
			for _, q := range s.Requirements {
				got = append(got, render(q))
				if q.Path != "spec.md" {
					t.Errorf("%s: path %q", q.Name, q.Path)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("requirements:\n got %q\nwant %q", got, tt.want)
			}
			var strays []int
			for _, f := range found {
				if f.Rule == RuleStrayHeading {
					strays = append(strays, f.Line)
				}
			}
			if !slices.Equal(strays, tt.strays) {
				t.Errorf("stray headings at %v, want %v", strays, tt.strays)
			}
		})
	}
}

func TestParseDelta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "sections merged in operation order",
			src: "## MODIFIED Requirements\n### Requirement: ORD-F01 One\nThe system SHALL one.\n" +
				"## added requirements\n### Requirement: ORD-F02 Two\nThe system SHALL two.\n" +
				"## ADDED  Requirements\n### Requirement: ORD-F03 Double space title\n" +
				"## ADDED Requirements\n### Requirement: ORD-F04 Four\nThe system SHALL four.\n" +
				"```\n## RENAMED Requirements\n```\n" +
				"## Notes\n### Requirement: ORD-F05 Outside a delta section\n" +
				"## ADDED Requirement\u017f\n### Requirement: ORD-F06 Long s\n",
			want: []string{"ADDED 5 ORD-F02 Two", "ADDED 10 ORD-F04 Four", "MODIFIED 2 ORD-F01 One"},
		},
		{
			name: "REMOVED headers and bullets in document order",
			src: "## REMOVED Requirements\n- `### Requirement: ORD-F01 One`\n### Requirement: ORD-F02 Two\n**Reason**: gone\n" +
				"* ### Requirement: ORD-F03 Three\n+ `### Requirement: ORD-F04 Four`\n```\n- `### Requirement: ORD-F09 Fenced`\n```\n" +
				"- ### requirement: ORD-F08 Bullets are case-sensitive\n",
			want: []string{"REMOVED 2 ORD-F01 One", "REMOVED 3 ORD-F02 Two", "REMOVED 5 ORD-F03 Three", "REMOVED 6 ORD-F04 Four"},
		},
		{
			name: "RENAMED pairs",
			src: "## RENAMED Requirements\n- FROM: `### Requirement: ORD-F01 One`\n- TO: `### Requirement: ORD-F01 Uno`\n" +
				"FROM: ### Requirement: ORD-F02 Two\n  + TO:### Requirement: ORD-F02 Dos\n" +
				"- from: `### Requirement: ORD-F03 Lower-case`\n",
			want: []string{"RENAMED 2 ORD-F01 One -> 3 ORD-F01 Uno", "RENAMED 4 ORD-F02 Two -> 5 ORD-F02 Dos"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deltas, _ := parseDelta("a/b", "d.md", []byte(tt.src))
			var got []string
			for _, d := range deltas {
				if d.Capability != "a/b" {
					t.Errorf("capability %q", d.Capability)
				}
				if d.Op == Renamed {
					got = append(got, fmt.Sprintf("%s %d %s -> %d %s", d.Op, d.From.Line, d.From.Name, d.To.Line, d.To.Name))
					continue
				}
				got = append(got, fmt.Sprintf("%s %d %s", d.Op, d.Requirement.Line, d.Requirement.Name))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("deltas:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestCaseless(t *testing.T) {
	t.Parallel()
	if got, want := caseless("Scenario: 1."), `[Ss][Cc][Ee][Nn][Aa][Rr][Ii][Oo]: 1\.`; got != want {
		t.Errorf("caseless = %q, want %q", got, want)
	}
}
