package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/actions"
	"github.com/svallejo-dev/aval/internal/approval"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/platform/git"
)

// event writes a pull_request event payload naming head and baseSHA, and
// returns its path.
func event(t *testing.T, head, baseSHA, baseRef, author string) string {
	t.Helper()
	pr := map[string]any{
		"number": 7,
		"head":   map[string]any{"sha": head},
		"base":   map[string]any{"sha": baseSHA, "ref": baseRef},
		"user":   map[string]any{"login": author},
	}
	data, err := json.Marshal(map[string]any{"action": "synchronize", "number": 7, "pull_request": pr})
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

// env returns a getenv over the given variables, so no test has to mutate the
// process environment to describe a GitHub Actions run.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// actionsEnv is a runner in the middle of a pull_request event, with overrides
// applied last so a case can drop the token or break a variable.
func actionsEnv(t *testing.T, eventPath string, over map[string]string) map[string]string {
	t.Helper()
	vars := map[string]string{
		actions.EnvActions:    "true",
		actions.EnvRepository: "svallejo-dev/shop",
		actions.EnvEventName:  actions.EventPullRequest,
		actions.EnvEventPath:  eventPath,
		actions.EnvToken:      "ghs_notatoken",
		actions.EnvAPIURL:     "https://api.github.com",
	}
	for k, v := range over {
		if v == "" {
			delete(vars, k)
			continue
		}
		vars[k] = v
	}
	return vars
}

// TestRunGateRefusals covers every invocation aval gate refuses before it
// gathers any evidence: ADR-0005 §8's events, a payload with nothing to judge,
// and a checkout that is not the commit the event is about.
func TestRunGateRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// vars builds the environment; base and head are the fixture's commits.
		vars    func(t *testing.T, base, head string) map[string]string
		wantErr string
	}{
		{
			name: "an event ADR-0005 §8 does not allow",
			vars: func(t *testing.T, base, head string) map[string]string {
				return actionsEnv(t, event(t, head, base, "main", "agent"),
					map[string]string{actions.EnvEventName: "pull_request_target"})
			},
			wantErr: `not on "pull_request_target"`,
		},
		{
			name: "no event name at all",
			vars: func(t *testing.T, base, head string) map[string]string {
				return actionsEnv(t, event(t, head, base, "main", "agent"),
					map[string]string{actions.EnvEventName: ""})
			},
			wantErr: "not on no event",
		},
		{
			name: "a payload with no pull request",
			vars: func(t *testing.T, _, _ string) map[string]string {
				name := filepath.Join(t.TempDir(), "event.json")
				if err := os.WriteFile(name, []byte(`{"action":"synchronize"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return actionsEnv(t, name, nil)
			},
			wantErr: "carries no usable pull request",
		},
		{
			name: "a payload that names no base branch",
			vars: func(t *testing.T, _, head string) map[string]string {
				return actionsEnv(t, event(t, head, "", "", "agent"), nil)
			},
			wantErr: "the event names no base branch",
		},
		{
			name: "the checkout is not the commit the event is about",
			vars: func(t *testing.T, base, _ string) map[string]string {
				// The event's head is the base commit, so it differs from HEAD:
				// what actions/checkout does by default, with the merge commit.
				return actionsEnv(t, event(t, base, base, "main", "agent"), nil)
			},
			wantErr: "must check out github.event.pull_request.head.sha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newGitRepo(t)
			base, _ := r.history()
			head := r.git("rev-parse", "HEAD")
			cmd, _, stderr := testCmd()
			err := runGate(t.Context(), cmd, &globalFlags{plain: true}, rangeFlags{dir: r.dir},
				gateOptions{getenv: env(tt.vars(t, base, head))})
			assertExitCode(t, err, ExitUsage, tt.wantErr)
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want it empty: the caller reports the error", stderr)
			}
		})
	}
}

// TestEventBaseRefs checks that the base comes from the merge base with the
// base branch's tip, never from the tip itself (ADR-0005 §1).
func TestEventBaseRefs(t *testing.T) {
	t.Parallel()
	tip := strings.Repeat("c", 40)
	got, err := eventBaseRefs(&actions.PullRequest{BaseBranchSHA: tip, BaseRef: "release/1.x"})
	if err != nil {
		t.Fatalf("eventBaseRefs: %v", err)
	}
	want := []string{tip, "refs/remotes/origin/release/1.x", "release/1.x"}
	if !slices.Equal(got, want) {
		t.Errorf("eventBaseRefs = %q, want %q", got, want)
	}
	if _, err := eventBaseRefs(&actions.PullRequest{}); err == nil {
		t.Error("eventBaseRefs without a base = nil error, want a usage error")
	}
}

// TestGateUsesTheMergeBase checks the whole resolution inside Actions: the base
// branch moved on after the pull request was cut, and the range still starts at
// the merge base, not at the branch's tip.
func TestGateUsesTheMergeBase(t *testing.T) {
	t.Parallel()
	r := newGitRepo(t)
	mergeBaseSHA, head := r.history()
	r.git("checkout", "--quiet", "main")
	tip := r.commit("docs: main moved on", map[string]string{"d.txt": "d\n"})
	r.git("checkout", "--quiet", "work")

	_, g, err := openRepo(t.Context(), r.dir)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	refs, err := eventBaseRefs(&actions.PullRequest{BaseBranchSHA: tip, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	base, gotHead, err := resolveRange(t.Context(), g, rangeFlags{}, refs)
	if err != nil {
		t.Fatalf("resolveRange: %v", err)
	}
	if base != mergeBaseSHA {
		t.Errorf("base = %s, want the merge base %s (the branch tip is %s)", base, mergeBaseSHA, tip)
	}
	if gotHead != head {
		t.Errorf("head = %s, want %s", gotHead, head)
	}
}

func TestBaseOwners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{name: "no CODEOWNERS at all"},
		{
			name:  "the root file, with the last matching line winning",
			files: map[string]string{"CODEOWNERS": "* @alice\n/aval.yaml @bob @carol\n"},
			want:  []string{"@bob", "@carol"},
		},
		{
			name: ".github wins: GitHub reads the first location that exists and no other",
			files: map[string]string{
				".github/CODEOWNERS": "* @alice\n",
				"CODEOWNERS":         "* @bob\n",
				"docs/CODEOWNERS":    "* @carol\n",
			},
			want: []string{"@alice"},
		},
		{
			name:  "teams and emails are not individual owners",
			files: map[string]string{"docs/CODEOWNERS": "* @org/platform owner@example.com\n"},
		},
		{
			name:  "a directory where a CODEOWNERS would be is not one",
			files: map[string]string{"CODEOWNERS/README.md": "not a codeowners file\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newGitRepo(t)
			files := map[string]string{"aval.yaml": "version: 1\n"}
			for k, v := range tt.files {
				files[k] = v
			}
			base := r.commit("chore: base", files)
			// The head deletes every CODEOWNERS: the owners must still come out
			// of the base commit's objects, never out of the working tree.
			for name := range tt.files {
				if err := os.RemoveAll(filepath.Join(r.dir, filepath.FromSlash(name))); err != nil {
					t.Fatal(err)
				}
			}
			r.commit("feat: drop CODEOWNERS", map[string]string{"b.txt": "b\n"})

			_, g, err := openRepo(t.Context(), r.dir)
			if err != nil {
				t.Fatalf("openRepo: %v", err)
			}
			got, err := baseOwners(t.Context(), g, base)
			if err != nil {
				t.Fatalf("baseOwners: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("baseOwners = %q, want %q", got, tt.want)
			}
		})
	}
}

// review is one review of the fake GitHub API, in the REST shape.
type review struct {
	ID          int64     `json:"id"`
	User        user      `json:"user"`
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	Body        string    `json:"body"`
}

type user struct {
	Login string `json:"login"`
}

// fakeGitHub answers the two endpoints the gate reads, and counts the calls so
// a test can check that a reviewer's access is asked for once.
type fakeGitHub struct {
	reviews []review
	access  map[string]map[string]string // login → {role_name, permission}
	status  int                          // when not 0, every call fails with it
	calls   int
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"message":"the API is having a bad minute"}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ghs_notatoken" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/svallejo-dev/shop/pulls/7/reviews":
			_ = json.NewEncoder(w).Encode(f.reviews)
		case strings.HasPrefix(r.URL.Path, "/repos/svallejo-dev/shop/collaborators/"):
			login := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/svallejo-dev/shop/collaborators/"), "/permission")
			a, ok := f.access[login]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(a)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestReviewApprovals(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	other := strings.Repeat("9", 40)

	tests := []struct {
		name string
		api  fakeGitHub
		// noToken drops the token, so the API is never called.
		noToken     bool
		wantSkipped string // substring; "" means approvals were judged
		want        []evidence.Approval
	}{
		{
			name: "a code owner's approval of the head commit is valid",
			api: fakeGitHub{
				reviews: []review{{ID: 1, User: user{"alice"}, State: "APPROVED", SubmittedAt: at}},
				access:  map[string]map[string]string{"alice": {"role_name": "write", "permission": "write"}},
			},
			want: []evidence.Approval{{Kind: evidence.ApprovalHuman, Actor: "@alice", SubmittedAt: at, Valid: true}},
		},
		{
			name: "an override by a maintainer is valid, reason and all",
			api: fakeGitHub{
				reviews: []review{{ID: 1, User: user{"mallory"}, State: "APPROVED", SubmittedAt: at, Body: "aval:override the regression is a known flake\n"}},
				access:  map[string]map[string]string{"mallory": {"role_name": "maintain", "permission": "write"}},
			},
			want: []evidence.Approval{{
				Kind: evidence.ApprovalOverride, Actor: "@mallory", SubmittedAt: at,
				Reason: "the regression is a known flake", Valid: true,
			}},
		},
		{
			name: "an approval of an earlier commit is recorded and rejected",
			api: fakeGitHub{
				reviews: []review{{ID: 1, User: user{"alice"}, State: "APPROVED", CommitID: other, SubmittedAt: at}},
				access:  map[string]map[string]string{"alice": {"role_name": "write", "permission": "write"}},
			},
			want: []evidence.Approval{{
				Kind: evidence.ApprovalHuman, Actor: "@alice", CommitID: other, SubmittedAt: at,
				Rejection: approval.RejectStale,
			}},
		},
		{
			name: "someone who is not a code owner is recorded and rejected",
			api: fakeGitHub{
				reviews: []review{{ID: 1, User: user{"dave"}, State: "APPROVED", SubmittedAt: at}},
				access:  map[string]map[string]string{"dave": {"role_name": "read", "permission": "read"}},
			},
			want: []evidence.Approval{{
				Kind: evidence.ApprovalHuman, Actor: "@dave", SubmittedAt: at,
				Rejection: approval.RejectNotOwner,
			}},
		},
		{
			name: "a code owner without write access is recorded and rejected",
			api: fakeGitHub{
				reviews: []review{{ID: 1, User: user{"bob"}, State: "APPROVED", SubmittedAt: at}},
				access:  map[string]map[string]string{"bob": {"role_name": "triage", "permission": "read"}},
			},
			want: []evidence.Approval{{
				Kind: evidence.ApprovalHuman, Actor: "@bob", SubmittedAt: at,
				Rejection: approval.RejectNoWrite,
			}},
		},
		{
			name:        "without a token the gate degrades and says which variable to set",
			noToken:     true,
			wantSkipped: actions.EnvToken,
		},
		{
			name:        "an API that fails never stops the gate",
			api:         fakeGitHub{status: http.StatusInternalServerError},
			wantSkipped: "500",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newGitRepo(t)
			base := r.commit("chore: base", map[string]string{
				"aval.yaml":          "version: 1\n",
				".github/CODEOWNERS": "* @nobody\n/aval.yaml @alice @bob\n",
			})
			head := r.commit("feat: work", map[string]string{"b.txt": "b\n"})
			for i := range tt.api.reviews {
				if tt.api.reviews[i].CommitID == "" {
					tt.api.reviews[i].CommitID = head
				}
			}

			srv := httptest.NewServer(tt.api.handler(t))
			t.Cleanup(srv.Close)
			hc := &http.Client{Timeout: 10 * time.Second}
			t.Cleanup(hc.CloseIdleConnections)

			over := map[string]string{actions.EnvAPIURL: srv.URL}
			if tt.noToken {
				over[actions.EnvToken] = ""
			}
			ac := actions.Detect(env(actionsEnv(t, event(t, head, base, "main", "agent"), over)))
			_, g, err := openRepo(t.Context(), r.dir)
			if err != nil {
				t.Fatalf("openRepo: %v", err)
			}

			got := reviewApprovals(t.Context(), ac, g, base, head, hc)
			if tt.wantSkipped != "" {
				if !strings.Contains(got.skipped, tt.wantSkipped) {
					t.Errorf("skipped = %q, want it to mention %q", got.skipped, tt.wantSkipped)
				}
				if len(got.approvals) != 0 {
					t.Errorf("approvals = %+v, want none when the reviews were not read", got.approvals)
				}
				return
			}
			if got.skipped != "" {
				t.Fatalf("skipped = %q, want the reviews to have been judged", got.skipped)
			}
			assertApprovals(t, got.approvals, tt.want)
			// Each approval must pass the bundle's own validation for this head,
			// or Save would refuse the bundle the gate just built.
			b := evidence.Bundle{Head: head, Approvals: got.approvals}
			for i, a := range b.Approvals {
				if a.Valid && a.CommitID != head {
					t.Errorf("approval %d is valid for %s, want the head commit %s", i, a.CommitID, head)
				}
			}
		})
	}
}

// TestReviewApprovalsAsksEachReviewerOnce checks that the gate resolves one
// permission per reviewer, whatever case GitHub spells the login in, and that a
// review without a user asks for nothing.
func TestReviewApprovalsAsksEachReviewerOnce(t *testing.T) {
	t.Parallel()
	r := newGitRepo(t)
	base := r.commit("chore: base", map[string]string{
		"aval.yaml":  "version: 1\n",
		"CODEOWNERS": "/aval.yaml @Alice\n",
	})
	head := r.commit("feat: work", map[string]string{"b.txt": "b\n"})
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	api := fakeGitHub{
		reviews: []review{
			{ID: 1, User: user{"alice"}, State: "COMMENTED", CommitID: head, SubmittedAt: at},
			{ID: 2, User: user{"ALICE"}, State: "APPROVED", CommitID: head, SubmittedAt: at.Add(time.Minute)},
			{ID: 3, State: "APPROVED", CommitID: head, SubmittedAt: at.Add(2 * time.Minute)},
		},
		access: map[string]map[string]string{"alice": {"role_name": "write", "permission": "write"}, "ALICE": {"role_name": "write", "permission": "write"}},
	}
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)
	hc := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(hc.CloseIdleConnections)

	ac := actions.Detect(env(actionsEnv(t, event(t, head, base, "main", "agent"),
		map[string]string{actions.EnvAPIURL: srv.URL})))
	_, g, err := openRepo(t.Context(), r.dir)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	got := reviewApprovals(t.Context(), ac, g, base, head, hc)
	assertApprovals(t, got.approvals, []evidence.Approval{
		{Kind: evidence.ApprovalHuman, Actor: "@ALICE", CommitID: head, SubmittedAt: at.Add(time.Minute), Valid: true},
	})
	// One call for the reviews and one for alice: the second spelling of the
	// same login must not cost another round trip.
	if api.calls != 2 {
		t.Errorf("the gate made %d API calls, want 2 (reviews, then alice's permission)", api.calls)
	}
}

// TestApprovalsSkippedNamesNoToken checks that the reasons the gate degrades for
// name the variable a token comes from and never a token.
func TestApprovalsSkippedNamesNoToken(t *testing.T) {
	t.Parallel()
	for name, vars := range map[string]map[string]string{
		"outside Actions":   {},
		"no pull request":   {actions.EnvActions: "true"},
		"no repository":     {actions.EnvActions: "true", actions.EnvRepository: "nope"},
		"no usable token":   {actions.EnvActions: "true", actions.EnvRepository: "o/r", actions.EnvToken: " "},
		"a token that is a": {actions.EnvActions: "true", actions.EnvToken: "ghs_secret"},
	} {
		got := approvalsSkipped(actions.Detect(env(vars)))
		if got == "" {
			t.Errorf("%s: approvalsSkipped = %q, want a reason", name, got)
		}
		if strings.Contains(got, "ghs_secret") {
			t.Errorf("%s: approvalsSkipped = %q, want it never to carry the token", name, got)
		}
	}
}

func TestGapList(t *testing.T) {
	t.Parallel()
	got := gapList(actions.Detect(env(nil)))
	if !strings.Contains(got, string(actions.GapActions)) {
		t.Errorf("gapList = %q, want it to name the gaps by kind", got)
	}
	if got := gapList(&actions.Context{}); got != "no reason recorded" {
		t.Errorf("gapList without gaps = %q, want a fallback", got)
	}
}

// TestBaseOwnersRejectsAnUnreadableBase checks that a base commit aval cannot
// read is an error and not silently "no owners", which would turn every code
// owner's approval into a rejection.
func TestBaseOwnersRejectsAnUnreadableBase(t *testing.T) {
	t.Parallel()
	r := newGitRepo(t)
	r.commit("chore: base", map[string]string{"aval.yaml": "version: 1\n"})
	_, g, err := openRepo(t.Context(), r.dir)
	if err != nil {
		t.Fatalf("openRepo: %v", err)
	}
	if _, err := baseOwners(t.Context(), git.New(filepath.Join(r.dir, "nope")), strings.Repeat("a", 40)); err == nil {
		t.Error("baseOwners outside a repository = nil error, want a failure")
	}
	// A commit that is not there is not an error: cat-file reports it missing,
	// and a repository with no CODEOWNERS has no owners either way.
	owners, err := baseOwners(t.Context(), g, strings.Repeat("a", 40))
	if err != nil || owners != nil {
		t.Errorf("baseOwners of an unknown commit = %q, %v; want no owners and no error", owners, err)
	}
}

// assertApprovals compares approvals field by field, so a change of shape shows
// up as a difference and not as a passing test.
func assertApprovals(t *testing.T, got, want []evidence.Approval) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("approvals = %+v, want %+v", got, want)
	}
	for i, w := range want {
		g := got[i]
		if g.Kind != w.Kind || g.Actor != w.Actor || g.Valid != w.Valid ||
			g.Reason != w.Reason || g.Rejection != w.Rejection || !g.SubmittedAt.Equal(w.SubmittedAt) {
			t.Errorf("approval %d = %+v, want %+v", i, g, w)
		}
		if w.CommitID != "" && g.CommitID != w.CommitID {
			t.Errorf("approval %d commit = %s, want %s", i, g.CommitID, w.CommitID)
		}
	}
}

// testCmd returns a cobra command whose streams a test can read, for the
// commands' own warnings.
func testCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	cmd := &cobra.Command{Use: "gate"}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}
