package gate

import (
	"cmp"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/svallejo-dev/aval/internal/baseline"
	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/manifest"
	"github.com/svallejo-dev/aval/internal/openspec"
)

const (
	head  = "2222222222222222222222222222222222222222"
	other = "3333333333333333333333333333333333333333"
)

var (
	approval = evidence.Approval{Kind: evidence.ApprovalHuman, Actor: "ana", CommitID: head, Valid: true}
	override = evidence.Approval{Kind: evidence.ApprovalOverride, Actor: "ana", CommitID: head, Reason: "hotfix", Valid: true}
	unbound  = baseline.Test{Package: "example.com/shop/refund", Test: "TestLegacy"}
)

// clean returns an input that passes at tier: an enforce policy with that
// tierDefault, one change of that tier with a mapped premortem and a valid
// approval, which only tier 3 needs.
func clean(tier manifest.Tier) Input {
	return Input{
		Policy: &manifest.Repo{Mode: manifest.Enforce, TierDefault: tier,
			Paths: manifest.Paths{DX: []string{"tools/**"}, Feat: []string{"internal/**"}, Seam: []string{"go.mod"}}},
		Head:      head,
		Changes:   []Change{{ID: "add-refunds", Tier: tier, Premortem: Premortem{Present: true, Items: 1}}},
		Baseline:  baseline.Empty(),
		Approvals: []evidence.Approval{approval},
	}
}

// ob is an obligation with one bound test that passes at head, and a base
// status consistent with strength s.
func ob(id string, d evidence.Delta, s evidence.Strength) evidence.Obligation {
	before := map[evidence.Strength]evidence.Status{evidence.Strong: evidence.Fail, evidence.Weak: evidence.BuildFail}[s]
	if before == "" {
		before = evidence.Pass
	}
	return evidence.Obligation{ID: id, Kind: id[len(id)-3 : len(id)-2], Delta: d, Characterization: s == evidence.Characterized,
		Tests: []string{"TestRefund/" + id}, Before: before, After: evidence.Pass, Strength: s}
}

// with returns o after edit.
func with(o evidence.Obligation, edit func(*evidence.Obligation)) evidence.Obligation {
	edit(&o)
	return o
}

// allowedInfo is the one INFO message openspec validate may report on a
// valid change (ADR-0002).
const allowedInfo = "skip_specs is set in .openspec.yaml: change declares no spec-level behavior changes, zero deltas accepted"

func obs(o ...evidence.Obligation) func(*Input) {
	return func(in *Input) { in.Obligations = o }
}

type decideCase struct {
	name  string
	tier  manifest.Tier
	edit  func(*Input)
	want  evidence.Result
	codes []string // of the reasons, in order
}

func TestDecide(t *testing.T) {
	t.Parallel()

	untested := evidence.Obligation{ID: "ORD-F01", Kind: "F", Delta: evidence.Added, Before: evidence.NotRun, After: evidence.NotRun, Strength: evidence.None}
	failing := ob("ORD-F01", evidence.Unchanged, evidence.None)
	failing.After = evidence.Fail
	tests := []decideCase{
		{name: "clean tier 0", want: evidence.ResultPass},
		{name: "clean tier 3", tier: 3, want: evidence.ResultPass},
		{name: "spec_rule", edit: func(in *Input) {
			in.SpecFindings = []openspec.Finding{{Severity: openspec.SeverityError, Rule: openspec.RuleDuplicateID, Path: "openspec/specs/a/spec.md", Message: "dup"}}
		}, want: evidence.ResultBlock, codes: []string{CodeSpecRule}},
		{name: "spec_warning", edit: func(in *Input) {
			in.SpecFindings = []openspec.Finding{{Severity: openspec.SeverityWarn, Rule: openspec.RuleNameLength, Path: "openspec/specs/a/spec.md", Message: "long"}}
		}, want: evidence.ResultWarn, codes: []string{CodeSpecWarning}},
		{name: "openspec_invalid", edit: func(in *Input) {
			in.Validation = &openspec.Report{Items: []openspec.Item{{ID: "add-refunds", Type: "change",
				Issues: []openspec.Issue{{Level: "ERROR", Path: "refunds/spec.md", Message: "bad"}}}}}
		}, want: evidence.ResultBlock, codes: []string{CodeOpenSpecInvalid}},
		{name: "unknown severity blocks", edit: func(in *Input) {
			in.SpecFindings = []openspec.Finding{{Severity: "fatal", Path: "openspec/specs/a/spec.md", Message: "?"}}
		}, want: evidence.ResultBlock, codes: []string{CodeSpecRule}},
		{name: "openspec_invalid without findings", edit: func(in *Input) {
			in.Validation = &openspec.Report{Items: []openspec.Item{{ID: "add-refunds", Type: "change",
				Issues: []openspec.Issue{{Level: "INFO", Message: allowedInfo}}}}}
		}, want: evidence.ResultBlock, codes: []string{CodeOpenSpecInvalid}},
		{name: "openspec validate passed", edit: func(in *Input) {
			in.Validation = &openspec.Report{Items: []openspec.Item{{ID: "refunds", Type: "spec", Valid: true}}}
		}, want: evidence.ResultPass},
		{name: "open_question at tier 1", tier: 1, edit: obs(ob("ORD-O01", evidence.Modified, evidence.None)),
			want: evidence.ResultBlock, codes: []string{CodeOpenQuestion}},
		{name: "open_question not below tier 1", edit: obs(ob("ORD-O01", evidence.Added, evidence.None)), want: evidence.ResultPass},
		{name: "removing an O does not block", tier: 1, edit: obs(ob("ORD-O01", evidence.Unchanged, evidence.None)), want: evidence.ResultPass},
		{name: "unknown kind fails closed", tier: 1, edit: obs(evidence.Obligation{ID: "ORD-X01", Kind: "X", Delta: evidence.Added}),
			want: evidence.ResultBlock, codes: []string{CodeOpenQuestion}},
		{name: "empty delta fails closed", tier: 1, edit: obs(with(untested, func(o *evidence.Obligation) { o.Delta = "" })),
			want: evidence.ResultBlock, codes: []string{CodeUnverified}},
		{name: "upper-case delta fails closed", tier: 1, edit: obs(with(untested, func(o *evidence.Obligation) { o.Delta = "ADDED" })),
			want: evidence.ResultBlock, codes: []string{CodeUnverified}},
		{name: "removed delta fails closed", tier: 1, edit: obs(with(untested, func(o *evidence.Obligation) { o.Delta = "removed" })),
			want: evidence.ResultBlock, codes: []string{CodeUnverified}},
		{name: "failing head on an added obligation is only after_not_passing", tier: 1,
			edit: obs(with(ob("ORD-F01", evidence.Added, evidence.None), func(o *evidence.Obligation) { o.Before, o.After = evidence.Fail, evidence.Fail })),
			want: evidence.ResultBlock, codes: []string{CodeAfterNotPassing}},
		{name: "characterization without the marker is inconsistent", tier: 1,
			edit: obs(with(ob("ORD-F01", evidence.Added, evidence.Characterized), func(o *evidence.Obligation) { o.Characterization = false })),
			want: evidence.ResultBlock, codes: []string{CodeFailBeforeMissing}},
		{name: "strong that passed at the base is inconsistent", tier: 1,
			edit: obs(with(ob("ORD-F01", evidence.Added, evidence.Strong), func(o *evidence.Obligation) { o.Before = evidence.Pass })),
			want: evidence.ResultBlock, codes: []string{CodeFailBeforeMissing}},
		{name: "a kind that is not the ID's is inconsistent", tier: 1,
			edit: obs(with(ob("ORD-F01", evidence.Added, evidence.None), func(o *evidence.Obligation) { o.Kind = "A" })),
			want: evidence.ResultBlock, codes: []string{CodeAssumption, CodeFailBeforeMissing}},
		{name: "strong on an unchanged obligation is inconsistent", tier: 1, edit: obs(ob("ORD-F01", evidence.Unchanged, evidence.Strong)),
			want: evidence.ResultBlock, codes: []string{CodeFailBeforeMissing}},
		{name: "unverified at tier 1", tier: 1, edit: obs(untested), want: evidence.ResultBlock, codes: []string{CodeUnverified}},
		{name: "unverified not below tier 1", edit: obs(untested), want: evidence.ResultPass},
		{name: "fail_before_missing at tier 1", tier: 1, edit: obs(ob("ORD-N01", evidence.Added, evidence.None)),
			want: evidence.ResultBlock, codes: []string{CodeFailBeforeMissing}},
		{name: "fail_before_missing not below tier 1", edit: obs(ob("ORD-N01", evidence.Added, evidence.None)), want: evidence.ResultPass},
		{name: "strong and characterization are valid", tier: 1,
			edit: obs(ob("ORD-F01", evidence.Added, evidence.Strong), ob("ORD-I01", evidence.Modified, evidence.Characterized)), want: evidence.ResultPass},
		{name: "after_not_passing at tier 0", edit: obs(failing), want: evidence.ResultBlock, codes: []string{CodeAfterNotPassing}},
		{name: "not_run at head is not passing", edit: obs(with(failing, func(o *evidence.Obligation) { o.After = evidence.NotRun })),
			want: evidence.ResultBlock, codes: []string{CodeAfterNotPassing}},
		{name: "skipped at head is not passing", edit: obs(with(failing, func(o *evidence.Obligation) { o.After = evidence.Skipped })),
			want: evidence.ResultBlock, codes: []string{CodeAfterNotPassing}},
		{name: "build_fail at head is not passing", edit: obs(with(failing, func(o *evidence.Obligation) { o.After = evidence.BuildFail })),
			want: evidence.ResultBlock, codes: []string{CodeAfterNotPassing}},
		{name: "regression", edit: func(in *Input) { in.UnboundFailures = []baseline.Test{unbound} },
			want: evidence.ResultBlock, codes: []string{CodeRegression}},
		{name: "a new subtest under a baseline parent is a regression", edit: func(in *Input) {
			in.UnboundFailures = []baseline.Test{{Package: unbound.Package, Test: unbound.Test + "/sub"}}
			in.Baseline.Failing = []baseline.Test{unbound}
		}, want: evidence.ResultBlock, codes: []string{CodeRegression}},
		{name: "the baseline matches by package too", edit: func(in *Input) {
			in.UnboundFailures = []baseline.Test{{Package: "example.com/shop/other", Test: unbound.Test}}
			in.Baseline.Failing = []baseline.Test{unbound}
		}, want: evidence.ResultBlock, codes: []string{CodeRegression}},
		{name: "baseline failure is no regression", edit: func(in *Input) {
			in.UnboundFailures = []baseline.Test{unbound}
			in.Baseline.Failing = []baseline.Test{unbound}
		}, want: evidence.ResultPass},
		{name: "build_failed", edit: func(in *Input) { in.BuildFailures = []string{"example.com/shop/refund"} },
			want: evidence.ResultBlock, codes: []string{CodeBuildFailed}},
		{name: "tamper", edit: func(in *Input) {
			in.Tamper = []evidence.Finding{{Kind: evidence.PolicyEdited, Detail: "aval.yaml edited"}}
		}, want: evidence.ResultBlock, codes: []string{CodeTamper}},
		{name: "undeclared_runtime", edit: func(in *Input) { in.UndeclaredRuntime = []string{"ORD-F09"} },
			want: evidence.ResultBlock, codes: []string{CodeUndeclaredRuntime}},
		{name: "mixed_commit", edit: func(in *Input) {
			in.Scope = []evidence.Commit{{SHA: other, Family: evidence.FamilyMixed, Families: []evidence.Family{evidence.FamilyDX, evidence.FamilyFeat}}}
		}, want: evidence.ResultBlock, codes: []string{CodeMixedCommit}},
		{name: "premortem_missing at change tier 2", tier: 2, edit: func(in *Input) { in.Changes[0].Premortem = Premortem{} },
			want: evidence.ResultBlock, codes: []string{CodePremortemMissing}},
		{name: "premortem not needed below change tier 2", tier: 1, edit: func(in *Input) { in.Changes[0].Premortem = Premortem{} },
			want: evidence.ResultPass},
		{name: "the base tier of a change counts", tier: 1, edit: func(in *Input) { in.Changes[0] = Change{ID: "x", Tier: 1, BaseTier: 2} },
			want: evidence.ResultBlock, codes: []string{CodePremortemMissing}},
		{name: "tierDefault is a change's floor", tier: 1, edit: func(in *Input) {
			in.Policy.TierDefault = 2
			in.Changes[0].Premortem = Premortem{}
		},
			want: evidence.ResultBlock, codes: []string{CodePremortemMissing}},
		{name: "premortem_unmapped without items", tier: 2, edit: func(in *Input) { in.Changes[0].Premortem.Items = 0 },
			want: evidence.ResultBlock, codes: []string{CodePremortemUnmapped}},
		{name: "premortem_unmapped not below change tier 2", tier: 1, edit: func(in *Input) {
			in.Changes[0].Premortem = Premortem{Present: true, Unmapped: []string{"db fails"}}
		}, want: evidence.ResultPass},
		{name: "premortem_unmapped item", tier: 2, edit: func(in *Input) { in.Changes[0].Premortem.Unmapped = []string{"db fails"} },
			want: evidence.ResultBlock, codes: []string{CodePremortemUnmapped}},
		{name: "approval_missing at tier 3", tier: 3, edit: func(in *Input) { in.Approvals = nil },
			want: evidence.ResultBlock, codes: []string{CodeApprovalMissing}},
		{name: "approval not needed below tier 3", tier: 2, edit: func(in *Input) { in.Approvals = nil }, want: evidence.ResultPass},
		{name: "lint_new_issues", edit: func(in *Input) { in.Lint = Lint{BaseConfig: true, Ran: true, NewIssues: 1} },
			want: evidence.ResultBlock, codes: []string{CodeLintNewIssues}},
		{name: "lint clean", edit: func(in *Input) { in.Lint = Lint{BaseConfig: true, Ran: true} }, want: evidence.ResultPass},
		{name: "lint did not run", edit: func(in *Input) { in.Lint = Lint{BaseConfig: true} },
			want: evidence.ResultBlock, codes: []string{CodeLintNewIssues}},
		{name: "lint ignored without base config", edit: func(in *Input) { in.Lint = Lint{NewIssues: 2} }, want: evidence.ResultPass},
		{name: "assumption at tier 1", tier: 1, edit: obs(ob("ORD-A01", evidence.Added, evidence.None)),
			want: evidence.ResultWarn, codes: []string{CodeAssumption}},
		{name: "assumption not below tier 1", edit: obs(ob("ORD-A01", evidence.Added, evidence.None)), want: evidence.ResultPass},
		{name: "slo_unverified at tier 1", tier: 1, edit: obs(ob("ORD-S01", evidence.Modified, evidence.None)),
			want: evidence.ResultWarn, codes: []string{CodeSLOUnverified}},
		{name: "seam_touched", edit: func(in *Input) {
			in.Scope = []evidence.Commit{{SHA: other, Family: evidence.FamilyFeat, Paths: []string{"go.mod", "internal/a.go"}}}
		}, want: evidence.ResultWarn, codes: []string{CodeSeamTouched}},
		{name: "a dx commit is not seam_touched", edit: func(in *Input) {
			in.Scope = []evidence.Commit{{SHA: other, Family: evidence.FamilyDX, Paths: []string{"go.mod", "tools/a.go"}}}
		}, want: evidence.ResultPass},
		{name: "seam alone is not seam_touched", edit: func(in *Input) {
			in.Scope = []evidence.Commit{{SHA: other, Family: evidence.FamilySeam, Paths: []string{"go.mod"}}}
		}, want: evidence.ResultPass},
		{name: "weak_evidence at tier 1", tier: 1, edit: obs(ob("ORD-F01", evidence.Added, evidence.Weak)),
			want: evidence.ResultWarn, codes: []string{CodeWeakEvidence}},
		{name: "weak_evidence not below tier 1", edit: obs(ob("ORD-F01", evidence.Added, evidence.Weak)), want: evidence.ResultPass},
		{name: "no_base_policy", edit: func(in *Input) { in.Policy = nil }, want: evidence.ResultWarn, codes: []string{CodeNoBasePolicy}},
		{name: "no_base_policy keeps the other rules", edit: func(in *Input) {
			in.Policy = nil
			in.BuildFailures = []string{"x"}
		}, want: evidence.ResultBlock, codes: []string{CodeBuildFailed, CodeNoBasePolicy}},
		{name: "valid override turns block into warn", tier: 3, edit: func(in *Input) {
			in.Approvals = []evidence.Approval{override}
			in.BuildFailures = []string{"x"}
		}, want: evidence.ResultWarn, codes: []string{CodeApprovalMissing, CodeBuildFailed}},
		{name: "valid approval clears only approval_missing", tier: 3, edit: func(in *Input) { in.BuildFailures = []string{"x"} },
			want: evidence.ResultBlock, codes: []string{CodeBuildFailed}},
		{name: "override leaves a warn a warn", tier: 1, edit: func(in *Input) {
			in.Approvals = []evidence.Approval{override}
			in.Obligations = []evidence.Obligation{ob("ORD-A01", evidence.Added, evidence.None)}
		}, want: evidence.ResultWarn, codes: []string{CodeAssumption}},
		{name: "override of a clean change passes", edit: func(in *Input) { in.Approvals = []evidence.Approval{override} }, want: evidence.ResultPass},
	}
	for _, inv := range invalidOverrides() {
		tests = append(tests, decideCase{name: "invalid override: " + inv.Rejection, tier: 3, edit: func(in *Input) {
			in.Approvals = []evidence.Approval{inv}
			in.BuildFailures = []string{"x"}
		}, want: evidence.ResultBlock, codes: []string{CodeApprovalMissing, CodeBuildFailed}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := clean(tt.tier)
			if tt.edit != nil {
				tt.edit(&in)
			}
			got := Decide(in)
			var codes []string
			for _, r := range got.Reasons {
				codes = append(codes, r.Code)
			}
			if got.Result != tt.want || !slices.Equal(codes, tt.codes) {
				t.Errorf("Decide() = %s %v, want %s %v\n%+v", got.Result, codes, tt.want, tt.codes, got.Reasons)
			}
		})
	}
}

// invalidOverrides are overrides that must change nothing.
func invalidOverrides() []evidence.Approval {
	rejected, stale, empty := override, override, override
	rejected.Valid, rejected.Rejection = false, "not a CODEOWNER"
	stale.CommitID, stale.Rejection = other, "stale commit"
	empty.Reason, empty.Rejection = " ", "empty reason"
	return []evidence.Approval{rejected, stale, empty}
}

func TestTier(t *testing.T) {
	t.Parallel()

	commit := func(f evidence.Family) []evidence.Commit { return []evidence.Commit{{SHA: other, Family: f}} }
	tests := []struct {
		name    string
		scope   []evidence.Commit
		changes []Change
		want    manifest.Tier
	}{
		{name: "empty", want: 0},
		{name: "dx, seam and other only", scope: []evidence.Commit{{Family: evidence.FamilyDX}, {Family: evidence.FamilySeam}, {Family: evidence.FamilyOther}}, want: 0},
		{name: "feat", scope: commit(evidence.FamilyFeat), want: 2},
		{name: "mixed", scope: commit(evidence.FamilyMixed), want: 2},
		{name: "unknown family", scope: commit("weird"), want: 2},
		{name: "change below the default", changes: []Change{{Tier: 1}}, want: 2},
		{name: "head raises", changes: []Change{{Tier: 3, BaseTier: 1}}, want: 3},
		{name: "head never lowers", changes: []Change{{Tier: 0, BaseTier: 3}}, want: 3},
		{name: "max over changes", changes: []Change{{Tier: 1}, {Tier: 3}}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Tier(2, tt.scope, tt.changes); got != tt.want {
				t.Errorf("Tier(2, ...) = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEffect(t *testing.T) {
	t.Parallel()

	warns := []string{CodeAssumption, CodeSLOUnverified, CodeSpecWarning, CodeSeamTouched, CodeWeakEvidence, CodeNoBasePolicy}
	for code := range rules {
		want := evidence.ResultBlock
		if slices.Contains(warns, code) {
			want = evidence.ResultWarn
		}
		if got := Effect(code); got != want {
			t.Errorf("Effect(%q) = %s, want %s", code, got, want)
		}
	}
	for _, code := range []string{CodeContractBreaking, CodeContractLint, CodeChangeNotArchived, "nope"} {
		if _, known := rules[code]; known || Effect(code) != evidence.ResultBlock {
			t.Errorf("Effect(%q) = %s; unknown and reserved codes must block and have no rule", code, Effect(code))
		}
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		mode   manifest.Mode
		result evidence.Result
		want   int
	}{
		{manifest.Observe, evidence.ResultBlock, 0},
		{manifest.Enforce, evidence.ResultPass, 0},
		{manifest.Enforce, evidence.ResultWarn, 0},
		{manifest.Enforce, evidence.ResultBlock, 1},
		{"", evidence.ResultBlock, 1}, // an unknown mode enforces
		{"", evidence.ResultWarn, 0},
		{manifest.Enforce, "", 1}, // an unknown result blocks
		{manifest.Enforce, "BLOCK", 1},
		{manifest.Observe, "BLOCK", 0},
	} {
		if got := ExitCode(tt.mode, evidence.Verdict{Result: tt.result}); got != tt.want {
			t.Errorf("ExitCode(%q, %s) = %d, want %d", tt.mode, tt.result, got, tt.want)
		}
	}
}

// genInput draws inputs over small domains, so rules collide and interact.
func genInput(t *rapid.T) Input {
	tier := rapid.Custom(func(t *rapid.T) manifest.Tier { return manifest.Tier(rapid.IntRange(0, 3).Draw(t, "tier")) })
	pick := func(t *rapid.T, label string, xs ...string) string { return rapid.SampledFrom(xs).Draw(t, label) }
	tests := []baseline.Test{unbound, {Package: "p", Test: "TestA"}, {Package: "p", Test: "TestA/sub"}}
	in := Input{Head: head, Baseline: baseline.Empty()}
	if rapid.Bool().Draw(t, "policy") {
		in.Policy = &manifest.Repo{Mode: manifest.Mode(pick(t, "mode", "observe", "enforce")), TierDefault: tier.Draw(t, "default"),
			Paths: manifest.Paths{Seam: []string{"go.mod"}}}
	}
	in.Scope = rapid.SliceOfN(rapid.Custom(func(t *rapid.T) evidence.Commit {
		c := evidence.Commit{SHA: pick(t, "sha", head, other), Family: evidence.Family(pick(t, "family", "dx", "feat", "seam", "mixed", "other"))}
		c.Paths = rapid.SliceOfN(rapid.SampledFrom([]string{"go.mod", "internal/a.go"}), 0, 2).Draw(t, "paths")
		return c
	}), 0, 3).Draw(t, "scope")
	in.Changes = rapid.SliceOfN(rapid.Custom(func(t *rapid.T) Change {
		return Change{ID: pick(t, "change", "a", "b"), Tier: tier.Draw(t, "head"), BaseTier: tier.Draw(t, "base"), Premortem: Premortem{
			Present: rapid.Bool().Draw(t, "present"), Items: rapid.IntRange(0, 2).Draw(t, "items"),
			Unmapped: rapid.SliceOfN(rapid.SampledFrom([]string{"x", "y"}), 0, 2).Draw(t, "unmapped")}}
	}), 0, 2).Draw(t, "changes")
	in.Obligations = rapid.SliceOfN(rapid.Custom(func(t *rapid.T) evidence.Obligation {
		o := ob(pick(t, "id", "ORD-F01", "ORD-N01", "ORD-I01", "ORD-S01", "ORD-A01", "ORD-O01"),
			evidence.Delta(pick(t, "delta", "added", "modified", "unchanged")), evidence.Strength(pick(t, "strength", "strong", "weak", "characterization", "none")))
		o.After = evidence.Status(pick(t, "after", "pass", "fail", "not_run"))
		if rapid.Bool().Draw(t, "untested") {
			o.Tests = nil
		}
		return o
	}), 0, 4).Draw(t, "obligations")
	in.SpecFindings = rapid.SliceOfN(rapid.Custom(func(t *rapid.T) openspec.Finding {
		return openspec.Finding{Severity: openspec.Severity(pick(t, "severity", "error", "warn")), Path: pick(t, "path", "a", "b"), Message: pick(t, "msg", "m", "n")}
	}), 0, 3).Draw(t, "findings")
	if rapid.Bool().Draw(t, "validated") {
		in.Validation = &openspec.Report{Items: rapid.SliceOfN(rapid.Custom(func(t *rapid.T) openspec.Item {
			return openspec.Item{ID: pick(t, "item", "a", "b"), Type: "spec", Valid: rapid.Bool().Draw(t, "valid")}
		}), 0, 2).Draw(t, "items")}
	}
	in.Tamper = rapid.SliceOfN(rapid.Custom(func(t *rapid.T) evidence.Finding {
		return evidence.Finding{Kind: evidence.FindingKind(pick(t, "kind", "skip_added", "policy_edited")), ID: pick(t, "tid", "", "ORD-F01"), Detail: "d"}
	}), 0, 2).Draw(t, "tamper")
	in.UndeclaredRuntime = rapid.SliceOfN(rapid.SampledFrom([]string{"ORD-F01", "ORD-F02"}), 0, 2).Draw(t, "undeclared")
	in.BuildFailures = rapid.SliceOfN(rapid.SampledFrom([]string{"p", "q"}), 0, 2).Draw(t, "build")
	in.UnboundFailures = rapid.SliceOfN(rapid.SampledFrom(tests), 0, 3).Draw(t, "unbound")
	in.Baseline.Failing = rapid.SliceOfN(rapid.SampledFrom(tests), 0, 3).Draw(t, "baseline")
	in.Lint = Lint{BaseConfig: rapid.Bool().Draw(t, "lintcfg"), Ran: rapid.Bool().Draw(t, "lintran"), NewIssues: rapid.IntRange(0, 2).Draw(t, "issues")}
	in.Approvals = rapid.SliceOfN(rapid.SampledFrom(append(invalidOverrides(), approval, override)), 0, 3).Draw(t, "approvals")
	return in
}

var rank = map[evidence.Result]int{evidence.ResultPass: 0, evidence.ResultWarn: 1, evidence.ResultBlock: 2}

func TestAddingABlockNeverImproves(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		before := Decide(in)
		switch rapid.IntRange(0, 5).Draw(t, "block") {
		case 0:
			in.BuildFailures = append(slices.Clone(in.BuildFailures), "new")
		case 1:
			in.Tamper = append(slices.Clone(in.Tamper), evidence.Finding{Kind: evidence.BaselineEdited, Detail: "new"})
		case 2:
			in.Scope = append(slices.Clone(in.Scope), evidence.Commit{SHA: "new", Family: evidence.FamilyMixed})
		case 3:
			in.UnboundFailures = append(slices.Clone(in.UnboundFailures), baseline.Test{Package: "p", Test: "TestNew"})
		case 4:
			in.UndeclaredRuntime = append(slices.Clone(in.UndeclaredRuntime), "ORD-F77")
		default:
			in.SpecFindings = append(slices.Clone(in.SpecFindings), openspec.Finding{Severity: openspec.SeverityError, Message: "new"})
		}
		after := Decide(in)
		// genInput draws approvals from a fixed set in which override is the
		// only valid override.
		want := evidence.ResultBlock
		if slices.Contains(in.Approvals, override) {
			want = evidence.ResultWarn
		}
		if rank[after.Result] < rank[before.Result] || after.Result != want {
			t.Fatalf("adding a block: %s → %s, want %s", before.Result, after.Result, want)
		}
	})
}

func TestObserveAlwaysExitsZero(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		v := Decide(in)
		if got := ExitCode(manifest.Observe, v); got != 0 {
			t.Fatalf("ExitCode(observe, %s) = %d", v.Result, got)
		}
		if in.Policy == nil && ExitCode(in.Mode(), v) != 0 {
			t.Fatalf("no base policy must observe, got mode %q", in.Mode())
		}
		if got, want := ExitCode(manifest.Enforce, v), rank[v.Result]/2; got != want {
			t.Fatalf("ExitCode(enforce, %s) = %d, want %d", v.Result, got, want)
		}
	})
}

func TestVerdictIsAValidBundleVerdict(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		v := Decide(in)
		b := evidence.Bundle{SchemaVersion: evidence.SchemaVersion, Repo: "o/r", TrustBase: other, ChangeBase: other, BaseRef: "main", Head: head, AvalVersion: "v0.0.0-test",
			GeneratedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), Mode: string(in.Mode()), Tier: int(in.Tier()), Verdict: v}
		if err := b.Validate(); err != nil {
			t.Fatalf("bundle with verdict %+v: %v", v, err)
		}
		if !slices.IsSortedFunc(v.Reasons, func(a, b evidence.Reason) int {
			return cmp.Or(strings.Compare(a.Code, b.Code), strings.Compare(a.ID, b.ID), strings.Compare(a.Message, b.Message))
		}) {
			t.Fatalf("reasons not sorted: %+v", v.Reasons)
		}
	})
}

func TestDecideIgnoresOrder(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		want := Decide(in)
		p := in
		p.Scope = rapid.Permutation(in.Scope).Draw(t, "scope")
		p.Changes = rapid.Permutation(in.Changes).Draw(t, "changes")
		for i := range p.Changes {
			p.Changes[i].Premortem.Unmapped = rapid.Permutation(p.Changes[i].Premortem.Unmapped).Draw(t, "unmapped")
		}
		p.Obligations = rapid.Permutation(in.Obligations).Draw(t, "obligations")
		p.SpecFindings = rapid.Permutation(in.SpecFindings).Draw(t, "findings")
		if in.Validation != nil {
			p.Validation = &openspec.Report{Items: rapid.Permutation(in.Validation.Items).Draw(t, "items")}
		}
		p.Tamper = rapid.Permutation(in.Tamper).Draw(t, "tamper")
		p.UndeclaredRuntime = rapid.Permutation(in.UndeclaredRuntime).Draw(t, "undeclared")
		p.BuildFailures = rapid.Permutation(in.BuildFailures).Draw(t, "build")
		p.UnboundFailures = rapid.Permutation(in.UnboundFailures).Draw(t, "unbound")
		p.Baseline.Failing = rapid.Permutation(in.Baseline.Failing).Draw(t, "baseline")
		p.Approvals = rapid.Permutation(in.Approvals).Draw(t, "approvals")
		if got := Decide(p); got.Result != want.Result || !slices.Equal(got.Reasons, want.Reasons) {
			t.Fatalf("permuted input changed the verdict:\n got %+v\nwant %+v", got, want)
		}
	})
}
