package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/hook"
	"github.com/svallejo-dev/aval/internal/manifest"
)

// reasons builds a verdict out of reason codes, with the result their effects
// justify, the way gate.Decide would.
func reasons(codes ...string) evidence.Verdict {
	v := evidence.Verdict{Result: evidence.ResultPass, Reasons: []evidence.Reason{}}
	for _, c := range codes {
		v.Reasons = append(v.Reasons, evidence.Reason{Code: c, Message: c})
		if gate.Effect(c) == evidence.ResultBlock {
			v.Result = evidence.ResultBlock
		} else if v.Result == evidence.ResultPass {
			v.Result = evidence.ResultWarn
		}
	}
	return v
}

// TestVerifyExitCode pins ADR-0005 §6 as verify applies it, exception included:
// a block whose only blocking reason is approval_missing exits 0, because
// verify reads no reviews and an agent cannot obtain one.
func TestVerifyExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		mode  manifest.Mode
		codes []string
		want  int
	}{
		{name: "nothing to report", mode: manifest.Enforce, want: ExitOK},
		{name: "only warnings", mode: manifest.Enforce, codes: []string{gate.CodeSeamTouched, gate.CodeWeakEvidence}, want: ExitOK},
		{name: "a block an agent can act on", mode: manifest.Enforce, codes: []string{gate.CodeUnverified}, want: ExitFailed},
		{name: "observe never fails", mode: manifest.Observe, codes: []string{gate.CodeUnverified}, want: ExitOK},
		{
			name:  "approval_missing alone does not fail: no agent can resolve it",
			mode:  manifest.Enforce,
			codes: []string{gate.CodeApprovalMissing},
			want:  ExitOK,
		},
		{
			name:  "approval_missing with warnings still does not fail",
			mode:  manifest.Enforce,
			codes: []string{gate.CodeApprovalMissing, gate.CodeAssumption, gate.CodeNoBasePolicy},
			want:  ExitOK,
		},
		{
			name:  "approval_missing beside a block an agent can act on fails",
			mode:  manifest.Enforce,
			codes: []string{gate.CodeApprovalMissing, gate.CodeAfterNotPassing},
			want:  ExitFailed,
		},
		{
			name:  "a reserved code blocks, because Effect fails closed",
			mode:  manifest.Enforce,
			codes: []string{gate.CodeContractBreaking},
			want:  ExitFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := evidence.Bundle{Mode: string(tt.mode), Verdict: reasons(tt.codes...)}
			if got := verifyExitCode(b); got != tt.want {
				t.Errorf("verifyExitCode(%s, %q) = %d, want %d", tt.mode, tt.codes, got, tt.want)
			}
		})
	}
}

// TestBlocksAgentIgnoresTheResult checks that blocksAgent reads the reasons and
// not only the result: an override lowers the result to a warn, and the gate's
// own exit code handles that, so verify must not report a failure there.
func TestBlocksAgentIgnoresTheResult(t *testing.T) {
	t.Parallel()
	overridden := reasons(gate.CodeUnverified)
	overridden.Result = evidence.ResultWarn
	if blocksAgent(overridden) {
		t.Error("blocksAgent = true for an overridden verdict, want false: the result is a warn")
	}
}

func TestPassForAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		verdict    evidence.Verdict
		passed     bool // the status Save wrote
		wantPassed bool
	}{
		{
			name:       "a block on a missing approval alone is relaxed",
			verdict:    reasons(gate.CodeApprovalMissing),
			wantPassed: true,
		},
		{
			name:    "a block an agent can act on is left alone",
			verdict: reasons(gate.CodeApprovalMissing, gate.CodeUnverified),
		},
		{
			name:       "a warn is left alone",
			verdict:    reasons(gate.CodeSeamTouched),
			passed:     true,
			wantPassed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			key := hook.Key{Head: strings.Repeat("a", 40), Diff: strings.Repeat("b", 64)}
			at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
			if err := hook.WriteStatus(root, hook.Status{Key: key, Passed: tt.passed, VerifiedAt: at}); err != nil {
				t.Fatal(err)
			}
			if err := passForAgent(root, tt.verdict); err != nil {
				t.Fatalf("passForAgent: %v", err)
			}
			got, err := hook.ReadStatus(root)
			if err != nil {
				t.Fatal(err)
			}
			if got.Passed != tt.wantPassed {
				t.Errorf("passed = %v, want %v", got.Passed, tt.wantPassed)
			}
			// The key and the time must survive: they describe the working tree
			// as it was before anything ran (ADR-0005 §7).
			if got.Key != key || !got.VerifiedAt.Equal(at) {
				t.Errorf("status = %+v, want key %+v and verifiedAt %s untouched", got, key, at)
			}
		})
	}
}

// TestPassForAgentWithoutStatus checks that a missing status is an error rather
// than a silent pass: the hook would otherwise keep blocking with no way to
// find out why.
func TestPassForAgentWithoutStatus(t *testing.T) {
	t.Parallel()
	err := passForAgent(t.TempDir(), reasons(gate.CodeApprovalMissing))
	if err == nil || !errors.Is(err, hook.ErrNoStatus) {
		t.Errorf("passForAgent without a status = %v, want an error wrapping ErrNoStatus", err)
	}
}

func TestTextLines(t *testing.T) {
	t.Parallel()
	got := textLines("one\n\nthree\n")
	want := []string{"one", "", "three"}
	if len(got) != len(want) {
		t.Fatalf("textLines gave %d lines, want %d: %q", len(got), len(want), got)
	}
	for i, l := range got {
		if len(l) != 1 || l[0].Text != want[i] {
			t.Errorf("line %d = %+v, want the single span %q", i, l, want[i])
		}
	}
}
