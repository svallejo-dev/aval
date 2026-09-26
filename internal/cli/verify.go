package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/envelope"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/hook"
	"github.com/svallejo-dev/aval/internal/manifest"
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

const verifyLong = `verify gathers the evidence for the range base..head, applies the gate's rules
to it and writes the bundle to .aval/evidence/<head>.json, together with the
status the agent hooks read (ADR-0005 §7).

It makes no network call, so it judges no approval: the bundle records
approvals as not collected, and a tier-3 change therefore always carries
approval_missing. That is the one reason an agent cannot do anything about, and
a Stop hook that treated it as a failure would keep the agent working for ever.
So verify fails only for a block an agent can act on: when nothing but
approval_missing blocks, it exits 0 and leaves the hook status passed. Every
other code is ADR-0005 §6: 0 in observe mode, 1 for a block in enforce mode, 2
for an invalid invocation and 3 for a missing tool.`

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
	base, head, err := resolveRange(ctx, gr, f, defaultBaseRefs)
	if err != nil {
		return err
	}
	ev, b, err := collect(ctx, verify.Options{
		Dir:         root,
		Base:        base,
		Head:        head,
		Repo:        originRepo(ctx, gr),
		AvalVersion: readVersion().Version,
	})
	if err != nil {
		return err
	}
	b.NotCollected = append(b.NotCollected, notCollectedApprovals)
	if err := ev.Save(b); err != nil {
		return fmt.Errorf("save the evidence: %w", err)
	}
	if err := passForAgent(ev.Root, b.Verdict); err != nil {
		return err
	}
	return report(cmd, g, "verify", b, verifyExitCode(b))
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

// verifyExitCode is ADR-0005 §6 with one deliberate exception, the one stated
// in verifyLong: observe never fails and enforce fails on a block, except that
// a block whose only blocking reason is approval_missing exits 0, because
// verify does not read reviews and an agent cannot obtain one.
func verifyExitCode(b evidence.Bundle) int {
	if b.Mode == string(manifest.Observe) || !blocksAgent(b.Verdict) {
		return ExitOK
	}
	return ExitFailed
}

// blocksAgent reports whether the verdict blocks for something other than a
// missing approval (ADR-0005 §4, approval_missing). It reads the reasons rather
// than the result, so an override that softened the result to a warn still
// leaves the blocking reasons visible to the agent.
func blocksAgent(v evidence.Verdict) bool {
	return v.Result == evidence.ResultBlock && slices.ContainsFunc(v.Reasons, func(r evidence.Reason) bool {
		return r.Code != gate.CodeApprovalMissing && gate.Effect(r.Code) == evidence.ResultBlock
	})
}

// passForAgent relaxes the hook status Save just wrote when the only thing that
// blocks is a missing approval: the Stop hook reads it to decide whether the
// agent may finish, and an approval is what the agent cannot get (ADR-0005 §5).
//
// It reads the status back instead of writing a fresh one so that the key stays
// the one Save stored it under, which describes the working tree as it was
// before anything ran (§7). The bundle keeps the real verdict; only this
// advisory file changes.
func passForAgent(root string, v evidence.Verdict) error {
	if v.Result != evidence.ResultBlock || blocksAgent(v) {
		return nil
	}
	s, err := hook.ReadStatus(root)
	if err != nil {
		return fmt.Errorf("read the verify status back: %w", err)
	}
	s.Passed = true
	if err := hook.WriteStatus(root, s); err != nil {
		return fmt.Errorf("relax the verify status: %w", err)
	}
	return nil
}
