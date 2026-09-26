package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/actions"
	"github.com/svallejo-dev/aval/internal/approval"
	"github.com/svallejo-dev/aval/internal/codeowners"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gate"
	"github.com/svallejo-dev/aval/internal/github"
	"github.com/svallejo-dev/aval/internal/platform/git"
	"github.com/svallejo-dev/aval/internal/summary"
	"github.com/svallejo-dev/aval/internal/verify"
)

const gateLong = `gate decides whether a pull request may merge and exits with the code ADR-0005
§6 fixes: 0 in observe mode and for a pass or a warn, 1 for a block in enforce
mode, 2 for an invalid invocation and 3 for a missing tool.

In GitHub Actions it takes the pull request from the event — only pull_request
and pull_request_review, the events of ADR-0005 §8 — reads the reviews that
approve or override it, writes the report to $GITHUB_STEP_SUMMARY and leaves the
bundle in .aval/evidence/ for the workflow to upload. The workflow must check out
github.event.pull_request.head.sha with fetch-depth: 0, and grant contents: read,
pull-requests: read and checks: read.

Without a token, or when the API refuses, the gate carries on without approvals
and says so on stderr and in the bundle: it never lets GitHub's availability
decide whether a pull request can merge. The evidence is always gathered in this
process, never read back from a bundle on disk (ADR-0005 §7).

Outside Actions it judges the range --base..--head, with no approvals and no
step summary.`

// gateOptions are the seams `aval gate` is tested through: where the GitHub
// Actions environment comes from, and the HTTP client the API is read with.
type gateOptions struct {
	getenv func(string) string
	http   *http.Client // nil is the github package's own client
}

func newGateCmd(g *globalFlags) *cobra.Command {
	var f rangeFlags
	o := gateOptions{getenv: os.Getenv}
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Decide whether a pull request may merge, with an exit code CI can gate on",
		Long:  gateLong,
		Args:  cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			return runGate(cmd.Context(), cmd, g, f, o)
		}),
	}
	f.bind(cmd)
	return cmd
}

func runGate(ctx context.Context, cmd *cobra.Command, g *globalFlags, f rangeFlags, o gateOptions) error {
	ac := actions.Detect(o.getenv)
	pr, err := eventPullRequest(ac)
	if err != nil {
		return err
	}
	root, gr, err := openRepo(ctx, f.dir)
	if err != nil {
		return err
	}
	// Inside Actions the base comes from the event, unless --base overrides it.
	refs := defaultBaseRefs
	if pr != nil && f.base == "" {
		if refs, err = eventBaseRefs(pr); err != nil {
			return err
		}
	}
	base, head, err := resolveRange(ctx, gr, f, refs)
	if err != nil {
		return err
	}
	if pr != nil && !strings.EqualFold(head, pr.HeadSHA) {
		return usageError(fmt.Errorf("the checked-out commit is %s but the event is about %s: "+
			"the workflow must check out github.event.pull_request.head.sha with fetch-depth: 0, "+
			"not the merge commit actions/checkout uses by default (ADR-0005 §8)", head, pr.HeadSHA))
	}

	ap := reviewApprovals(ctx, ac, gr, base, head, o.http)
	if ap.skipped != "" && pr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "aval: no approvals were judged, the gate carries on without them: %s\n", ap.skipped)
	}
	ev, b, err := collect(ctx, verify.Options{
		Dir:         root,
		Base:        base,
		Head:        head,
		Approvals:   ap.approvals,
		Repo:        cmp.Or(ac.FullName(), originRepo(ctx, gr)),
		AvalVersion: readVersion().Version,
	})
	if err != nil {
		return err
	}
	if ap.skipped != "" {
		b.NotCollected = append(b.NotCollected, notCollectedApprovals)
	}
	if err := ev.Save(b); err != nil {
		return fmt.Errorf("save the evidence: %w", err)
	}
	if ac.StepSummaryPath != "" {
		// A summary that did not fit, or a file the runner will not let aval
		// append to, is not the verdict: the bundle holds everything, and the
		// report still goes to stdout.
		if err := ac.WriteStepSummary(summary.Markdown(b)); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "aval: %v\n", err)
		}
	}
	return report(cmd, g, "gate", b, gate.ExitCode(ev.Input.Mode(), b.Verdict))
}

// eventPullRequest returns the pull request the GitHub Actions event is about,
// or nil outside Actions, where the gate judges the range its flags name.
//
// Inside Actions it refuses anything else, as an invalid invocation of the
// workflow: an event that is not one of ADR-0005 §8's either carries no head
// aval checked out or, like pull_request_target and workflow_run, runs with the
// base's permissions against an untrusted head; and a payload with no usable
// pull request leaves nothing to judge. Neither may read as a pass.
func eventPullRequest(ac *actions.Context) (*actions.PullRequest, error) {
	if !ac.InActions {
		return nil, nil
	}
	if ac.EventName != actions.EventPullRequest && ac.EventName != actions.EventPullRequestReview {
		return nil, usageError(fmt.Errorf("aval gate runs on the %s and %s events, not on %s (ADR-0005 §8)",
			actions.EventPullRequest, actions.EventPullRequestReview, quotedOr(ac.EventName, "no event")))
	}
	if ac.PullRequest == nil {
		return nil, usageError(fmt.Errorf("the %s event carries no usable pull request: %s", ac.EventName, gapList(ac)))
	}
	return ac.PullRequest, nil
}

// eventBaseRefs are the revisions the base comes from inside Actions, the most
// authoritative first.
//
// PullRequest.BaseBranchSHA is the TIP of the base branch as GitHub built the
// event, never the merge base, so it goes to resolveRange as a candidate to take
// a merge base with — using it as the base itself would put every commit merged
// into the base branch since the pull request was cut into the diff (ADR-0005
// §1). The branch's name follows it, for a payload whose base.sha the runner did
// not fetch.
func eventBaseRefs(pr *actions.PullRequest) ([]string, error) {
	var refs []string
	if pr.BaseBranchSHA != "" {
		refs = append(refs, pr.BaseBranchSHA)
	}
	if pr.BaseRef != "" {
		refs = append(refs, "refs/remotes/origin/"+pr.BaseRef, pr.BaseRef)
	}
	if len(refs) == 0 {
		return nil, usageError(errors.New("the event names no base branch: its payload has neither base.sha nor base.ref, " +
			"so there is no merge base to judge against"))
	}
	return refs, nil
}

// approvalsOf is what the gate learned about the pull request's reviews.
type approvalsOf struct {
	approvals []evidence.Approval
	// skipped says why no review was judged, and is empty when they were. The
	// gate records it in the bundle's notCollected and warns on stderr, so that
	// approvals aval never looked at cannot read as approvals nobody gave
	// (ADR-0005 §5, §7).
	skipped string
}

// reviewApprovals reads the pull request's reviews and judges them against the
// CODEOWNERS of the base (ADR-0005 §5).
//
// It never fails. Without a token, or when the API refuses, it degrades to no
// approvals and says why: a gate that stopped because GitHub had a bad minute
// would block every pull request, and degrading is the safe direction anyway —
// with no approvals, approval_missing keeps blocking at tier 3 and no override
// can lower a verdict.
func reviewApprovals(ctx context.Context, ac *actions.Context, g *git.Runner, base, head string, hc *http.Client) approvalsOf {
	if !ac.ApprovalsAvailable() {
		return approvalsOf{skipped: approvalsSkipped(ac)}
	}
	owners, err := baseOwners(ctx, g, base)
	if err != nil {
		return approvalsOf{skipped: err.Error()}
	}
	c := &github.Client{BaseURL: ac.APIURL, Token: ac.Token(), HTTP: hc}
	reviews, err := c.ListReviews(ctx, ac.Owner, ac.Repo, ac.PullRequest.Number)
	if err != nil {
		return approvalsOf{skipped: err.Error()}
	}
	// Every distinct reviewer's access, not only the ones who approved: what
	// counts as an override is approval's judgement, not this command's, and a
	// pull request has few reviewers.
	access := make(map[string]approval.Access, len(reviews))
	for _, r := range reviews {
		user := strings.ToLower(r.User)
		if r.User == "" { // GitHub reports no user: a deleted account owns nothing
			continue
		}
		if _, done := access[user]; done {
			continue
		}
		a, err := c.Permission(ctx, ac.Owner, ac.Repo, r.User)
		if err != nil {
			return approvalsOf{skipped: err.Error()}
		}
		access[user] = a
	}
	return approvalsOf{approvals: approval.Evaluate(reviews, head, ac.PullRequest.Author, owners, access)}
}

// approvalsSkipped says why there was no point calling the API at all. It names
// the variable a token comes from, never a token.
func approvalsSkipped(ac *actions.Context) string {
	switch {
	case !ac.InActions:
		return "not a GitHub Actions run, so there is no pull request whose reviews to read (ADR-0005 §5)"
	case ac.PullRequest == nil:
		return "the event carries no pull request"
	case ac.FullName() == "":
		return actions.EnvRepository + " does not name a repository"
	}
	return "no token: pass one in " + actions.EnvToken + ", from a job with pull-requests: read"
}

// baseOwners returns the users CODEOWNERS makes owners of the root aval.yaml at
// the base commit. GitHub reads the first of codeowners.Locations() that exists
// and no other, so a second file never adds an owner (ADR-0005 §5).
//
// The file comes out of git's object database with cat-file, never from the
// working tree, which is head's, and never with git archive, which would honour
// an export-ignore in .gitattributes and hand back nothing (ADR-0005 §1).
func baseOwners(ctx context.Context, g *git.Runner, base string) ([]string, error) {
	locs := codeowners.Locations()
	var req strings.Builder
	for _, l := range locs {
		req.WriteString(base + ":" + l + "\n")
	}
	// --batch-check answers one line per request, in order: "<oid> <type>
	// <size>", or "<request> missing" for a path the commit does not have.
	out, err := g.Run(ctx, []byte(req.String()), "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("read CODEOWNERS at %s: %w", base, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(locs) {
		return nil, fmt.Errorf("read CODEOWNERS at %s: git cat-file answered %d lines for %d paths", base, len(lines), len(locs))
	}
	for i, l := range locs {
		f := strings.Fields(lines[i])
		if len(f) != 3 || f[1] != "blob" { // not there, or not a file
			continue
		}
		if size, err := strconv.ParseInt(f[2], 10, 64); err != nil || size < 0 || size >= codeowners.MaxSize {
			// GitHub loads no CODEOWNERS of 3 MB or more, so neither does aval:
			// it would assign owners GitHub does not.
			return nil, fmt.Errorf("%s at %s: %q is not a size GitHub would load", l, base, f[2])
		}
		data, err := g.Run(ctx, nil, "cat-file", "blob", "--end-of-options", f[0])
		if err != nil {
			return nil, fmt.Errorf("read %s at %s: %w", l, base, err)
		}
		rules, err := codeowners.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s at %s: %w", l, base, err)
		}
		return rules.Owners("aval.yaml"), nil
	}
	return nil, nil
}

// gapList names what Detect wanted and did not get, for an error message. No
// Gap ever carries the token.
func gapList(ac *actions.Context) string {
	out := make([]string, 0, len(ac.Gaps))
	for _, gp := range ac.Gaps {
		out = append(out, gp.String())
	}
	if len(out) == 0 {
		return "no reason recorded"
	}
	return strings.Join(out, "; ")
}

// quotedOr quotes s, or returns fallback when it is empty.
func quotedOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return strconv.Quote(s)
}
