// Package evidence defines the evidence bundle: the versioned, reproducible
// record of what was verified for a change and what the gate decided. A merge
// depends on the bundle, never on the agent's own account of its work.
package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/platform/strictjson"
)

// SchemaVersion is the bundle schema version this build writes and reads.
// Any change to the shape of the bundle bumps it (ADR-0004): the schema is
// closed, so a reader never accepts fields it does not understand. Version 2
// replaced the label-based override with PR reviews bound to the head commit
// (ADR-0005).
const SchemaVersion = 2

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
	Approvals     []Approval   `json:"approvals"`
	Verdict       Verdict      `json:"verdict"`
	// NotCollected lists evidence the gates doc describes but v0 does not
	// gather yet, e.g. "mutation", "rollback", "slo".
	NotCollected []string `json:"notCollected"`
}

// Status is the outcome of running a verifier.
type Status string

// Verifier outcomes.
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
	Strong        Strength = "strong"           // failed at the base, passes at head
	Weak          Strength = "weak"             // did not build at the base, passes at head
	Characterized Strength = "characterization" // declared pre-existing behavior: passes at base and head
	None          Strength = "none"             // missing, not passing at head, or passing at the base undeclared
)

// Delta says how the change touched an obligation.
type Delta string

// Delta origins. Fail-before only applies to Added and Modified.
const (
	Added     Delta = "added"
	Modified  Delta = "modified"
	Unchanged Delta = "unchanged" // checked for regressions only
)

// Obligation is the evidence gathered for one obligation ID.
type Obligation struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`   // F, N, I, S, A or O
	Source           string   `json:"source"` // spec path and requirement name
	Delta            Delta    `json:"delta"`
	Characterization bool     `json:"characterization"`
	Tests            []string `json:"tests"` // full test names as reported by go test -json
	Before           Status   `json:"before"`
	After            Status   `json:"after"`
	Strength         Strength `json:"strength"`
	Note             string   `json:"note,omitempty"` // why a status is n/a or not_run
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

// Family is the command family a commit belongs to, by the paths it touches.
type Family string

// Commit families. A commit is mixed exactly when it touches dx and feat paths
// (ADR-0005 §3b); seam and other never make it mixed. Mixed commits block.
const (
	FamilyDX    Family = "dx"
	FamilyFeat  Family = "feat"
	FamilySeam  Family = "seam"
	FamilyMixed Family = "mixed"
	FamilyOther Family = "other"
)

// Commit is one commit of the range, classified by the paths it touches.
type Commit struct {
	SHA      string   `json:"sha"`
	Family   Family   `json:"family"`
	Families []Family `json:"families,omitempty"` // [dx feat], only for mixed commits
	Paths    []string `json:"paths"`
}

// FindingKind is a kind of tampering signal.
type FindingKind string

// Tampering signals the gate looks for.
const (
	FingerprintChanged FindingKind = "fingerprint_changed" // bound test changed outside its delta
	TestRemoved        FindingKind = "test_removed"        // bound test disappeared outside its delta
	SkipAdded          FindingKind = "skip_added"          // t.Skip added to a bound test
	PolicyEdited       FindingKind = "policy_edited"       // the change edits aval.yaml, .github/**, CODEOWNERS or .golangci.yml
	BaselineEdited     FindingKind = "baseline_edited"     // the change edits .aval/baseline.json
)

// Finding is one tampering signal.
type Finding struct {
	Kind   FindingKind `json:"kind"`
	ID     string      `json:"id,omitempty"`
	Detail string      `json:"detail"`
}

// ApprovalKind says what a review grants.
type ApprovalKind string

// Approval kinds (ADR-0005 §5).
const (
	ApprovalHuman    ApprovalKind = "approval" // satisfies approval_missing, nothing else
	ApprovalOverride ApprovalKind = "override" // turns a block into a warn, keeping the reasons
)

// Approval records one PR review the gate considered, accepted or not. It is
// valid only when a CODEOWNER approved the head commit itself; an override
// also needs an "aval:override <reason>" line in the review body.
type Approval struct {
	Kind        ApprovalKind `json:"kind"`
	Actor       string       `json:"actor"`
	CommitID    string       `json:"commitId"` // the commit the review was submitted on
	SubmittedAt time.Time    `json:"submittedAt"`
	Reason      string       `json:"reason,omitempty"` // overrides only
	Valid       bool         `json:"valid"`
	Rejection   string       `json:"rejection,omitempty"` // why a review was not accepted
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

// MarshalJSON writes nil slices as empty arrays: the schema requires arrays,
// and "no findings" must never read as "unknown".
func (b Bundle) MarshalJSON() ([]byte, error) {
	type plain Bundle // drops the method set, so Marshal does not recurse
	n := plain(b)
	n.Changes = orEmpty(n.Changes)
	n.Checks = orEmpty(n.Checks)
	n.Tamper = orEmpty(n.Tamper)
	n.Approvals = orEmpty(n.Approvals)
	n.NotCollected = orEmpty(n.NotCollected)
	n.Verdict.Reasons = orEmpty(n.Verdict.Reasons)
	n.Obligations = make([]Obligation, len(b.Obligations))
	for i, o := range b.Obligations {
		o.Tests = orEmpty(o.Tests)
		n.Obligations[i] = o
	}
	n.Scope = make([]Commit, len(b.Scope))
	for i, c := range b.Scope {
		c.Paths = orEmpty(c.Paths)
		n.Scope[i] = c
	}
	data, err := json.Marshal(n)
	if err != nil {
		return nil, fmt.Errorf("marshal bundle: %w", err)
	}
	return data, nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ErrInvalid is wrapped by every bundle validation error.
var ErrInvalid = errors.New("invalid evidence bundle")

// mixedFamilies is the only valid Families value: the two families a mixed
// commit spans.
var mixedFamilies = []Family{FamilyDX, FamilyFeat}

// maxBundleBytes caps a bundle read from disk. Larger input is an error, never
// silently truncated.
const maxBundleBytes = 16 << 20

// Parse reads a bundle written by an earlier run and validates the raw bytes,
// not a re-encoding of them: unknown fields, duplicate keys, empty strings the
// writer would omit and trailing data are all errors. Only local gates reuse a
// bundle; in CI the gate always recomputes it (ADR-0005 §7).
func Parse(r io.Reader) (Bundle, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBundleBytes+1))
	if err != nil {
		return Bundle{}, fmt.Errorf("read bundle: %w", err)
	}
	if len(data) > maxBundleBytes {
		return Bundle{}, fmt.Errorf("%w: larger than %d bytes", ErrInvalid, maxBundleBytes)
	}
	if err := validateJSON(data); err != nil {
		return Bundle{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var b Bundle
	if err := strictjson.Decode(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := b.Validate(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// Validate checks the bundle against the embedded JSON Schema, then the rules
// that span several fields and that a schema cannot express. The gate never
// trusts a bundle that fails it.
func (b Bundle) Validate() error {
	var errs []error
	if err := validateSchema(b); err != nil {
		errs = append(errs, err)
	}
	if b.GeneratedAt.IsZero() {
		errs = append(errs, errors.New("generatedAt: must be set"))
	}
	if b.Verdict.Result != ResultPass && len(b.Verdict.Reasons) == 0 {
		errs = append(errs, errors.New("verdict: a warn or block needs at least one reason"))
	}
	for _, o := range b.Obligations {
		if err := o.validate(); err != nil {
			errs = append(errs, fmt.Errorf("obligation %s: %w", o.ID, err))
		}
	}
	for _, c := range b.Scope {
		if (c.Family == FamilyMixed) != slices.Equal(c.Families, mixedFamilies) {
			errs = append(errs, fmt.Errorf("commit %s: families must be [dx feat] exactly when family is mixed", c.SHA))
		}
	}
	for i, a := range b.Approvals {
		if err := a.validate(b.Head); err != nil {
			errs = append(errs, fmt.Errorf("approval %d by %s: %w", i, a.Actor, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return nil
}

// validate checks that an obligation's strength matches its statuses.
func (o Obligation) validate() error {
	switch o.Strength {
	case Strong:
		if o.Before != Fail || o.After != Pass {
			return fmt.Errorf("strong needs before=fail and after=pass, got %s→%s", o.Before, o.After)
		}
	case Weak:
		if o.Before != BuildFail || o.After != Pass {
			return fmt.Errorf("weak needs before=build_fail and after=pass, got %s→%s", o.Before, o.After)
		}
	case Characterized:
		if !o.Characterization || o.Before != Pass || o.After != Pass {
			return errors.New("characterization strength needs characterization=true, before=pass and after=pass")
		}
	case None:
		// Anything goes: none is the absence of acceptable evidence.
	}
	if o.Delta == Unchanged && (o.Strength == Strong || o.Strength == Weak) {
		return errors.New("fail-before strength only applies to added or modified obligations")
	}
	if i := strings.LastIndexByte(o.ID, '-'); i < 0 || i+1 >= len(o.ID) || o.ID[i+1:i+2] != o.Kind {
		return fmt.Errorf("kind %q does not match the ID's kind letter", o.Kind)
	}
	return nil
}

// validate checks that an approval is consistent and, when valid, bound to
// the bundle's head commit: a review of an earlier commit never counts.
func (a Approval) validate(head string) error {
	var errs []error
	if a.SubmittedAt.IsZero() {
		errs = append(errs, errors.New("submittedAt must be set"))
	}
	if a.Valid && a.Rejection != "" {
		errs = append(errs, errors.New("a valid approval has no rejection"))
	}
	if !a.Valid && a.Rejection == "" {
		errs = append(errs, errors.New("a rejected approval needs a rejection reason"))
	}
	if a.Kind == ApprovalHuman && a.Reason != "" {
		errs = append(errs, errors.New("only an override carries a reason"))
	}
	if a.Valid && a.Kind == ApprovalOverride && a.Reason == "" {
		errs = append(errs, errors.New("a valid override needs a reason"))
	}
	if a.Valid && a.CommitID != head {
		errs = append(errs, fmt.Errorf("valid only for the head commit, got %s", a.CommitID))
	}
	return errors.Join(errs...)
}

// CheckHead reports whether the bundle was produced for the given commit.
// The gate recomputes evidence in the same job and refuses stale bundles.
func (b Bundle) CheckHead(head string) error {
	if b.Head != head {
		return fmt.Errorf("%w: bundle is for head %s, but HEAD is %s", ErrInvalid, b.Head, head)
	}
	return nil
}
