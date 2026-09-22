// Package approval decides which pull request reviews grant an approval or an
// override of the gate (ADR-0005 §5), and records every review that tried to,
// valid or not, as evidence.
package approval

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
)

// State is a review state as the GitHub REST API reports it.
type State string

// Review states.
const (
	Approved         State = "APPROVED"
	ChangesRequested State = "CHANGES_REQUESTED"
	Commented        State = "COMMENTED"
	Dismissed        State = "DISMISSED"
	Pending          State = "PENDING"
)

// Review is one pull request review.
type Review struct {
	ID          int64
	User        string // login; empty when GitHub reports no user (a deleted account)
	State       State
	CommitID    string // the commit the review was submitted on; empty if GitHub reports none
	SubmittedAt time.Time
	Body        string
}

// Roles that make a collaborator a code owner of the policy even when
// CODEOWNERS does not list them. They are role_name values: the legacy
// permission field reports maintain as write.
const (
	RoleAdmin    = "admin"
	RoleMaintain = "maintain"
)

// OverrideMarker starts the review body line that asks for an override; the
// rest of the line is the reason.
const OverrideMarker = "aval:override"

// Rejections recorded for reviews that do not count.
const (
	RejectSuperseded = "superseded by a later review"
	RejectStale      = "review of an earlier commit"
	RejectAuthor     = "the PR author cannot approve their own PR"
	RejectNotOwner   = "not a code owner"
	RejectNoReason   = "override without a reason"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Evaluate returns one Approval for every review that approves or asks for an
// override, in submission order (then by actor). A review is valid when:
//   - it is its author's latest review that is not COMMENTED, as GitHub
//     counts reviews, so a later CHANGES_REQUESTED or DISMISSED voids it;
//   - it is APPROVED and bound to head, the PR's head commit;
//   - its author is not prAuthor;
//   - its author is a code owner: listed in owners (the base CODEOWNERS
//     owners of the root aval.yaml, "@login" or "login") or holding
//     RoleAdmin or RoleMaintain in roles (login → role_name).
//
// An override also needs a non-empty reason. A review whose body has a line
// starting with OverrideMarker is an override even if that reason is empty,
// so it is rejected rather than counted as a plain approval.
//
// Logins match case-insensitively. Team entries (@org/team) in owners never
// match: expanding them needs read:org, which v0 does not have.
//
// Pending reviews, reviews without a user or a submission time, and reviews
// whose commit is not a full SHA grant nothing and are not recorded; the
// latter still supersede their author's earlier reviews. head must be a full
// lowercase SHA, so every returned Approval passes evidence.Bundle.Validate
// for a bundle whose head is head.
func Evaluate(reviews []Review, head, prAuthor string, owners []string, roles map[string]string) []evidence.Approval {
	isOwner := make(map[string]bool, len(owners)+len(roles))
	for _, o := range owners {
		isOwner[login(o)] = true
	}
	for u, r := range roles {
		if r == RoleAdmin || r == RoleMaintain {
			isOwner[login(u)] = true
		}
	}

	submitted := make([]Review, 0, len(reviews))
	for _, r := range reviews {
		if r.State != Pending && r.User != "" && !r.SubmittedAt.IsZero() {
			submitted = append(submitted, r)
		}
	}
	// GitHub lists reviews oldest first; sort anyway rather than trust it.
	slices.SortStableFunc(submitted, func(a, b Review) int {
		return cmp.Or(a.SubmittedAt.Compare(b.SubmittedAt), cmp.Compare(a.ID, b.ID))
	})
	latest := make(map[string]int, len(submitted)) // login → index of the review GitHub counts
	for i, r := range submitted {
		if r.State != Commented {
			latest[login(r.User)] = i
		}
	}

	var out []evidence.Approval
	for i, r := range submitted {
		reason, isOverride := overrideReason(r.Body)
		if (r.State != Approved && !isOverride) || !shaRE.MatchString(r.CommitID) {
			continue
		}
		a := evidence.Approval{
			Kind:        evidence.ApprovalHuman,
			Actor:       "@" + r.User,
			CommitID:    r.CommitID,
			SubmittedAt: r.SubmittedAt,
		}
		if isOverride {
			a.Kind, a.Reason = evidence.ApprovalOverride, reason
		}
		u := login(r.User)
		switch {
		case r.State != Approved:
			a.Rejection = fmt.Sprintf("review is %s, not APPROVED", r.State)
		case latest[u] != i:
			a.Rejection = RejectSuperseded
		case r.CommitID != head:
			a.Rejection = RejectStale
		case u == login(prAuthor):
			a.Rejection = RejectAuthor
		case !isOwner[u]:
			a.Rejection = RejectNotOwner
		case isOverride && reason == "":
			a.Rejection = RejectNoReason
		}
		a.Valid = a.Rejection == ""
		out = append(out, a)
	}
	slices.SortStableFunc(out, func(a, b evidence.Approval) int {
		return cmp.Or(a.SubmittedAt.Compare(b.SubmittedAt), cmp.Compare(a.Actor, b.Actor))
	})
	return out
}

// overrideReason finds the first line of body that starts with
// OverrideMarker, after leading spaces, and returns the rest of that line.
// Among several such lines the first non-empty reason wins.
func overrideReason(body string) (reason string, found bool) {
	for line := range strings.Lines(body) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), OverrideMarker)
		if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
			continue // not the marker, or a longer word such as aval:overrides
		}
		found = true
		if reason = strings.TrimSpace(rest); reason != "" {
			return reason, true
		}
	}
	return "", found
}

func login(s string) string {
	return strings.ToLower(strings.TrimPrefix(s, "@"))
}
