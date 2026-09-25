package actions

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The SHAs of the fixtures under testdata.
const (
	head1 = "febdd4dee1aef790a73c332ca77638e89c515ede"
	head2 = "178c040fb76195151aba3d9f4ab33804b03b96c8"
	baseT = "1405df66cbe219b0bf6355bc3d60361a8376b6b4"
)

// secret is the token every test hands to Detect. No error, no gap and no
// formatted Context may contain it.
const secret = "ghs_16C7e42F292c6912E7710c838347Ae178B4a" //nolint:gosec // G101: a made-up token; the point is that it never gets printed

// lookup turns a map into the environment Detect reads.
func lookup(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// runEnv is the environment of a gate run on the given event fixture.
func runEnv(fixture string) map[string]string {
	return map[string]string{
		EnvActions:     "true",
		EnvRepository:  "svallejo-dev/aval",
		EnvEventName:   "pull_request",
		EnvEventPath:   filepath.Join("testdata", fixture),
		EnvStepSummary: filepath.Join("testdata", "never-written.md"),
		EnvRunID:       "17123456789",
		EnvRunAttempt:  "2",
		EnvAPIURL:      "https://api.github.com",
		EnvServerURL:   "https://github.com",
		EnvToken:       secret,
	}
}

func gapKinds(c *Context) []GapKind {
	kinds := make([]GapKind, 0, len(c.Gaps))
	for _, g := range c.Gaps {
		kinds = append(kinds, g.Kind)
	}
	return kinds
}

// TestDetectEvents: what each payload yields, from the two events of ADR-0005
// §8 to the ones that carry no pull request at all.
func TestDetectEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture string
		event   string
		action  string
		wantPR  *PullRequest
		gaps    []GapKind
	}{
		{
			name: "pull_request opened", fixture: "pull_request_opened.json",
			event: "pull_request", action: "opened",
			wantPR: &PullRequest{Number: 42, HeadSHA: head1, BaseBranchSHA: baseT, BaseRef: "main", Author: "agente-bot"},
		},
		{
			name: "pull_request synchronize", fixture: "pull_request_synchronize.json",
			event: "pull_request", action: "synchronize",
			wantPR: &PullRequest{Number: 42, HeadSHA: head2, BaseBranchSHA: baseT, BaseRef: "main", Author: "agente-bot"},
		},
		{
			name: "pull_request_review submitted", fixture: "pull_request_review_submitted.json",
			event: "pull_request_review", action: "submitted",
			wantPR: &PullRequest{Number: 42, HeadSHA: head1, BaseBranchSHA: baseT, BaseRef: "main", Author: "agente-bot"},
		},
		{
			name: "draft pull request", fixture: "pull_request_draft.json",
			event: "pull_request", action: "opened",
			wantPR: &PullRequest{Number: 43, HeadSHA: head1, BaseBranchSHA: baseT, BaseRef: "main", Author: "agente-bot", Draft: true},
		},
		{
			name: "unicode in the author login", fixture: "pull_request_unicode_author.json",
			event: "pull_request", action: "reopened",
			wantPR: &PullRequest{Number: 44, HeadSHA: head1, BaseBranchSHA: baseT, BaseRef: "main", Author: "sebastián-ø-测试[bot]"},
		},
		{
			name: "number only at the top level, no base, no user", fixture: "pull_request_nested.json",
			event: "pull_request", action: "ready_for_review",
			wantPR: &PullRequest{Number: 45, HeadSHA: head1},
			gaps:   []GapKind{GapEventField},
		},
		{
			name: "push carries no pull request", fixture: "push.json",
			event: "push", gaps: []GapKind{GapPullRequest},
		},
		{
			name: "workflow_dispatch carries no pull request", fixture: "workflow_dispatch.json",
			event: "workflow_dispatch", gaps: []GapKind{GapPullRequest},
		},
		{
			name: "head sha that is not a sha", fixture: "pull_request_bad_sha.json",
			event: "pull_request", action: "opened", gaps: []GapKind{GapPullRequest},
		},
		{
			name: "negative pull request number", fixture: "pull_request_negative_number.json",
			event: "pull_request", action: "opened", gaps: []GapKind{GapPullRequest},
		},
		{
			// An unusable action, base sha, base ref and login cost the fields,
			// never the pull request.
			name: "rejected optional fields", fixture: "pull_request_bad_fields.json",
			event:  "pull_request",
			wantPR: &PullRequest{Number: 47, HeadSHA: head1},
			gaps:   []GapKind{GapEventField, GapEventField, GapEventField, GapEventField},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := runEnv(tt.fixture)
			env[EnvEventName] = tt.event
			c := Detect(lookup(env))

			if c.EventName != tt.event {
				t.Errorf("EventName = %q, want %q", c.EventName, tt.event)
			}
			if c.Action != tt.action {
				t.Errorf("Action = %q, want %q", c.Action, tt.action)
			}
			switch {
			case tt.wantPR == nil && c.PullRequest != nil:
				t.Errorf("PullRequest = %+v, want none", *c.PullRequest)
			case tt.wantPR != nil && c.PullRequest == nil:
				t.Errorf("PullRequest = none, want %+v", *tt.wantPR)
			case tt.wantPR != nil && *c.PullRequest != *tt.wantPR:
				t.Errorf("PullRequest = %+v, want %+v", *c.PullRequest, *tt.wantPR)
			}
			if got := gapKinds(c); !slices.Equal(got, tt.gaps) {
				t.Errorf("gaps = %v, want %v\n%v", got, tt.gaps, c.Gaps)
			}
		})
	}
}

// TestDetectEnvironment: everything Detect learns outside the payload.
func TestDetectEnvironment(t *testing.T) {
	t.Parallel()

	c := Detect(lookup(runEnv("pull_request_opened.json")))
	if !c.InActions {
		t.Error("InActions = false, want true")
	}
	if c.FullName() != "svallejo-dev/aval" || c.Owner != "svallejo-dev" || c.Repo != "aval" {
		t.Errorf("repository = %q (%q, %q)", c.FullName(), c.Owner, c.Repo)
	}
	if c.RunID != 17123456789 || c.RunAttempt != 2 {
		t.Errorf("run = %d attempt %d, want 17123456789 attempt 2", c.RunID, c.RunAttempt)
	}
	if want := filepath.Join("testdata", "never-written.md"); c.StepSummaryPath != want {
		t.Errorf("StepSummaryPath = %q, want %q", c.StepSummaryPath, want)
	}
	if c.APIURL != "https://api.github.com" || c.ServerURL != "https://github.com" {
		t.Errorf("urls = %q, %q", c.APIURL, c.ServerURL)
	}
	if got, want := c.PullRequestURL(), "https://github.com/svallejo-dev/aval/pull/42"; got != want {
		t.Errorf("PullRequestURL() = %q, want %q", got, want)
	}
	if c.Token() != secret || c.TokenSource != EnvToken || !c.HasToken {
		t.Errorf("token from %q, has = %t", c.TokenSource, c.HasToken)
	}
	if !c.ApprovalsAvailable() {
		t.Error("ApprovalsAvailable() = false, want true")
	}
	if len(c.Gaps) != 0 {
		t.Errorf("gaps = %v, want none", c.Gaps)
	}
	// One line for a log: the run, the pull request and where the token came
	// from.
	for _, want := range []string{
		"inActions:true", "repo:svallejo-dev/aval", "event:pull_request.opened",
		"pr:#42", "head:" + head1, "baseBranchTip:" + baseT, "draft:false", "token:" + EnvToken,
	} {
		if !strings.Contains(c.String(), want) {
			t.Errorf("String() = %q, want it to mention %q", c.String(), want)
		}
	}
}

// TestDetectNotInActions: a local run says so and keeps no pull request.
func TestDetectNotInActions(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "false", "0", "yes please"} {
		env := map[string]string{EnvActions: value}
		c := Detect(lookup(env))
		if c.InActions {
			t.Errorf("%s=%q: InActions = true, want false", EnvActions, value)
		}
		if !c.Missing(GapActions) {
			t.Errorf("%s=%q: gaps = %v, want %s", EnvActions, value, gapKinds(c), GapActions)
		}
		if c.PullRequest != nil || c.ApprovalsAvailable() {
			t.Errorf("%s=%q: a local run must not produce a pull request", EnvActions, value)
		}
	}
	// Detect never touches global state, so a nil lookup is an empty
	// environment, not a panic.
	c := Detect(nil)
	for _, kind := range []GapKind{GapActions, GapRepository, GapEvent, GapEventPayload, GapToken, GapStepSummary} {
		if !c.Missing(kind) {
			t.Errorf("Detect(nil): gaps = %v, want %s among them", gapKinds(c), kind)
		}
	}
}

// TestDetectPayloadProblems: an unusable event file costs the payload and the
// pull request, never a panic or an unexplained zero value.
func TestDetectPayloadProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		maxEvent int64
		want     string // substring of the gap's reason
	}{
		{name: "unset", path: "", want: EnvEventPath + " is not set"},
		{name: "missing file", path: filepath.Join("testdata", "no-such-event.json"), want: "event payload"},
		{name: "empty file", path: filepath.Join("testdata", "empty.json"), want: "is empty"},
		{name: "malformed json", path: filepath.Join("testdata", "malformed.json"), want: "decode"},
		{name: "not a regular file", path: "testdata", want: "is not a regular file"},
		{
			name: "over the limit", path: filepath.Join("testdata", "pull_request_opened.json"),
			maxEvent: 200, want: "larger than 200 bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := runEnv("pull_request_opened.json")
			env[EnvEventPath] = tt.path
			maxEvent := tt.maxEvent
			if maxEvent == 0 {
				maxEvent = MaxEventBytes
			}
			c := detect(lookup(env), maxEvent)

			if c.PullRequest != nil {
				t.Errorf("PullRequest = %+v, want none", *c.PullRequest)
			}
			if c.ApprovalsAvailable() {
				t.Error("ApprovalsAvailable() = true, want false without a pull request")
			}
			if got := gapKinds(c); !slices.Equal(got, []GapKind{GapEventPayload}) {
				t.Fatalf("gaps = %v, want [%s]: %v", got, GapEventPayload, c.Gaps)
			}
			if got := c.Gaps[0].Err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("reason = %q, want it to mention %q", got, tt.want)
			}
		})
	}
}

// TestDetectMissingFileIsInspectable: the caller can tell "no event file" from
// "unusable event file" without reading a message.
func TestDetectMissingFileIsInspectable(t *testing.T) {
	t.Parallel()

	env := runEnv("pull_request_opened.json")
	env[EnvEventPath] = filepath.Join("testdata", "no-such-event.json")
	c := detect(lookup(env), MaxEventBytes)
	if len(c.Gaps) != 1 {
		t.Fatalf("gaps = %v, want exactly one", c.Gaps)
	}
	if !errors.Is(c.Gaps[0].Err, fs.ErrNotExist) {
		t.Errorf("gap = %v, want it to wrap fs.ErrNotExist", c.Gaps[0])
	}
}

// TestDetectToken: which variable wins, and what happens when none holds
// something that can go in a header.
func TestDetectToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		env        map[string]string
		wantSource string
		wantToken  string
		wantReason string // substring of the GapToken reason; empty means no gap
	}{
		{
			name: "GITHUB_TOKEN", env: map[string]string{EnvToken: secret},
			wantSource: EnvToken, wantToken: secret,
		},
		{
			name:       "the action input wins over the ambient variables",
			env:        map[string]string{EnvInputToken: secret, EnvToken: "otro", EnvGHToken: "otro"},
			wantSource: EnvInputToken, wantToken: secret,
		},
		{
			name: "GH_TOKEN as a last resort", env: map[string]string{EnvGHToken: secret},
			wantSource: EnvGHToken, wantToken: secret,
		},
		{
			name:       "surrounding whitespace is a typo, not part of the token",
			env:        map[string]string{EnvToken: "  " + secret + "\n"},
			wantSource: EnvToken, wantToken: secret,
		},
		{
			name:       "a value that cannot go in a header is skipped",
			env:        map[string]string{EnvInputToken: "roto\ncon-salto", EnvToken: secret}, //nolint:gosec // G101: made-up values
			wantSource: EnvToken, wantToken: secret,
		},
		{
			name:       "no token at all",
			env:        map[string]string{},
			wantReason: "approvals are unavailable",
		},
		{
			name:       "only an unusable token",
			env:        map[string]string{EnvInputToken: "roto\ncon-salto"}, //nolint:gosec // G101: a made-up value
			wantReason: EnvInputToken + " holds bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := runEnv("pull_request_opened.json")
			for _, key := range []string{EnvToken, EnvInputToken, EnvGHToken} {
				delete(env, key)
			}
			for key, value := range tt.env {
				env[key] = value
			}
			c := Detect(lookup(env))

			if c.Token() != tt.wantToken || c.TokenSource != tt.wantSource {
				t.Errorf("token from %q, want from %q", c.TokenSource, tt.wantSource)
			}
			if c.HasToken != (tt.wantToken != "") {
				t.Errorf("HasToken = %t, want %t", c.HasToken, tt.wantToken != "")
			}
			if c.ApprovalsAvailable() != (tt.wantToken != "") {
				t.Errorf("ApprovalsAvailable() = %t, want %t", c.ApprovalsAvailable(), tt.wantToken != "")
			}
			if tt.wantReason == "" {
				if c.Missing(GapToken) {
					t.Errorf("gaps = %v, want no %s", c.Gaps, GapToken)
				}
				return
			}
			if !c.Missing(GapToken) {
				t.Fatalf("gaps = %v, want %s", gapKinds(c), GapToken)
			}
			var reason string
			for _, g := range c.Gaps {
				if g.Kind == GapToken {
					reason = g.Err.Error()
				}
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantReason)
			}
		})
	}
}

// TestTokenNeverPrinted: the token reaches the caller only through Token(). No
// format verb, no gap and no error may carry it.
func TestTokenNeverPrinted(t *testing.T) {
	t.Parallel()

	// An environment that goes wrong in every way at once, with the token in
	// all three variables, so every reason is built while it is in hand.
	env := map[string]string{
		EnvActions:     "true",
		EnvRepository:  "no-es-owner-barra-name",
		EnvEventName:   "pull request",
		EnvEventPath:   filepath.Join("testdata", "malformed.json"),
		EnvStepSummary: "",
		EnvToken:       secret,
		EnvInputToken:  secret + "\n" + secret,
		EnvGHToken:     secret,
	}
	c := Detect(lookup(env))
	if c.Token() != secret {
		t.Fatalf("Token() = %q, want the token", c.Token())
	}

	printed := []string{fmt.Sprint(c), fmt.Sprint(*c), c.String(), c.GoString()}
	// Every verb that reflection could reach the unexported field through, on
	// both the pointer and the value: a String method with a pointer receiver
	// would leak the value's.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		printed = append(printed, fmt.Sprintf(verb, c), fmt.Sprintf(verb, *c),
			fmt.Sprintf(verb, c.Gaps), fmt.Sprintf(verb, c.PullRequest))
	}
	for _, g := range c.Gaps {
		printed = append(printed, g.String(), g.Err.Error(), fmt.Sprintf("%v", g.Err))
	}
	// The errors of the other half of the package, built from the same context.
	for _, err := range []error{c.WriteStepSummary("resumen\n"), WriteStepSummary("", "resumen\n")} {
		printed = append(printed, fmt.Sprintf("%v", err))
	}
	for i, out := range printed {
		if strings.Contains(out, secret) {
			t.Errorf("output %d leaks the token: %s", i, out)
		}
	}
	// It does say where the token came from, which is what a summary needs.
	if !strings.Contains(c.String(), "token:"+EnvToken) {
		t.Errorf("String() = %q, want it to name the token's variable", c.String())
	}
}

// TestDetectRepository: GITHUB_REPOSITORY is owner/name and nothing else.
func TestDetectRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		owner string
		repo  string
	}{
		{value: "svallejo-dev/aval", owner: "svallejo-dev", repo: "aval"},
		{value: "svallejo-dev/aval.go", owner: "svallejo-dev", repo: "aval.go"},
		{value: ""},
		{value: "aval"},
		{value: "svallejo-dev/aval/extra"},
		{value: "svallejo-dev//aval"},
		{value: "../aval"},
		{value: "svallejo-dev/.."},
		{value: "svallejo dev/aval"},
		{value: "svallejo-dev/av\nal"},
		{value: strings.Repeat("o", 101) + "/aval"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			env := runEnv("pull_request_opened.json")
			env[EnvRepository] = tt.value
			c := Detect(lookup(env))

			if c.Owner != tt.owner || c.Repo != tt.repo {
				t.Errorf("owner, repo = %q, %q, want %q, %q", c.Owner, c.Repo, tt.owner, tt.repo)
			}
			if want := tt.owner == ""; c.Missing(GapRepository) != want {
				t.Errorf("Missing(%s) = %t, want %t: %v", GapRepository, !want, want, c.Gaps)
			}
			if c.ApprovalsAvailable() == (tt.owner == "") {
				t.Errorf("ApprovalsAvailable() = %t with repository %q", c.ApprovalsAvailable(), tt.value)
			}
		})
	}
}

// TestDetectRunAndURLs: absurd counters and URLs degrade to zero values, so
// nothing builds a request against junk.
func TestDetectRunAndURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runID       string
		attempt     string
		apiURL      string
		wantRunID   int64
		wantAttempt int
		wantAPIURL  string
	}{
		{name: "normal", runID: "42", attempt: "1", apiURL: "https://api.github.com", wantRunID: 42, wantAttempt: 1, wantAPIURL: "https://api.github.com"},
		{name: "ghes with a trailing slash", runID: "42", attempt: "1", apiURL: "https://ghe.example.com/api/v3/", wantRunID: 42, wantAttempt: 1, wantAPIURL: "https://ghe.example.com/api/v3"},
		{name: "not numbers", runID: "abc", attempt: "2.5"},
		{name: "negative", runID: "-5", attempt: "-1"},
		{name: "not a url", apiURL: "no-es-una-url"},
		{name: "another scheme", apiURL: "ftp://ghe.example.com"},
		{name: "credentials in the url", apiURL: "https://user:pass@ghe.example.com/api/v3"}, //nolint:gosec // G101: that is the case under test
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := runEnv("pull_request_opened.json")
			env[EnvRunID], env[EnvRunAttempt], env[EnvAPIURL] = tt.runID, tt.attempt, tt.apiURL
			c := Detect(lookup(env))

			if c.RunID != tt.wantRunID || c.RunAttempt != tt.wantAttempt {
				t.Errorf("run = %d attempt %d, want %d attempt %d", c.RunID, c.RunAttempt, tt.wantRunID, tt.wantAttempt)
			}
			if c.APIURL != tt.wantAPIURL {
				t.Errorf("APIURL = %q, want %q", c.APIURL, tt.wantAPIURL)
			}
		})
	}
}

// TestValidators: what each field of the payload accepts, since refusing
// absurd values is most of what this package does.
func TestValidators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(string) error
		ok    []string
		bad   []string
	}{
		{
			name: "segment", check: checkSegment,
			ok:  []string{"aval", "aval.go", "a_b-c.1", strings.Repeat("o", 100)},
			bad: []string{"", ".", "..", "av al", "av/al", "avál", strings.Repeat("o", 101)},
		},
		{
			name: "name", check: checkName,
			ok:  []string{"", "pull_request", "ready_for_review", "v2.0"},
			bad: []string{"pull request", "sinvergüenza", "with\nnewline", strings.Repeat("n", 65)},
		},
		{
			name: "sha", check: checkSHA,
			ok:  []string{head1, strings.Repeat("a", 64)},
			bad: []string{"", "deadbeef", strings.ToUpper(head1), strings.Repeat("z", 40), head1 + "0"},
		},
		{
			name: "ref", check: checkRef,
			ok:  []string{"main", "feat/actions", "release/v0.1", "rama-ñ"},
			bad: []string{"", "-mal", "con espacio", "con\ttab", "con\x7fdel", "\xff\xfe", strings.Repeat("r", 256)},
		},
		{
			name: "login", check: checkLogin,
			ok:  []string{"", "agente-bot", "dependabot[bot]", "sebastián-ø-测试"},
			bad: []string{"con\acampana", "\xff\xfe", strings.Repeat("l", 129)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, s := range tt.ok {
				if err := tt.check(s); err != nil {
					t.Errorf("%q: %v, want it accepted", s, err)
				}
			}
			for _, s := range tt.bad {
				if err := tt.check(s); err == nil {
					t.Errorf("%q was accepted, want it rejected", s)
				}
			}
		})
	}
	for _, s := range []string{"", "con espacio", "con\nsalto", "conñ"} {
		if headerSafe(s) {
			t.Errorf("headerSafe(%q) = true, want false", s)
		}
	}
	if !headerSafe(secret) {
		t.Error("headerSafe(a normal token) = false, want true")
	}
}

// TestPullRequestURLNeedsEverything: a link is only built when every half of it
// is known.
func TestPullRequestURLNeedsEverything(t *testing.T) {
	t.Parallel()

	for _, key := range []string{EnvServerURL, EnvRepository} {
		env := runEnv("pull_request_opened.json")
		env[key] = ""
		if got := Detect(lookup(env)).PullRequestURL(); got != "" {
			t.Errorf("without %s: PullRequestURL() = %q, want empty", key, got)
		}
	}
	env := runEnv("push.json")
	env[EnvEventName] = "push"
	if got := Detect(lookup(env)).PullRequestURL(); got != "" {
		t.Errorf("without a pull request: PullRequestURL() = %q, want empty", got)
	}
}

// TestBaseBranchSHAIsNotTheMergeBase: the field the ADR warns about carries
// the payload's base.sha, and the gate still has to compute the merge-base
// with git (ADR-0005 §1). This test exists so that a rename that made the two
// interchangeable would break it.
func TestBaseBranchSHAIsNotTheMergeBase(t *testing.T) {
	t.Parallel()

	c := Detect(lookup(runEnv("pull_request_opened.json")))
	if c.PullRequest == nil {
		t.Fatal("no pull request")
	}
	if c.PullRequest.BaseBranchSHA != baseT {
		t.Errorf("BaseBranchSHA = %q, want the payload's base.sha %q", c.PullRequest.BaseBranchSHA, baseT)
	}
	if c.PullRequest.BaseBranchSHA == c.PullRequest.HeadSHA {
		t.Error("the base branch tip and head cannot be the same commit in this fixture")
	}
}
