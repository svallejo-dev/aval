// Package gate decides whether a pull request may merge: it turns the
// evidence aval gathered into a verdict explained with ADR-0005's reason codes
// (§4), and the verdict into an exit code (§6).
//
// The package is pure. It runs no git, no tests and no network call and reads
// no file, clock or environment variable: everything it knows comes in
// through Input, so the same evidence always gets the same verdict, whatever
// order it arrives in.
package gate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/baseline"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/obligation"
	"github.com/svallejo-dev/aval/internal/openspec"
	"github.com/svallejo-dev/aval/internal/scope"
)

// Reason codes (ADR-0005 §4). They are a contract: adding one is compatible,
// changing what one means needs a new ADR.
const (
	CodeSpecRule          = "spec_rule"           // new error finding of openspec.Check
	CodeOpenSpecInvalid   = "openspec_invalid"    // openspec validate failed
	CodeOpenQuestion      = "open_question"       // added or modified O obligation
	CodeUnverified        = "unverified"          // added or modified F/N/I without a bound test
	CodeFailBeforeMissing = "fail_before_missing" // added or modified F/N/I without valid fail-before
	CodeAfterNotPassing   = "after_not_passing"   // a bound test does not pass at head
	CodeRegression        = "regression"          // an unbound test fails at head, not in the baseline
	CodeBuildFailed       = "build_failed"        // a package does not build at head
	CodeTamper            = "tamper"              // tampering signal
	CodeUndeclaredRuntime = "undeclared_runtime"  // a test with an ID ran without a static declaration
	CodeMixedCommit       = "mixed_commit"        // a commit touches dx and feat paths
	CodePremortemMissing  = "premortem_missing"   // a change of tier ≥ 2 has no premortem.md
	CodePremortemUnmapped = "premortem_unmapped"  // a premortem.md without items or with an item citing no ID
	CodeApprovalMissing   = "approval_missing"    // tier 3 without a valid approval
	CodeLintNewIssues     = "lint_new_issues"     // golangci-lint reports new issues
	CodeAssumption        = "assumption"          // added or modified A obligation
	CodeSLOUnverified     = "slo_unverified"      // added or modified S obligation
	CodeSpecWarning       = "spec_warning"        // new warn finding of openspec.Check
	CodeSeamTouched       = "seam_touched"        // a feat commit touches seam paths
	CodeWeakEvidence      = "weak_evidence"       // an obligation with weak strength
	CodeNoBasePolicy      = "no_base_policy"      // the base has no root aval.yaml
)

// Reserved codes for later milestones (ADR-0005 §4). Decide never emits them.
const (
	CodeContractBreaking  = "contract_breaking"   // M4
	CodeContractLint      = "contract_lint"       // M4
	CodeChangeNotArchived = "change_not_archived" // M3
)

// premortemTier is the lowest change tier that needs a premortem.md.
const premortemTier manifest.Tier = 2

// rule is one row of the ADR-0005 §4 table.
type rule struct {
	minTier manifest.Tier // lowest pull request tier the rule applies at
	effect  evidence.Result
}

const (
	block = evidence.ResultBlock
	warn  = evidence.ResultWarn
)

// rules is the ADR-0005 §4 table. The premortem rules apply at every pull
// request tier because they judge each change by its own tier.
var rules = map[string]rule{
	CodeSpecRule:          {0, block},
	CodeOpenSpecInvalid:   {0, block},
	CodeOpenQuestion:      {1, block},
	CodeUnverified:        {1, block},
	CodeFailBeforeMissing: {1, block},
	CodeAfterNotPassing:   {0, block},
	CodeRegression:        {0, block},
	CodeBuildFailed:       {0, block},
	CodeTamper:            {0, block},
	CodeUndeclaredRuntime: {0, block},
	CodeMixedCommit:       {0, block},
	CodePremortemMissing:  {0, block},
	CodePremortemUnmapped: {0, block},
	CodeApprovalMissing:   {3, block},
	CodeLintNewIssues:     {0, block},
	CodeAssumption:        {1, warn},
	CodeSLOUnverified:     {1, warn},
	CodeSpecWarning:       {0, warn},
	CodeSeamTouched:       {0, warn},
	CodeWeakEvidence:      {1, warn},
	CodeNoBasePolicy:      {0, warn},
}

// Effect returns what a reason code does to the verdict before any override:
// evidence.ResultBlock or evidence.ResultWarn. It fails closed: a code the
// gate does not know, reserved ones included, blocks.
func Effect(code string) evidence.Result {
	if r, ok := rules[code]; ok {
		return r.effect
	}
	return block
}

// Input is everything the gate knows about a pull request. The verify and
// gate commands fill it from the other packages; Decide reads nothing else.
// Slices may come in any order.
type Input struct {
	// Policy is the root aval.yaml of the base commit, or nil when the base
	// has none: the gate then observes, tierDefault counts as 0 and no path
	// is seam.
	Policy *manifest.Repo
	// Head is the head commit. Only approvals of it count.
	Head string
	// Scope is every commit of base..head, classified (scope.Classify).
	Scope []evidence.Commit
	// Changes are the OpenSpec changes the diff touches.
	Changes []Change
	// Obligations carry each obligation's kind, delta and bound tests, and
	// the fail-before overlay's Before, After, Strength and Note.
	Obligations []evidence.Obligation
	// SpecFindings are the openspec.Check findings at head that the base
	// did not have.
	SpecFindings []openspec.Finding
	// Validation is the openspec.Validate report, or nil when the pull
	// request does not touch openspec/.
	Validation *openspec.Report
	// Tamper holds testsource.Compare's findings plus the policy_edited and
	// baseline_edited findings.
	Tamper []evidence.Finding
	// UndeclaredRuntime lists the IDs of tests that ran at head with no
	// static declaration to pair them.
	UndeclaredRuntime []string
	// BuildFailures names each package that did not build at head, or the
	// go test setup that failed.
	BuildFailures []string
	// UnboundFailures are the leaf failures at head of tests bound to no
	// obligation.
	UnboundFailures []baseline.Test
	// Baseline is .aval/baseline.json at the base commit, baseline.Empty()
	// when it has none.
	Baseline baseline.Baseline
	// Lint is golangci-lint's result with the base configuration.
	Lint Lint
	// Approvals are every PR review the gate considered, valid or not.
	Approvals []evidence.Approval
}

// Change is one OpenSpec change the pull request touches.
type Change struct {
	ID        string        // directory name, e.g. add-refunds
	Tier      manifest.Tier // tier of its manifest at head
	BaseTier  manifest.Tier // tier of its manifest at base; 0 when the base lacks it
	Premortem Premortem
}

// Premortem is what the change's premortem.md holds.
type Premortem struct {
	Present bool // premortem.md exists
	Items   int  // top-level list items outside code blocks
	// Unmapped are the items that cite no obligation ID of this change.
	Unmapped []string
}

// Lint is golangci-lint's result, run with the base commit's .golangci.yml.
type Lint struct {
	BaseConfig bool // the base commit has a .golangci.yml; without one the rule does not apply
	NewIssues  int  // issues new since the merge base
}

// Tier returns the pull request's tier (ADR-0005 §1): the maximum of
// tierDefault, when any commit is not dx, seam or other or any change is
// touched, and of each change's tier at head and at base. The head can raise
// the tier, never lower it. A commit of unknown family counts as feat.
func Tier(tierDefault manifest.Tier, scope []evidence.Commit, changes []Change) manifest.Tier {
	var t manifest.Tier
	if len(changes) > 0 || slices.ContainsFunc(scope, func(c evidence.Commit) bool {
		return c.Family != evidence.FamilyDX && c.Family != evidence.FamilySeam && c.Family != evidence.FamilyOther
	}) {
		t = tierDefault
	}
	for _, c := range changes {
		t = max(t, c.Tier, c.BaseTier)
	}
	return t
}

// Tier returns the pull request's tier under in's base policy.
func (in Input) Tier() manifest.Tier { return Tier(in.tierDefault(), in.Scope, in.Changes) }

// Mode returns the gate mode: the base policy's, or observe when the base has
// no policy.
func (in Input) Mode() manifest.Mode {
	if in.Policy == nil {
		return manifest.Observe
	}
	return in.Policy.Mode
}

func (in Input) tierDefault() manifest.Tier {
	if in.Policy == nil {
		return 0
	}
	return in.Policy.TierDefault
}

// approved reports whether a valid review of kind approved the head commit.
// Only the upstream check knows who is a CODEOWNER, so Valid is trusted, but
// a review of another commit, or an override without a reason, never counts.
func (in Input) approved(kind evidence.ApprovalKind) bool {
	return slices.ContainsFunc(in.Approvals, func(a evidence.Approval) bool {
		return a.Kind == kind && a.Valid && a.CommitID != "" && a.CommitID == in.Head &&
			(kind != evidence.ApprovalOverride || strings.TrimSpace(a.Reason) != "")
	})
}

// Decide applies every rule of ADR-0005 §4 to in. The result is block if any
// reason blocks, warn if there are only warnings and pass with no reason; a
// valid override turns a block into a warn and keeps every reason. Reasons
// are sorted by code, ID and message, so equal input gives an equal bundle.
func Decide(in Input) evidence.Verdict {
	c := collector{tier: in.Tier(), reasons: []evidence.Reason{}}
	if in.Policy == nil {
		c.addf(CodeNoBasePolicy, "", "the base commit has no root aval.yaml: the gate only observes")
	}
	c.specs(in)
	c.obligations(in.Obligations)
	c.execution(in)
	c.integrity(in)
	c.changes(in.tierDefault(), in.Changes)
	if !in.approved(evidence.ApprovalHuman) {
		c.addf(CodeApprovalMissing, "", "tier %d needs a CODEOWNER's approval of the head commit", c.tier)
	}
	if in.Lint.BaseConfig && in.Lint.NewIssues > 0 {
		c.addf(CodeLintNewIssues, "", "golangci-lint reports %d new issues", in.Lint.NewIssues)
	}

	slices.SortFunc(c.reasons, func(a, b evidence.Reason) int {
		return cmp.Or(strings.Compare(a.Code, b.Code), strings.Compare(a.ID, b.ID), strings.Compare(a.Message, b.Message))
	})
	reasons := slices.Compact(c.reasons)
	result := evidence.ResultPass
	for _, r := range reasons {
		if result = Effect(r.Code); result == block {
			break
		}
	}
	if result == block && in.approved(evidence.ApprovalOverride) {
		result = warn
	}
	return evidence.Verdict{Result: result, Reasons: reasons}
}

// ExitCode maps a verdict to the gate's exit code (ADR-0005 §6): 0 in observe
// mode or for a pass or warn, 1 for a block in enforce mode. It fails closed:
// a mode other than observe enforces. Usage and tool errors (2 and 3) belong
// to the CLI.
func ExitCode(mode manifest.Mode, v evidence.Verdict) int {
	if mode == manifest.Observe || v.Result != evidence.ResultBlock {
		return 0
	}
	return 1
}

// collector gathers the reasons that apply at the pull request's tier.
type collector struct {
	tier    manifest.Tier
	reasons []evidence.Reason
}

func (c *collector) addf(code, id, format string, args ...any) {
	if c.tier < rules[code].minTier {
		return
	}
	c.reasons = append(c.reasons, evidence.Reason{Code: code, ID: id, Message: fmt.Sprintf(format, args...)})
}

func (c *collector) specs(in Input) {
	for _, f := range in.SpecFindings {
		code := CodeSpecRule // an unknown severity blocks
		if f.Severity == openspec.SeverityWarn {
			code = CodeSpecWarning
		}
		c.addf(code, "", "%s", f)
	}
	if in.Validation == nil || in.Validation.Passed() {
		return
	}
	fs := in.Validation.Findings()
	if len(fs) == 0 {
		c.addf(CodeOpenSpecInvalid, "", "openspec validate failed")
	}
	for _, f := range fs {
		c.addf(CodeOpenSpecInvalid, "", "%s", f)
	}
}

func (c *collector) obligations(obs []evidence.Obligation) {
	for _, o := range obs {
		if len(o.Tests) > 0 && o.After != evidence.Pass {
			c.addf(CodeAfterNotPassing, o.ID, "bound tests do not pass at head (after=%s)", o.After)
		}
		if o.Strength == evidence.Weak {
			c.addf(CodeWeakEvidence, o.ID, "weak fail-before evidence (before=%s)%s", o.Before, suffix(o.Note))
		}
		if o.Delta != evidence.Added && o.Delta != evidence.Modified {
			continue
		}
		switch k := kindOf(o.Kind); {
		case k == obligation.SLO:
			c.addf(CodeSLOUnverified, o.ID, "%s SLO: v0 does not measure SLOs", o.Delta)
		case k == obligation.Assumption:
			c.addf(CodeAssumption, o.ID, "%s assumption still to validate", o.Delta)
		case k.Policy() != obligation.RequireTest: // O, and kinds aval does not know
			c.addf(CodeOpenQuestion, o.ID, "%s open question: a human must resolve it", o.Delta)
		case len(o.Tests) == 0:
			c.addf(CodeUnverified, o.ID, "%s obligation has no bound test", o.Delta)
		case o.Strength != evidence.Strong && o.Strength != evidence.Weak && o.Strength != evidence.Characterized:
			c.addf(CodeFailBeforeMissing, o.ID, "no valid fail-before (before=%s, after=%s)%s", o.Before, o.After, suffix(o.Note))
		}
	}
}

func (c *collector) execution(in Input) {
	for _, f := range in.UnboundFailures {
		if !in.Baseline.Contains(f.Package, f.Test) {
			c.addf(CodeRegression, "", "%s %s fails at head and is not in the base baseline", f.Package, f.Test)
		}
	}
	for _, p := range in.BuildFailures {
		c.addf(CodeBuildFailed, "", "build or test setup failed at head: %s", p)
	}
}

func (c *collector) integrity(in Input) {
	for _, f := range in.Tamper {
		c.addf(CodeTamper, f.ID, "%s: %s", f.Kind, f.Detail)
	}
	for _, id := range in.UndeclaredRuntime {
		c.addf(CodeUndeclaredRuntime, id, "a test bound to %s ran without a static declaration", id)
	}
	for _, cm := range in.Scope {
		switch {
		case cm.Family == evidence.FamilyMixed:
			c.addf(CodeMixedCommit, "", "commit %s touches dx and feat paths", cm.SHA)
		case cm.Family == evidence.FamilyFeat && in.Policy != nil && scope.TouchesSeam(in.Policy.Paths, cm):
			c.addf(CodeSeamTouched, "", "feat commit %s touches seam paths", cm.SHA)
		}
	}
}

// changes applies the premortem rules to each change by its own tier, which
// the head can raise but never lower below the base or tierDefault.
func (c *collector) changes(tierDefault manifest.Tier, changes []Change) {
	for _, ch := range changes {
		t := max(tierDefault, ch.Tier, ch.BaseTier)
		if t < premortemTier {
			continue
		}
		pm := ch.Premortem
		if !pm.Present {
			c.addf(CodePremortemMissing, "", "change %s (tier %d) has no premortem.md", ch.ID, t)
			continue
		}
		if pm.Items == 0 {
			c.addf(CodePremortemUnmapped, "", "premortem.md of change %s has no items", ch.ID)
		}
		for _, item := range pm.Unmapped {
			c.addf(CodePremortemUnmapped, "", "premortem.md of change %s: item cites no ID of the change: %q", ch.ID, item)
		}
	}
}

// kindOf returns the obligation kind named by s, or 0, which blocks, when s
// is not a single letter.
func kindOf(s string) obligation.Kind {
	if len(s) != 1 {
		return 0
	}
	return obligation.Kind(s[0])
}

func suffix(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}
