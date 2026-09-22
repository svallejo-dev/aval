// Package evidence defines the evidence bundle: the versioned, reproducible
// record of what was verified for a change and what the gate decided. A merge
// depends on the bundle, never on the agent's own account of its work.
package evidence

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// SchemaVersion is the bundle schema version this build writes and reads.
// Adding optional fields keeps the version; removing or changing the meaning
// of a field bumps it.
const SchemaVersion = 1

// Bundle is the evidence for one change: a base..head range of a repository.
type Bundle struct {
	SchemaVersion int          `json:"schemaVersion"`
	Repo          string       `json:"repo"`
	Base          string       `json:"base"`
	Head          string       `json:"head"`
	AvalVersion   string       `json:"avalVersion"`
	GeneratedAt   time.Time    `json:"generatedAt"`
	Mode          string       `json:"mode"`
	Tier          int          `json:"tier"`
	Changes       []string     `json:"changes"`
	Obligations   []Obligation `json:"obligations"`
	Checks        []Check      `json:"checks"`
	Scope         []Commit     `json:"scope"`
	Tamper        []Finding    `json:"tamper"`
	Override      *Override    `json:"override"`
	Verdict       Verdict      `json:"verdict"`
	// NotCollected lists evidence the gates doc describes but v0 does not
	// gather yet, e.g. "mutation", "rollback", "slo".
	NotCollected []string `json:"notCollected"`
}

// Status is the outcome of running a verifier.
type Status string

// Verifier outcomes. BuildFail at the base counts as weak fail-before evidence.
const (
	Pass      Status = "pass"
	Fail      Status = "fail"
	BuildFail Status = "build_fail"
	NotRun    Status = "not_run"
	Skipped   Status = "skipped"
	NotApply  Status = "n/a"
)

// Strength grades how well an obligation's evidence proves it.
type Strength string

// Evidence strengths.
const (
	Strong Strength = "strong" // failed at the base, passes at head
	Weak   Strength = "weak"   // did not build at the base, passes at head
	None   Strength = "none"   // missing, or passing at the base without being characterization
)

// Obligation is the evidence gathered for one obligation ID.
type Obligation struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	Source           string   `json:"source"` // spec path and requirement name
	Characterization bool     `json:"characterization"`
	Tests            []string `json:"tests"` // full test names as reported by go test -json
	Before           Status   `json:"before"`
	After            Status   `json:"after"`
	Strength         Strength `json:"strength"`
}

// Check is one verifier run: a command and its outcome.
type Check struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exitCode"`
	DurationMS int64  `json:"durationMs"`
	Status     Status `json:"status"`
	Artifact   string `json:"artifact,omitempty"`
}

// Commit is one commit of the range, classified by the paths it touches.
type Commit struct {
	SHA    string   `json:"sha"`
	Family string   `json:"family"` // dx, feat, seam, mixed or other
	Paths  []string `json:"paths"`
}

// Finding is a tampering signal: a bound test changed outside its delta, a
// skip was added, or the policy was edited in the change itself.
type Finding struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Detail string `json:"detail"`
}

// Override records an accepted aval:override label.
type Override struct {
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	LabeledAt time.Time `json:"labeledAt"`
}

// Result is the gate's decision.
type Result string

// Gate results. In observe mode a Block is reported but not enforced.
const (
	ResultPass  Result = "pass"
	ResultWarn  Result = "warn"
	ResultBlock Result = "block"
)

// Verdict is the gate's decision and every reason behind it.
type Verdict struct {
	Result  Result   `json:"result"`
	Reasons []Reason `json:"reasons"`
}

// Reason explains one contribution to the verdict.
type Reason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	ID      string `json:"id,omitempty"`
}

// ErrInvalid is wrapped by every bundle validation error.
var ErrInvalid = errors.New("invalid evidence bundle")

var sha = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Validate checks the invariants every bundle must satisfy before the gate
// trusts it.
func (b Bundle) Validate() error {
	var errs []error
	if b.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Errorf("schemaVersion: got %d, want %d", b.SchemaVersion, SchemaVersion))
	}
	if !sha.MatchString(b.Base) || !sha.MatchString(b.Head) {
		errs = append(errs, errors.New("base and head must be full 40-character commit SHAs"))
	}
	if b.Mode != "observe" && b.Mode != "enforce" {
		errs = append(errs, fmt.Errorf("mode: %q must be observe or enforce", b.Mode))
	}
	if b.Tier < 0 || b.Tier > 3 {
		errs = append(errs, fmt.Errorf("tier: %d must be between 0 and 3", b.Tier))
	}
	switch b.Verdict.Result {
	case ResultPass, ResultWarn, ResultBlock:
	default:
		errs = append(errs, fmt.Errorf("verdict.result: %q must be pass, warn or block", b.Verdict.Result))
	}
	if b.Verdict.Result != ResultPass && len(b.Verdict.Reasons) == 0 {
		errs = append(errs, errors.New("verdict: a warn or block needs at least one reason"))
	}
	if b.Override != nil && b.Override.Reason == "" {
		errs = append(errs, errors.New("override: a reason is required"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return nil
}

// CheckHead reports whether the bundle was produced for the given commit.
// The gate recomputes evidence in the same job and refuses stale bundles.
func (b Bundle) CheckHead(head string) error {
	if b.Head != head {
		return fmt.Errorf("%w: bundle is for head %s, but HEAD is %s", ErrInvalid, b.Head, head)
	}
	return nil
}
