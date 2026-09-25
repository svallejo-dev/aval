// Package actions tells aval which pull request it is judging when it runs
// inside GitHub Actions, and writes the gate's step summary.
//
// Detect reads the runner's environment — never the network, a subprocess or
// git — and returns a Context: the repository, the event, the pull request the
// event is about, the token the reviews are read with (ADR-0005 §5) and the
// file the step summary goes to (§7). Missing context is not an error: what
// Detect could not learn lands in Context.Gaps, typed, so the gate decides
// whether to record "approvals unavailable" and carry on or to stop.
//
// The event payload is untrusted input. GitHub writes it from its own API, but
// it arrives as a file on a runner that also executes the pull request's code,
// so Detect reads it under a size limit, decodes only the fields the gate
// needs, tolerates every other field and refuses absurd values.
package actions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Environment variables Detect reads. The runner sets them all except the
// token, which only exists if the workflow passes it in.
const (
	EnvActions     = "GITHUB_ACTIONS"
	EnvRepository  = "GITHUB_REPOSITORY"
	EnvEventName   = "GITHUB_EVENT_NAME"
	EnvEventPath   = "GITHUB_EVENT_PATH"
	EnvStepSummary = "GITHUB_STEP_SUMMARY"
	EnvRunID       = "GITHUB_RUN_ID"
	EnvRunAttempt  = "GITHUB_RUN_ATTEMPT"
	EnvAPIURL      = "GITHUB_API_URL"
	EnvServerURL   = "GITHUB_SERVER_URL"
	EnvToken       = "GITHUB_TOKEN" //nolint:gosec // G101: the name of a variable, never a credential
	EnvInputToken  = "INPUT_TOKEN"  //nolint:gosec // G101: idem
	EnvGHToken     = "GH_TOKEN"     //nolint:gosec // G101: idem
)

// MaxEventBytes caps the event payload. A payload carries the whole pull
// request body, its labels and both branch descriptions, so the limit is
// generous; a larger file is refused, never half-decoded.
const MaxEventBytes = 64 << 20

const (
	// maxPRNumber is the largest pull request number that can be real: GitHub
	// numbers issues and pull requests with a 32-bit counter.
	maxPRNumber = 1<<31 - 1
	// maxNameBytes caps an event name or a payload action.
	maxNameBytes = 64
)

// GapKind names one piece of context the gate wanted and the environment did
// not provide. The kinds are what the caller switches on; the wording of a
// Gap is for the step summary, never for a decision.
type GapKind string

// Kinds of missing context.
const (
	// GapActions means GITHUB_ACTIONS is not true: a local run. Everything
	// else in the Context is whatever the environment happened to hold.
	GapActions GapKind = "not_in_actions"
	// GapRepository means GITHUB_REPOSITORY is absent or is not owner/name.
	GapRepository GapKind = "repository"
	// GapEvent means GITHUB_EVENT_NAME is absent or unusable.
	GapEvent GapKind = "event"
	// GapEventPayload means the event file could not be read: no
	// GITHUB_EVENT_PATH, no file, not a regular file, empty, too large or
	// not JSON.
	GapEventPayload GapKind = "event_payload"
	// GapPullRequest means the payload carries no usable pull request, either
	// because the event is not about one (push, workflow_dispatch) or because
	// its number or head SHA are absurd. Context.PullRequest is then nil.
	GapPullRequest GapKind = "pull_request"
	// GapEventField means an optional field of the pull request was present
	// and rejected. The pull request is still usable without it.
	GapEventField GapKind = "event_field"
	// GapToken means no usable token: the gate cannot read reviews, so
	// approvals are unavailable (ADR-0005 §5).
	GapToken GapKind = "token"
	// GapStepSummary means GITHUB_STEP_SUMMARY is absent: there is nowhere to
	// write the summary of §7.
	GapStepSummary GapKind = "step_summary"
)

// Gap is one missing or rejected piece of context, with the reason. Err is
// always non-nil and always safe to print: no Gap ever carries the token or
// any part of it.
type Gap struct {
	Err  error
	Kind GapKind
}

func (g Gap) String() string { return string(g.Kind) + ": " + g.Err.Error() }

// PullRequest is the pull request the event is about, as its payload describes
// it.
type PullRequest struct {
	// HeadSHA is github.event.pull_request.head.sha, the commit the gate
	// judges (ADR-0005 §1). It is lowercase hex, 40 or 64 characters.
	HeadSHA string

	// BaseBranchSHA is the TIP OF THE BASE BRANCH when GitHub built the
	// event. It is NOT THE MERGE-BASE, and the gate must not use it as one:
	// ADR-0005 §1 defines base as the merge-base of the pull request with the
	// base branch, which the caller computes with git after a checkout with
	// fetch-depth: 0. The two differ whenever the base branch moved since the
	// branch was cut, which is most of the time, and using this SHA instead
	// would put every commit merged into the base branch in the diff.
	//
	// It is useful for reporting and for naming the base branch's tip in the
	// summary. Nothing else.
	BaseBranchSHA string

	// BaseRef is the name of the base branch, without refs/heads/.
	BaseRef string

	// Author is the login that opened the pull request, as the payload spells
	// it. Any valid UTF-8 without control characters is accepted: GitHub
	// logins are ASCII today, and rejecting a login would lose the field for
	// no gain. Empty when the payload reports no user.
	Author string

	// Number is the pull request number, always positive.
	Number int

	// Draft reports whether the pull request is a draft. ADR-0005 §8 runs the
	// gate on ready_for_review, so a draft can reach the gate; the policy
	// decides what that means.
	Draft bool
}

// Context is what aval knows about the run it is in. Detect builds it; the
// zero value is a local run that knows nothing.
type Context struct {
	// PullRequest is the pull request being judged, nil when the event
	// carries none. Gaps says why.
	PullRequest *PullRequest

	// Gaps lists everything Detect wanted and did not get, in the order it
	// found out. It is the explanation; the fields are what to decide on.
	Gaps []Gap

	Owner string // owner of the repository, from GITHUB_REPOSITORY
	Repo  string // name of the repository, from GITHUB_REPOSITORY

	// EventName is GITHUB_EVENT_NAME: pull_request or pull_request_review in
	// the workflow of ADR-0005 §8.
	EventName string

	// Action is the payload's "action": opened, synchronize, reopened,
	// ready_for_review or submitted. Empty when the payload has none.
	Action string

	// StepSummaryPath is the file GITHUB_STEP_SUMMARY names, where
	// WriteStepSummary appends the gate's summary (ADR-0005 §7).
	StepSummaryPath string

	// APIURL is GITHUB_API_URL, the REST API root of this GitHub, which is
	// not api.github.com on GHES. Empty when unset or unusable, and then the
	// API client falls back to its own default.
	APIURL string

	// ServerURL is GITHUB_SERVER_URL, the web root, used to link the pull
	// request from the summary. Empty when unset or unusable.
	ServerURL string

	// TokenSource names the variable the token came from, for the summary.
	// Empty when there is no token.
	TokenSource string

	// token is unexported so that no format verb can print it. Token reads it.
	token string

	RunID      int64 // GITHUB_RUN_ID, 0 when unset or absurd
	RunAttempt int   // GITHUB_RUN_ATTEMPT, 0 when unset or absurd

	InActions bool // GITHUB_ACTIONS is true
	HasToken  bool // a usable token was found
}

// Detect reads the GitHub Actions environment through getenv (os.Getenv in
// production; nil is an empty environment) and returns what the gate can learn
// from it. It never fails and never panics: everything it could not learn is
// in Gaps.
func Detect(getenv func(string) string) *Context {
	return detect(getenv, MaxEventBytes)
}

// detect is Detect with the payload limit injected, so a test can reach the
// too-large case without writing 64 MiB.
func detect(getenv func(string) string, maxEvent int64) *Context {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	// Surrounding whitespace in a workflow's env: block is a typo, never part
	// of a value.
	env := func(key string) string { return strings.TrimSpace(getenv(key)) }

	c := &Context{}
	c.InActions, _ = strconv.ParseBool(env(EnvActions))
	if !c.InActions {
		c.gap(GapActions, errors.New(EnvActions+" is not true: this is not a GitHub Actions run"))
	}

	owner, repo, err := splitRepository(env(EnvRepository))
	if err != nil {
		c.gap(GapRepository, err)
	}
	c.Owner, c.Repo = owner, repo

	if name := env(EnvEventName); name == "" {
		c.gap(GapEvent, errors.New(EnvEventName+" is not set"))
	} else if err := checkName(name); err != nil {
		c.gap(GapEvent, fmt.Errorf("%s: %w", EnvEventName, err))
	} else {
		c.EventName = name
	}

	c.APIURL = webURL(env(EnvAPIURL))
	c.ServerURL = webURL(env(EnvServerURL))
	c.RunID = max(parseInt64(env(EnvRunID)), 0)
	c.RunAttempt = max(parseInt(env(EnvRunAttempt)), 0)

	if c.StepSummaryPath = env(EnvStepSummary); c.StepSummaryPath == "" {
		c.gap(GapStepSummary, errors.New(EnvStepSummary+" is not set: nowhere to write the gate's summary"))
	}

	c.findToken(env)
	c.loadEvent(env(EnvEventPath), maxEvent)
	return c
}

// Token returns the token to authenticate with, empty when there is none. It
// is the only way to read it: the field behind it is unexported, so no format
// verb, no error and no String can leak it.
func (c Context) Token() string { return c.token }

// FullName is owner/repo as GitHub spells it, empty when either half is
// unknown.
func (c Context) FullName() string {
	if c.Owner == "" || c.Repo == "" {
		return ""
	}
	return c.Owner + "/" + c.Repo
}

// PullRequestURL is the web page of the pull request, empty when the server or
// the pull request is unknown.
func (c Context) PullRequestURL() string {
	if c.ServerURL == "" || c.FullName() == "" || c.PullRequest == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/pull/%d", c.ServerURL, c.FullName(), c.PullRequest.Number)
}

// ApprovalsAvailable reports whether the gate can read this pull request's
// reviews: ADR-0005 §5 needs the repository, the pull request number and a
// token. When it is false the gate records approvals as unavailable and
// decides on its own policy, instead of treating the environment as an error.
func (c Context) ApprovalsAvailable() bool {
	return c.FullName() != "" && c.PullRequest != nil && c.HasToken
}

// Missing reports whether a gap of this kind was recorded.
func (c Context) Missing(kind GapKind) bool {
	for _, g := range c.Gaps {
		if g.Kind == kind {
			return true
		}
	}
	return false
}

// String describes the run in one line for a log. It names where the token
// came from and never the token itself.
func (c Context) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "actions.Context{inActions:%t repo:%s event:%s", c.InActions,
		cmp.Or(c.FullName(), "-"), cmp.Or(c.EventName, "-"))
	if c.Action != "" {
		fmt.Fprintf(&b, ".%s", c.Action)
	}
	if pr := c.PullRequest; pr != nil {
		fmt.Fprintf(&b, " pr:#%d head:%s baseBranchTip:%s baseRef:%s author:%s draft:%t",
			pr.Number, cmp.Or(pr.HeadSHA, "-"), cmp.Or(pr.BaseBranchSHA, "-"),
			cmp.Or(pr.BaseRef, "-"), cmp.Or(pr.Author, "-"), pr.Draft)
	}
	fmt.Fprintf(&b, " token:%s", cmp.Or(c.TokenSource, "none"))
	if len(c.Gaps) > 0 {
		kinds := make([]string, 0, len(c.Gaps))
		for _, g := range c.Gaps {
			kinds = append(kinds, string(g.Kind))
		}
		fmt.Fprintf(&b, " gaps:[%s]", strings.Join(kinds, " "))
	}
	b.WriteByte('}')
	return b.String()
}

// GoString keeps %#v from reaching the token through reflection.
func (c Context) GoString() string { return c.String() }

// gap records one missing piece of context.
func (c *Context) gap(kind GapKind, err error) {
	c.Gaps = append(c.Gaps, Gap{Kind: kind, Err: err})
}

// findToken takes the first usable token of INPUT_TOKEN, GITHUB_TOKEN and
// GH_TOKEN, in that order: the action's own input is the most explicit, and a
// workflow that sets GH_TOKEN for the gh CLI may not mean it for aval.
//
// A value that cannot go in an HTTP header is skipped, and the reason never
// quotes it.
func (c *Context) findToken(env func(string) string) {
	var rejected []string
	for _, key := range []string{EnvInputToken, EnvToken, EnvGHToken} {
		value := env(key)
		switch {
		case value == "":
		case !headerSafe(value):
			rejected = append(rejected, key+" holds bytes that cannot go in an HTTP header")
		default:
			c.token, c.TokenSource, c.HasToken = value, key, true
			return
		}
	}
	reason := "no token in " + EnvInputToken + ", " + EnvToken + " or " + EnvGHToken +
		": the gate cannot read reviews, so approvals are unavailable"
	if len(rejected) > 0 {
		reason += " (" + strings.Join(rejected, "; ") + ")"
	}
	c.gap(GapToken, errors.New(reason))
}

// eventPayload is the part of a webhook payload the gate needs. Every other
// field is ignored, so a payload that grows stays readable.
type eventPayload struct {
	Action string `json:"action"`
	// Number is the top-level number a pull_request event also carries; it
	// backs up pull_request.number.
	Number      int64 `json:"number"`
	PullRequest *struct {
		Number int64 `json:"number"`
		Draft  bool  `json:"draft"`
		Head   *struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base *struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"base"`
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
}

// loadEvent reads the event file and takes from it the action and the pull
// request. Only a regular file counts, so a FIFO or a device cannot block the
// gate, and the size is checked twice: on the inode, and again while reading,
// in case the file grows in between.
func (c *Context) loadEvent(path string, maxBytes int64) {
	if path == "" {
		c.gap(GapEventPayload, errors.New(EnvEventPath+" is not set"))
		return
	}
	fi, err := os.Lstat(path)
	if err != nil {
		c.gap(GapEventPayload, fmt.Errorf("event payload: %w", err))
		return
	}
	if !fi.Mode().IsRegular() {
		c.gap(GapEventPayload, fmt.Errorf("event payload %s is not a regular file", path))
		return
	}
	if fi.Size() > maxBytes {
		c.gap(GapEventPayload, fmt.Errorf("event payload %s is larger than %d bytes", path, maxBytes))
		return
	}
	f, err := os.Open(path) //nolint:gosec // the runner names the event file; only the declared fields are read
	if err != nil {
		c.gap(GapEventPayload, fmt.Errorf("event payload: %w", err))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		c.gap(GapEventPayload, fmt.Errorf("event payload %s: read: %w", path, err))
		return
	}
	if int64(len(data)) > maxBytes {
		c.gap(GapEventPayload, fmt.Errorf("event payload %s is larger than %d bytes", path, maxBytes))
		return
	}
	if len(bytes.TrimSpace(data)) == 0 {
		c.gap(GapEventPayload, fmt.Errorf("event payload %s is empty", path))
		return
	}
	var p eventPayload
	if err := json.Unmarshal(data, &p); err != nil {
		c.gap(GapEventPayload, fmt.Errorf("event payload %s: decode: %w", path, err))
		return
	}
	if p.Action != "" {
		if err := checkName(p.Action); err != nil {
			c.gap(GapEventField, fmt.Errorf("action: %w", err))
		} else {
			c.Action = p.Action
		}
	}
	c.setPullRequest(p)
}

// setPullRequest takes the pull request from the payload. The number and the
// head SHA are what the gate cannot work without (ADR-0005 §1 and §5): if
// either is absurd there is no pull request at all. Every other field is
// best-effort, and a rejected one only costs a GapEventField.
func (c *Context) setPullRequest(p eventPayload) {
	pr := p.PullRequest
	if pr == nil {
		c.gap(GapPullRequest, fmt.Errorf("the %s payload carries no pull_request", cmp.Or(c.EventName, "event")))
		return
	}
	number := cmp.Or(pr.Number, p.Number)
	if number <= 0 || number > maxPRNumber {
		c.gap(GapPullRequest, fmt.Errorf("pull request number %d is not a real number", number))
		return
	}
	var head string
	if pr.Head != nil {
		head = pr.Head.SHA
	}
	if err := checkSHA(head); err != nil {
		c.gap(GapPullRequest, fmt.Errorf("pull_request.head.sha: %w", err))
		return
	}
	out := &PullRequest{Number: int(number), HeadSHA: head, Draft: pr.Draft}
	if pr.Base == nil {
		c.gap(GapEventField, errors.New("pull_request.base is absent: no base branch tip or name"))
	} else {
		if err := checkSHA(pr.Base.SHA); err != nil {
			c.gap(GapEventField, fmt.Errorf("pull_request.base.sha: %w", err))
		} else {
			out.BaseBranchSHA = pr.Base.SHA
		}
		if err := checkRef(pr.Base.Ref); err != nil {
			c.gap(GapEventField, fmt.Errorf("pull_request.base.ref: %w", err))
		} else {
			out.BaseRef = pr.Base.Ref
		}
	}
	if pr.User != nil && pr.User.Login != "" {
		if err := checkLogin(pr.User.Login); err != nil {
			c.gap(GapEventField, fmt.Errorf("pull_request.user.login: %w", err))
		} else {
			out.Author = pr.User.Login
		}
	}
	c.PullRequest = out
}

// splitRepository parses owner/name as GITHUB_REPOSITORY holds it. Both halves
// go into API paths, so neither may look like a path segment of its own.
func splitRepository(s string) (owner, name string, err error) {
	if s == "" {
		return "", "", errors.New(EnvRepository + " is not set")
	}
	o, n, ok := strings.Cut(s, "/")
	if !ok {
		return "", "", fmt.Errorf("%s=%q is not owner/name", EnvRepository, s)
	}
	if err := checkSegment(o); err != nil {
		return "", "", fmt.Errorf("%s: owner: %w", EnvRepository, err)
	}
	if err := checkSegment(n); err != nil {
		return "", "", fmt.Errorf("%s: repository: %w", EnvRepository, err)
	}
	return o, n, nil
}

// checkSegment accepts what GitHub allows in an owner or repository name.
func checkSegment(s string) error {
	if s == "" {
		return errors.New("is empty")
	}
	if len(s) > 100 {
		return fmt.Errorf("is longer than 100 bytes (%d)", len(s))
	}
	if s == "." || s == ".." {
		return fmt.Errorf("%q is not a name", s)
	}
	return checkNameRunes(s)
}

// checkName accepts an event name or a payload action: short, printable ASCII
// without spaces.
func checkName(s string) error {
	if len(s) > maxNameBytes {
		return fmt.Errorf("%q is longer than %d bytes", s, maxNameBytes)
	}
	return checkNameRunes(s)
}

// checkNameRunes accepts ASCII letters, digits, dash, underscore and dot: what
// a GitHub name, event name or action is spelled with.
func checkNameRunes(s string) error {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%q has a character that cannot be in a name: %q", s, r)
		}
	}
	return nil
}

// checkSHA accepts a commit SHA as git and the GitHub API write one: lowercase
// hex, 40 characters in a SHA-1 repository and 64 in a SHA-256 one. Case
// matters because the gate compares SHAs by string equality with git's output
// (ADR-0005 §5).
func checkSHA(s string) error {
	if s == "" {
		return errors.New("is empty")
	}
	if len(s) != 40 && len(s) != 64 {
		return fmt.Errorf("%q is %d characters, not 40 or 64", s, len(s))
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return fmt.Errorf("%q is not lowercase hex", s)
		}
	}
	return nil
}

// checkRef accepts a branch name: valid UTF-8, no control character, no space
// and nothing that could pass for a command-line flag.
func checkRef(s string) error {
	if s == "" {
		return errors.New("is empty")
	}
	if len(s) > 255 {
		return fmt.Errorf("is longer than 255 bytes (%d)", len(s))
	}
	if !utf8.ValidString(s) {
		return errors.New("is not valid UTF-8")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%q starts with a dash", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return fmt.Errorf("has a character that cannot be in a ref: %q", r)
		}
	}
	return nil
}

// checkLogin accepts an author login. GitHub logins are ASCII letters, digits
// and hyphens, plus the [bot] suffix of an app, but the login is only ever
// reported and passed back to the API, so anything printable is kept: losing
// the author would hurt more than a strange one.
func checkLogin(s string) error {
	if len(s) > 128 {
		return fmt.Errorf("is longer than 128 bytes (%d)", len(s))
	}
	if !utf8.ValidString(s) {
		return errors.New("is not valid UTF-8")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("has a control character: %q", r)
		}
	}
	return nil
}

// headerSafe reports whether every byte of s can go in an HTTP header value,
// which is what a token is used for. It never returns s or part of it.
func headerSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// webURL accepts an absolute http or https URL without credentials, and
// returns it without a trailing slash. Anything else is empty, so the caller
// falls back to its own default instead of building a request against junk.
func webURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimSuffix(u.String(), "/")
}

// parseInt64 reads a decimal identifier, 0 when it is not one.
func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseInt reads a decimal counter, 0 when it is not one or does not fit.
func parseInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
