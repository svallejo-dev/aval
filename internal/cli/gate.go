package cli

import (
	"cmp"
	"context"
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

A range has two bases. The TRUST BASE is the tip of the repository's default
branch, and the policy, CODEOWNERS, the baseline and the lint configuration come
from there; the CHANGE BASE is the merge base of the head with the branch the
pull request targets, and what the pull request did is measured from there. An
empty range is exit 2, never a pass.

In GitHub Actions it takes all of that from the event — only pull_request and
pull_request_review, the events of ADR-0005 §8 — reads the reviews that approve
or override it, writes the report to $GITHUB_STEP_SUMMARY and leaves the bundle
in .aval/evidence/ for the workflow to upload. --trust-base, --change-base and
--head are refused there: the range is the event's, not the workflow's to pick.
The workflow must check out github.event.pull_request.head.sha with
fetch-depth: 0, and grant contents: read, pull-requests: read and checks: read.

Without a token, or when the API refuses, the gate carries on without approvals
and says so on stderr and in the bundle: it never lets GitHub's availability
decide whether a pull request can merge. The evidence is always gathered in this
process, never read back from a bundle on disk (ADR-0005 §7).

Outside Actions it judges the range the flags name, with no approvals and no
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
	// Before anything looks at the event: a workflow may not name its own range,
	// and that has to hold whatever event fired, not only for the ones the gate
	// goes on to accept (ADR-0005 §1). resolveActionsRange refuses them too, for
	// any caller; this is the guarantee stated where it is easy to see.
	if err := refuseRangeFlags(ac.InActions, f); err != nil {
		return err
	}
	pr, err := eventPullRequest(ac)
	if err != nil {
		return err
	}
	root, gr, err := openRepo(ctx, f.dir)
	if err != nil {
		return err
	}
	// The gate may ask the API which branch is the default one; verify may not,
	// because it makes no network call, so it exits 2 instead (runVerify).
	r, err := resolveActionsRange(ctx, gr, f, ac.InActions, defaultBranch(ctx, ac, o.http), pr)
	if err != nil {
		return err
	}

	ap := reviewApprovals(ctx, ac, gr, r.trust, r.head, o.http)
	if pr != nil {
		if ap.skipped != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "aval: no approvals were judged, the gate carries on without them: %s\n", ap.skipped)
		} else if ap.note != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "aval: %s\n", ap.note)
		}
	}
	ev, b, err := collect(ctx, verify.Options{
		Dir:         root,
		TrustBase:   r.trust,
		Base:        r.change,
		BaseRef:     r.ref,
		Head:        r.head,
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
	// The bundle only: the status under .aval/cache/ is `aval verify`'s, for the
	// agent hooks on a developer's own working tree (ADR-0005 §7). The gate runs
	// in CI over a checkout no hook watches, and overwriting it would answer a
	// question nobody asked here — with the verdict, which is not what the status
	// says.
	if err := ev.Save(b, verify.WithoutHookStatus()); err != nil {
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

// defaultBranch names the repository's default branch, whose tip is the trust
// base (ADR-0005 §1): from the event payload, then from the API, and "" when
// neither says, which leaves the resolver to the remote's own refs.
//
// It is the default branch and not the branch the pull request targets. A
// stacked pull request targets a branch its own author pushes to, and taking the
// policy, the CODEOWNERS and the baseline from there would let the author write
// all three in a commit outside this pull request's diff, so nothing would read
// as tamper.
func defaultBranch(ctx context.Context, ac *actions.Context, hc *http.Client) string {
	if ac.DefaultBranch != "" {
		return ac.DefaultBranch
	}
	if !ac.HasToken || ac.FullName() == "" {
		return ""
	}
	c := &github.Client{BaseURL: ac.APIURL, Token: ac.Token(), HTTP: hc}
	name, err := c.DefaultBranch(ctx, ac.Owner, ac.Repo)
	if err != nil {
		// Nothing named a branch. Inside Actions that is exit 2, which
		// resolveActionsRange decides; outside it, the remote's own refs answer.
		return ""
	}
	return name
}

// approvalsOf is what the gate learned about the pull request's reviews.
type approvalsOf struct {
	approvals []evidence.Approval
	// skipped says why no review was judged, and is empty when they were. The
	// gate records it in the bundle's notCollected and warns on stderr, so that
	// approvals aval never looked at cannot read as approvals nobody gave
	// (ADR-0005 §5, §7).
	skipped string
	// note is something worth saying about what was read, without anything having
	// gone wrong: which CODEOWNERS the owners came from when there are none.
	note string
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
	owners, from, err := baseOwners(ctx, g, base)
	if err != nil {
		return approvalsOf{skipped: err.Error()}
	}
	// Which file the owners came from, said out loud when there are none: every
	// approval will then be rejected as "not a code owner", and a reviewer
	// reading that deserves to know whether aval found no file or found one that
	// names nobody.
	note := ""
	if len(owners) == 0 {
		why := "none of " + strings.Join(codeowners.Locations(), ", ") + " is there"
		if from != "" {
			why = from + " names none"
		}
		note = "nobody owns aval.yaml at the trust base (" + why +
			"), so only an admin or a maintainer can approve this pull request"
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
	return approvalsOf{approvals: approval.Evaluate(reviews, head, ac.PullRequest.Author, owners, access), note: note}
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
// the trust base, and which of the three locations it read them from.
//
// The walk over codeowners.Locations() is not a fallback over equivalent
// candidates: it is GitHub's own lookup order, and GitHub uses the first file
// that exists and no other. Stopping at a later one would assign owners GitHub
// does not, so aval has to do exactly this. No file at all means no individual
// owners, which is stricter and not weaker: only admins and maintainers count
// then (ADR-0005 §5).
//
// The file comes out of git's object database with cat-file, never from the
// working tree, which is head's, and never with git archive, which would honour
// an export-ignore in .gitattributes and hand back nothing (ADR-0005 §1).
func baseOwners(ctx context.Context, g *git.Runner, base string) (owners []string, from string, err error) {
	locs := codeowners.Locations()
	var req strings.Builder
	for _, l := range locs {
		req.WriteString(base + ":" + l + "\n")
	}
	// --batch-check answers one line per request, in order: "<oid> <type>
	// <size>", or "<request> missing" for a path the commit does not have.
	out, err := g.Run(ctx, []byte(req.String()), "cat-file", "--batch-check")
	if err != nil {
		return nil, "", fmt.Errorf("read CODEOWNERS at %s: %w", base, err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(locs) {
		return nil, "", fmt.Errorf("read CODEOWNERS at %s: git cat-file answered %d lines for %d paths", base, len(lines), len(locs))
	}
	for i, l := range locs {
		f := strings.Fields(lines[i])
		if len(f) != 3 || f[1] != "blob" { // not there, or not a file
			continue
		}
		if size, err := strconv.ParseInt(f[2], 10, 64); err != nil || size < 0 || size >= codeowners.MaxSize {
			// GitHub loads no CODEOWNERS of 3 MB or more, so neither does aval:
			// it would assign owners GitHub does not.
			return nil, "", fmt.Errorf("%s at %s: %q is not a size GitHub would load", l, base, f[2])
		}
		data, err := g.Run(ctx, nil, "cat-file", "blob", "--end-of-options", f[0])
		if err != nil {
			return nil, "", fmt.Errorf("read %s at %s: %w", l, base, err)
		}
		rules, err := codeowners.Parse(data)
		if err != nil {
			return nil, "", fmt.Errorf("parse %s at %s: %w", l, base, err)
		}
		return rules.Owners("aval.yaml"), l, nil
	}
	return nil, "", nil
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
