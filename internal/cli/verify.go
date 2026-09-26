package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/envelope"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/hook"
	"github.com/svallejo-dev/aval/internal/summary"
	"github.com/svallejo-dev/aval/internal/ui"
	"github.com/svallejo-dev/aval/internal/verify"
)

// notCollectedApprovals is the token that records, in the bundle's
// notCollected, that no review was judged (ADR-0005 §7). Its point is that the
// absence is explicit: without it, a bundle with an empty approvals list and a
// tier-3 verdict would be indistinguishable from one where nobody had
// approved, and "aval did not look" would read as "nobody approved".
const notCollectedApprovals = "approvals"

const verifyLong = `verify gathers the evidence for a range, applies the gate's rules to it and
writes the bundle to .aval/evidence/<head>.json, together with the status the
agent hooks read (ADR-0005 §7).

A range has two bases and they are not the same commit. The TRUST BASE is the
tip of the default branch, and the policy, CODEOWNERS, the baseline and the lint
configuration all come from there, because nobody who opens a pull request gets
to choose it. The CHANGE BASE is where the range starts — the merge base of the
head with the branch the pull request targets — and everything about what the
pull request did is measured from there, because measuring against a branch tip
would attribute other people's commits to it.

An empty range is never a pass: a change base that does not resolve, or that is
the head itself, is exit 2.

verify makes no network call, so it judges no approval: the bundle records
approvals as not collected, and a tier-3 change therefore always carries
approval_missing. The exit code is ADR-0005 §6 unchanged — 0 in observe mode, 1
for a block in enforce mode, 2 for an invalid invocation, 3 for a missing tool —
but the status the agent hooks read says passed when approval_missing is the only
thing blocking, because an agent cannot obtain one and a Stop hook that waited
for it would never let the agent finish. Only that one reason is exempt.`

func newVerifyCmd(g *globalFlags) *cobra.Command {
	var f rangeFlags
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Gather the evidence for base..head and write the bundle",
		Long:  verifyLong,
		Args:  cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			return runVerify(cmd.Context(), cmd, g, f)
		}),
	}
	f.bind(cmd)
	return cmd
}

func runVerify(ctx context.Context, cmd *cobra.Command, g *globalFlags, f rangeFlags) error {
	root, gr, err := openRepo(ctx, f.dir)
	if err != nil {
		return err
	}
	r, err := resolveRange(ctx, gr, f, trustRefs(ctx, gr, ""), nil)
	if err != nil {
		return err
	}
	ev, b, err := collect(ctx, verify.Options{
		Dir:         root,
		TrustBase:   r.trust,
		Base:        r.change,
		BaseRef:     r.ref,
		Head:        r.head,
		Repo:        originRepo(ctx, gr),
		AvalVersion: readVersion().Version,
	})
	if err != nil {
		return err
	}
	b.NotCollected = append(b.NotCollected, notCollectedApprovals)
	// The verdict is the gate's and the status is the agent's: the only reason
	// the agent is excused from is the approval it cannot obtain (ADR-0005 §7).
	if err := ev.Save(b, verify.WithAgentExempt(hook.ExemptApprovalMissing)); err != nil {
		return fmt.Errorf("save the evidence: %w", err)
	}
	return report(cmd, g, "verify", b, gate.ExitCode(ev.Input.Mode(), b.Verdict))
}

// collect gathers the evidence for o's range, decides on it and assembles the
// bundle. Both commands go through here, because the verdict has to come from
// evidence gathered in this process: a bundle on disk proves only which commit
// it names, so reusing one would be trusting a file, verdict included
// (ADR-0005 §7).
func collect(ctx context.Context, o verify.Options) (*verify.Evidence, evidence.Bundle, error) {
	ev, err := verify.Collect(ctx, o)
	if err != nil {
		return nil, evidence.Bundle{}, collectError(err)
	}
	return ev, ev.Bundle(gate.Decide(ev.Input)), nil
}

// collectError maps what verify reports to an exit code (ADR-0005 §6): a range
// or a base policy aval cannot use is an invalid invocation, a missing tool or
// one of the wrong version is exit 3, and anything else is a plain failure.
func collectError(err error) error {
	switch {
	case errors.Is(err, verify.ErrUsage):
		return usageError(err)
	case errors.Is(err, verify.ErrTool):
		return &ExitError{Code: ExitTool, Err: err}
	}
	return err
}

// report prints the bundle's report in the mode the flags resolved and turns
// code into the command's own outcome: in json mode the reasons make the
// envelope's ok false, and otherwise they follow the report on stderr
// (ADR-0003).
func report(cmd *cobra.Command, g *globalFlags, name string, b evidence.Bundle, code int) error {
	r := ui.Result{Command: name, Data: b, Lines: textLines(summary.Text(b))}
	if code != ExitOK {
		r.Issues = []envelope.Issue{{Code: errorCode(code), Message: name + " failed: " + summary.Line(b)}}
	}
	if err := g.printer(cmd).Print(r); err != nil {
		return fmt.Errorf("print the %s report: %w", name, err)
	}
	if code != ExitOK {
		return &ExitError{Code: code} // Print reported why
	}
	return nil
}

// textLines turns the plain-text report into the printer's lines. tui shows
// the same report for now; the live view is a later milestone (ADR-0003).
func textLines(s string) []ui.Line {
	s = strings.TrimRight(s, "\n")
	lines := make([]ui.Line, 0, strings.Count(s, "\n")+1)
	for line := range strings.SplitSeq(s, "\n") {
		lines = append(lines, ui.Line{{Text: line}})
	}
	return lines
}
