package hook

import (
	"context"
	"path/filepath"
	"testing"
)

// shopFiles is a repository whose active change adds ORD-F02, modifies
// ORD-N01, removes ORD-F04 and renames ORD-F05 to ORD-F06, and whose archived
// change added ORD-F03.
var shopFiles = map[string]string{
	"openspec/changes/partial/specs/refunds/spec.md": "## ADDED Requirements\n\n### Requirement: ORD-F02 Partial refunds\nThe system SHALL refund part of an order.\n\n" +
		"## MODIFIED Requirements\n\n### Requirement: ORD-N01 Refund cap\nThe system SHALL NOT refund more than the total.\n\n" +
		"## REMOVED Requirements\n\n- `### Requirement: ORD-F04 Manual refunds`\n\n" +
		"## RENAMED Requirements\n\n- FROM: `### Requirement: ORD-F05 Refund mail`\n- TO: `### Requirement: ORD-F06 Refund notice`\n",
	"openspec/changes/archive/2026-01-01-ledger/specs/refunds/spec.md": "## ADDED Requirements\n\n### Requirement: ORD-F03 Ledger entry\nThe system SHALL write a ledger entry.\n",
	"refund/refund.go": "package refund\n",
	"refund/refund_test.go": `package refund

import "testing"

func TestRefund(t *testing.T) {
	t.Run("ORD-F01 refunds once", func(t *testing.T) {})
	t.Run("ORD-F02 refunds half", func(t *testing.T) {})
	t.Run("ORD-F03 writes a ledger entry", func(t *testing.T) {})
	t.Run("ORD-N01 never exceeds the total", func(t *testing.T) {})
	t.Run("ORD-F01 again", func(t *testing.T) {})
}
`,
	"refund/table_test.go": "package refund\n\nimport \"testing\"\n\nfunc TestTable(t *testing.T) {\n\tfor _, tt := range cases {\n\t\tt.Run(tt.name, func(t *testing.T) {})\n\t}\n}\n",
	"refund/cases_test.go": "package refund\n\nvar cases = []struct{ name string }{{name: \"ORD-I01 balances\"}}\n",
	"refund/delta_test.go": `package refund

import "testing"

func TestDelta(t *testing.T) {
	t.Run("ORD-F05 mail", func(t *testing.T) {})
	t.Run("ORD-F06 notice", func(t *testing.T) {})
	t.Run("ORD-F04 manual", func(t *testing.T) {})
}
`,
	"refund/only_delta_test.go": `package refund

import "testing"

func TestOnlyDelta(t *testing.T) {
	t.Run("ORD-F02 partial", func(t *testing.T) {})
	t.Run("ORD-N01 bounded", func(t *testing.T) {})
	t.Run("ORD-F04 manual", func(t *testing.T) {})
}
`,
	"refund/plain_test.go": "package refund\n\nimport \"testing\"\n\nfunc TestPlain(t *testing.T) { t.Run(\"works\", func(t *testing.T) {}) }\n",
}

func TestCheckBoundTests(t *testing.T) {
	t.Parallel()
	shop := newRepo(t, shopFiles)
	writeFiles(t, shop, map[string]string{"broken/broken_test.go": "package broken\n\nfunc {"})
	bare := newRepo(t, map[string]string{"refund/refund_test.go": shopFiles["refund/refund_test.go"]}) // no OpenSpec tree
	edit := func(tool, cwd, file string) input {
		return input{Cwd: cwd, ToolName: tool, ToolInput: toolInput{FilePath: file}}
	}
	file := func(root, name string) string { return filepath.Join(root, "refund", name) }

	const outside = ", which are not in the delta of any active OpenSpec change"
	tests := []struct {
		name string
		in   input
		want string // the IDs part of the reminder; "" means no answer
	}{
		{"IDs outside the delta", edit("Edit", shop, file(shop, "refund_test.go")), "ORD-F01, ORD-F03" + outside},
		{"relative to cwd", edit("Write", shop, "refund/refund_test.go"), "ORD-F01, ORD-F03" + outside},
		{"from a subdirectory", edit("Edit", filepath.Join(shop, "refund"), file(shop, "refund_test.go")), "ORD-F01, ORD-F03" + outside},
		{"table in another file", edit("Write", shop, file(shop, "table_test.go")), "ORD-I01" + outside},
		{"renamed IDs stay guarded", edit("Edit", shop, file(shop, "delta_test.go")), "ORD-F05, ORD-F06" + outside},
		{"no OpenSpec tree", edit("Edit", bare, file(bare, "refund_test.go")), "ORD-F01, ORD-F02, ORD-F03, ORD-N01"},
		{"only delta IDs", edit("Edit", shop, file(shop, "only_delta_test.go")), ""},
		{"no ID", edit("Edit", shop, file(shop, "plain_test.go")), ""},
		{"only the data of a table", edit("Edit", shop, file(shop, "cases_test.go")), ""},
		{"not a test file", edit("Edit", shop, file(shop, "refund.go")), ""},
		{"not an edit", edit("Read", shop, file(shop, "refund_test.go")), ""},
		{"no tool name", edit("", shop, file(shop, "refund_test.go")), ""},
		{"outside the repository", edit("Edit", shop, file(bare, "refund_test.go")), ""},
		{"cwd outside a repository", edit("Edit", t.TempDir(), file(shop, "refund_test.go")), ""},
		{"missing file", edit("Write", shop, file(shop, "gone_test.go")), ""},
		{"does not parse", edit("Write", shop, filepath.Join(shop, "broken", "broken_test.go")), ""},
		{"no file", edit("Edit", shop, ""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := checkBoundTests(context.Background(), tt.in)
			if tt.want == "" {
				if got != nil {
					t.Errorf("checkBoundTests() = %+v, want nil", got.Specific)
				}
				return
			}
			want := "aval: this file binds tests to " + tt.want + ". " + tamperNote
			if got == nil || got.Decision != "" || got.Specific == nil || *got.Specific != (specific{HookEventName: "PostToolUse", AdditionalContext: want}) {
				t.Errorf("checkBoundTests() = %+v, want additional context %q", got, want)
			}
		})
	}
}
