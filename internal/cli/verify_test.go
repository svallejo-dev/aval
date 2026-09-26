package cli

import (
	"testing"

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

// TestVerifyExitCode pins ADR-0005 §6 as both commands apply it, with no
// exception: a block in enforce mode is exit 1 even when the only thing blocking
// is the approval verify never reads. The exemption is the hook status's, not the
// exit code's, so a human running verify before pushing still sees a failure.
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
		{name: "a block", mode: manifest.Enforce, codes: []string{gate.CodeUnverified}, want: ExitFailed},
		{name: "observe never fails", mode: manifest.Observe, codes: []string{gate.CodeUnverified}, want: ExitOK},
		{
			name:  "approval_missing alone still fails in enforce mode",
			mode:  manifest.Enforce,
			codes: []string{gate.CodeApprovalMissing},
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
			if got := gate.ExitCode(tt.mode, reasons(tt.codes...)); got != tt.want {
				t.Errorf("ExitCode(%s, %q) = %d, want %d", tt.mode, tt.codes, got, tt.want)
			}
		})
	}
}

// TestExemptCodeMatchesTheGate keeps internal/hook's spelling of the one exempt
// reason code equal to internal/gate's. hook cannot import gate — gate reaches
// openspec, yaml and a JSON Schema compiler, and a hook has 50 ms (ADR-0003) — so
// this is the only thing holding the two together.
func TestExemptCodeMatchesTheGate(t *testing.T) {
	t.Parallel()
	if hook.ExemptApprovalMissing != gate.CodeApprovalMissing {
		t.Errorf("hook.ExemptApprovalMissing = %q, gate.CodeApprovalMissing = %q; they must be the same code",
			hook.ExemptApprovalMissing, gate.CodeApprovalMissing)
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
