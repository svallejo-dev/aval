package approval

import (
	"reflect"
	"testing"
	"time"

	"github.com/svallejo-dev/aval/internal/evidence"
)

const (
	baseSHA  = "0000000000000000000000000000000000000000"
	headSHA  = "1111111111111111111111111111111111111111"
	olderSHA = "2222222222222222222222222222222222222222"
	prAuthor = "agent"
)

var t0 = time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

func at(minute int) time.Time { return t0.Add(time.Duration(minute) * time.Minute) }

func review(id int64, user string, s State, commit string, minute int, body string) Review {
	return Review{ID: id, User: user, State: s, CommitID: commit, SubmittedAt: at(minute), Body: body}
}

func valid(user string, minute int) evidence.Approval {
	return evidence.Approval{Kind: evidence.ApprovalHuman, Actor: "@" + user, CommitID: headSHA, SubmittedAt: at(minute), Valid: true}
}

func rejected(user, commit string, minute int, why string) evidence.Approval {
	return evidence.Approval{Kind: evidence.ApprovalHuman, Actor: "@" + user, CommitID: commit, SubmittedAt: at(minute), Rejection: why}
}

func rs(r ...Review) []Review { return r }

func as(a ...evidence.Approval) []evidence.Approval { return a }

func override(a evidence.Approval, reason string) evidence.Approval {
	a.Kind, a.Reason = evidence.ApprovalOverride, reason
	return a
}

func TestEvaluate(t *testing.T) {
	t.Parallel()

	owners := []string{"@lead", "@agent", "@ghost", "@nobody", "@gone", "@custom", "@customro"}
	access := map[string]Access{
		"lead":   {Role: "write", Permission: "write"},
		"agent":  {Role: "write", Permission: "write"},
		"ghost":  {Role: "read", Permission: "read"}, // a stale entry someone else registered
		"nobody": {Permission: "none"},
		// A custom role: only the legacy permission says whether it writes.
		"custom":   {Role: "reviewer", Permission: "write"},
		"customro": {Role: "reviewer", Permission: "read"},
		"maint":    {Role: RoleMaintain, Permission: "write"},
		"boss":     {Role: RoleAdmin, Permission: "admin"},
		"writer":   {Role: "write", Permission: "write"},
	}

	tests := []struct {
		name    string
		reviews []Review
		owners  []string // nil means owners above
		want    []evidence.Approval
	}{
		{name: "code owner approves the head", reviews: rs(review(1, "lead", Approved, headSHA, 1, "LGTM")), want: as(valid("lead", 1))},
		{name: "later changes requested voids the approval", reviews: rs(
			review(1, "lead", Approved, headSHA, 1, ""),
			review(2, "lead", ChangesRequested, headSHA, 2, "wait"),
		), want: as(rejected("lead", headSHA, 1, RejectSuperseded))},
		{name: "later dismissed review voids the approval", reviews: rs(
			review(1, "lead", Approved, headSHA, 1, ""),
			review(2, "lead", Dismissed, headSHA, 2, ""),
		), want: as(rejected("lead", headSHA, 1, RejectSuperseded))},
		{name: "approval of an earlier commit", reviews: rs(review(1, "lead", Approved, olderSHA, 1, "")), want: as(rejected("lead", olderSHA, 1, RejectStale))},
		{name: "same time: the higher ID is later", reviews: rs(
			review(2, "lead", Approved, headSHA, 1, ""),
			review(1, "lead", ChangesRequested, headSHA, 1, ""),
		), want: as(valid("lead", 1))},
		{name: "same time: the lower ID is earlier", reviews: rs(
			review(2, "lead", ChangesRequested, headSHA, 1, ""),
			review(1, "lead", Approved, headSHA, 1, ""),
		), want: as(rejected("lead", headSHA, 1, RejectSuperseded))},
		{name: "comment after the approval keeps it", reviews: rs(
			review(1, "lead", Approved, headSHA, 1, ""),
			review(2, "lead", Commented, headSHA, 2, "one more nit"),
		), want: as(valid("lead", 1))},
		{name: "re-approval after changes requested", reviews: rs(
			review(1, "lead", Approved, olderSHA, 1, ""),
			review(2, "lead", ChangesRequested, olderSHA, 2, ""),
			review(3, "lead", Approved, headSHA, 3, ""),
		), want: as(rejected("lead", olderSHA, 1, RejectSuperseded), valid("lead", 3))},
		{name: "a later approval of another commit supersedes one of the head", reviews: rs(
			review(1, "lead", Approved, headSHA, 1, ""),
			review(2, "lead", Approved, olderSHA, 2, ""),
		), want: as(rejected("lead", headSHA, 1, RejectSuperseded), rejected("lead", olderSHA, 2, RejectStale))},
		{name: "override with a reason", reviews: rs(review(1, "lead", Approved, headSHA, 1, "Checked.\r\naval:override   hotfix INC-42  \r\n")),
			want: as(override(valid("lead", 1), "hotfix INC-42"))},
		{name: "override without a reason", reviews: rs(review(1, "lead", Approved, headSHA, 1, "aval:override")),
			want: as(override(rejected("lead", headSHA, 1, RejectNoReason), ""))},
		{name: "the first override line with a reason wins", reviews: rs(review(1, "lead", Approved, headSHA, 1, "aval:override \naval:override flaky CI")),
			want: as(override(valid("lead", 1), "flaky CI"))},
		{name: "override lines in fenced code blocks do not count", reviews: rs(review(1, "lead", Approved, headSHA, 1,
			"```\naval:override a\n```\n~~~~\naval:override b\n~~~\naval:override c\n~~~~~\naval:override real\n")),
			want: as(override(valid("lead", 1), "real"))},
		{name: "an override only in a code block is a plain approval", reviews: rs(review(1, "lead", Approved, headSHA, 1, "```sh\naval:override x\n```")),
			want: as(valid("lead", 1))},
		{name: "the first of two reasons wins", reviews: rs(review(1, "lead", Approved, headSHA, 1, "aval:override first\naval:override second")),
			want: as(override(valid("lead", 1), "first"))},
		{name: "an indented override line does not count", reviews: rs(review(1, "lead", Approved, headSHA, 1, "Example:\n\n    aval:override hotfix\n")),
			want: as(valid("lead", 1))},
		{name: "a longer word is not the marker", reviews: rs(review(1, "lead", Approved, headSHA, 1, "aval:overrides nothing")), want: as(valid("lead", 1))},
		{name: "override in a comment is not an approval", reviews: rs(review(1, "lead", Commented, headSHA, 1, "aval:override hotfix")),
			want: as(override(rejected("lead", headSHA, 1, "review is COMMENTED, not APPROVED"), "hotfix"))},
		{name: "the PR author, even when listed as an owner", reviews: rs(review(1, "Agent", Approved, headSHA, 1, "")), want: as(rejected("Agent", headSHA, 1, RejectAuthor))},
		{name: "maintain role without a CODEOWNERS entry", reviews: rs(review(1, "maint", Approved, headSHA, 1, "")), want: as(valid("maint", 1))},
		{name: "admin role without a CODEOWNERS entry", reviews: rs(review(1, "boss", Approved, headSHA, 1, "")), want: as(valid("boss", 1))},
		{name: "write role is not a code owner", reviews: rs(review(1, "writer", Approved, headSHA, 1, "")), want: as(rejected("writer", headSHA, 1, RejectNotOwner))},
		{name: "CODEOWNERS entry without write access", reviews: rs(review(1, "ghost", Approved, headSHA, 1, "")), want: as(rejected("ghost", headSHA, 1, RejectNoWrite))},
		{name: "CODEOWNERS entry with a custom role and write permission", reviews: rs(review(1, "custom", Approved, headSHA, 1, "")), want: as(valid("custom", 1))},
		{name: "CODEOWNERS entry with a custom role and read permission", reviews: rs(review(1, "customro", Approved, headSHA, 1, "")),
			want: as(rejected("customro", headSHA, 1, RejectNoWrite))},
		{name: "CODEOWNERS entry with permission none", reviews: rs(review(1, "nobody", Approved, headSHA, 1, "")), want: as(rejected("nobody", headSHA, 1, RejectNoWrite))},
		{name: "CODEOWNERS entry with no access at all", reviews: rs(review(1, "gone", Approved, headSHA, 1, "")), want: as(rejected("gone", headSHA, 1, RejectNoWrite))},
		{name: "no role and no CODEOWNERS entry", reviews: rs(review(1, "stranger", Approved, headSHA, 1, "")), want: as(rejected("stranger", headSHA, 1, RejectNotOwner))},
		{name: "owners match case-insensitively", reviews: rs(review(1, "LEAD", Approved, headSHA, 1, "")), owners: []string{"@Lead"}, want: as(valid("LEAD", 1))},
		{name: "a team entry never matches", reviews: rs(review(1, "lead", Approved, headSHA, 1, "")), owners: []string{"@org/lead"},
			want: as(rejected("lead", headSHA, 1, RejectNotOwner))},
		{name: "pending, anonymous and unbound reviews are not recorded", reviews: rs(
			Review{ID: 1, User: "lead", State: Pending, CommitID: headSHA},
			review(2, "", Approved, headSHA, 2, ""),
			review(3, "boss", Approved, "", 3, ""),
		)},
		{name: "an unbound review still supersedes", reviews: rs(
			review(1, "lead", Approved, headSHA, 1, ""),
			review(2, "lead", ChangesRequested, "", 2, ""),
		), want: as(rejected("lead", headSHA, 1, RejectSuperseded))},
		{name: "input order does not matter", reviews: rs(
			review(2, "lead", ChangesRequested, headSHA, 2, ""),
			review(1, "lead", Approved, headSHA, 1, ""),
		), want: as(rejected("lead", headSHA, 1, RejectSuperseded))},
		{name: "sorted by time, then actor", reviews: rs(
			review(3, "maint", Approved, headSHA, 5, ""),
			review(2, "lead", Approved, headSHA, 5, ""),
			review(1, "boss", Approved, headSHA, 4, ""),
		), want: as(valid("boss", 4), valid("lead", 5), valid("maint", 5))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := owners
			if tt.owners != nil {
				o = tt.owners
			}
			got := Evaluate(tt.reviews, headSHA, prAuthor, o, access)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Evaluate =\n%+v\nwant\n%+v", got, tt.want)
			}
			mustValidate(t, got)
		})
	}
}

// mustValidate checks that approvals pass the bundle's own rules for head.
func mustValidate(t *testing.T, approvals []evidence.Approval) {
	t.Helper()
	b := evidence.Bundle{
		SchemaVersion: evidence.SchemaVersion,
		Repo:          "svallejo-dev/aval-sandbox",
		TrustBase:     baseSHA,
		ChangeBase:    baseSHA,
		BaseRef:       "main",
		Head:          headSHA,
		AvalVersion:   "v0.0.0-test",
		GeneratedAt:   t0,
		Mode:          "observe",
		Approvals:     approvals,
		Verdict:       evidence.Verdict{Result: evidence.ResultPass},
	}
	if err := b.Validate(); err != nil {
		t.Errorf("bundle with these approvals is invalid: %v", err)
	}
}
