package gate

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

func baseRecord() *model.RunRecord {
	return &model.RunRecord{
		SchemaVersion: model.SchemaVersion,
		Repository:    "octo/app",
		PRNumber:      7,
		HeadSHA:       "abc123",
		Coverage: model.Coverage{
			APIFiles: 2, Reviewed: 2, ApprovedSkip: 0, Errored: 0, Complete: true,
		},
		Samples: 1,
	}
}

func TestDecidePass(t *testing.T) {
	rec := baseRecord()
	v, reason := Decide(rec, config.Default(), Options{})
	if v != model.VerdictPass {
		t.Fatalf("verdict = %s, want PASS (reason %q)", v, reason)
	}
	if reason == "" {
		t.Fatal("PASS must carry a reason")
	}
}

func TestDecideFoundWhenFindingBlocks(t *testing.T) {
	rec := baseRecord()
	rec.Findings = []model.ValidatedFinding{{
		Finding: model.Finding{
			ID:         "f1",
			Category:   model.CategoryInjection,
			Title:      "user input reaches shell",
			Confidence: model.ConfidenceCertain,
		},
		Path:   "app/handler.go",
		Blocks: true,
	}}
	v, reason := Decide(rec, config.Default(), Options{})
	if v != model.VerdictFound {
		t.Fatalf("verdict = %s, want FOUND", v)
	}
	if !strings.Contains(reason, "injection") {
		t.Fatalf("reason %q should name the category", reason)
	}
}

func TestDecideNonBlockingFindingDoesNotBlock(t *testing.T) {
	rec := baseRecord()
	rec.Findings = []model.ValidatedFinding{{
		Finding: model.Finding{
			ID:         "f2",
			Category:   model.CategoryConvention,
			Title:      "naming",
			Confidence: model.ConfidenceLikely,
		},
		Path:   "a.go",
		Blocks: false,
	}}
	if v, _ := Decide(rec, config.Default(), Options{}); v != model.VerdictPass {
		t.Fatalf("verdict = %s, want PASS for a non-blocking finding", v)
	}
}

func TestDecideCouldNotEvaluateMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*model.RunRecord, *Options)
		wantSub string
	}{
		{
			name: "provider failed",
			mutate: func(r *model.RunRecord, o *Options) {
				o.ProviderFailed = true
				r.VerdictReason = "provider 503 after 3 retries"
			},
			wantSub: "provider 503",
		},
		{
			name: "budget tripped with default reason",
			mutate: func(r *model.RunRecord, o *Options) {
				o.BudgetTripped = true
			},
			wantSub: "budget tripped",
		},
		{
			name: "zero api files",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Coverage = model.Coverage{}
			},
			wantSub: "path-filter bypass",
		},
		{
			name: "empty in scope despite listed files",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Coverage = model.Coverage{APIFiles: 3, Complete: false}
				r.Files = nil
			},
			wantSub: "path-filter bypass",
		},
		{
			name: "errored file",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Files = []model.FileOutcome{
					{Path: "ok.go", State: model.FileReviewed},
					{Path: "bad.go", State: model.FileErrored},
				}
				r.Coverage = model.Coverage{APIFiles: 2, Reviewed: 1, Complete: false}
			},
			wantSub: "bad.go",
		},
		{
			name: "unapproved skip reason",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Files = []model.FileOutcome{
					{Path: "ok.go", State: model.FileReviewed},
					{Path: "weird.bin", State: model.FileSkipped, Reason: "looks_binary_i_guess"},
				}
				r.Coverage = model.Coverage{APIFiles: 2, Reviewed: 1, Complete: false}
			},
			wantSub: "unapproved skip",
		},
		{
			name: "risk cutoff skip is not approved coverage",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Files = []model.FileOutcome{
					{Path: "big.go", State: model.FileReviewed},
					{Path: "small.go", State: model.FileSkipped, Reason: scopeSkipRiskCutoff},
				}
				r.Coverage = model.Coverage{APIFiles: 2, Reviewed: 1, Complete: false}
			},
			wantSub: "risk_rank_cutoff",
		},
		{
			name: "coverage incomplete",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Coverage = model.Coverage{APIFiles: 5, Reviewed: 4, Complete: false}
			},
			wantSub: "coverage incomplete",
		},
		{
			name: "no samples",
			mutate: func(r *model.RunRecord, o *Options) {
				r.Samples = 0
			},
			wantSub: "no sample",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := baseRecord()
			var opts Options
			tc.mutate(rec, &opts)
			v, reason := Decide(rec, config.Default(), opts)
			if v != model.VerdictCouldNotEvaluate {
				t.Fatalf("verdict = %s (%s), want COULD_NOT_EVALUATE", v, reason)
			}
			if !strings.Contains(reason, tc.wantSub) {
				t.Fatalf("reason %q does not mention %q", reason, tc.wantSub)
			}
		})
	}
}

// scopeSkipRiskCutoff mirrors the scope package constant without importing
// it twice under test; the value is asserted to match below.
const scopeSkipRiskCutoff = "risk_rank_cutoff"

func TestApprovedSkipReasonsMatchScope(t *testing.T) {
	got := map[string]bool{}
	for _, r := range ApprovedSkipReasons {
		got[r] = true
	}
	for _, want := range []string{"binary", "vendored", "lockfile", "generated", "minified", "paths_ignore", "oversized"} {
		if !got[want] {
			t.Errorf("ApprovedSkipReasons missing %q", want)
		}
		delete(got, want)
	}
	for extra := range got {
		t.Errorf("ApprovedSkipReasons has unexpected entry %q", extra)
	}
}

func TestDecideNilRecordFailsClosed(t *testing.T) {
	if v, _ := Decide(nil, config.Default(), Options{}); v != model.VerdictCouldNotEvaluate {
		t.Fatalf("nil record verdict = %s, want COULD_NOT_EVALUATE", v)
	}
	if v, reason := DecideDisabled(config.Default()); v != model.VerdictPass || reason != "disabled by configuration" {
		t.Fatalf("DecideDisabled = (%s, %q), want (PASS, disabled by configuration)", v, reason)
	}
}

func TestConclusionsAreFailClosed(t *testing.T) {
	for _, v := range []model.Verdict{model.VerdictFound, model.VerdictCouldNotEvaluate} {
		if v.Conclusion() != "failure" {
			t.Errorf("%s concludes %q, want failure", v, v.Conclusion())
		}
	}
	if model.VerdictPass.Conclusion() != "success" {
		t.Errorf("PASS concludes %q, want success", model.VerdictPass.Conclusion())
	}
}

func TestCheckRunPayloadIncludesVerbatimSampleLineAndSkips(t *testing.T) {
	rec := baseRecord()
	rec.Samples = 1
	rec.Files = []model.FileOutcome{
		{Path: "a.go", State: model.FileReviewed},
		{Path: "img/logo.png", State: model.FileSkipped, Reason: "binary"},
		{Path: "img/other.png", State: model.FileSkipped, Reason: "binary"},
	}
	rec.Coverage = model.Coverage{APIFiles: 3, Reviewed: 1, ApprovedSkip: 2, Complete: true}

	title, summary := CheckRunPayload(rec, model.VerdictPass, "all clear")

	if !strings.Contains(title, string(model.VerdictPass)) {
		t.Errorf("title %q should name the state", title)
	}
	if !strings.Contains(summary, rec.Summary()) {
		t.Errorf("summary missing the verbatim one-sample line:\n%s", summary)
	}
	stateLine := string(model.VerdictPass) + " — all clear"
	if !strings.Contains(summary, stateLine) {
		t.Errorf("summary missing state line %q:\n%s", stateLine, summary)
	}
	if !strings.Contains(summary, "Skipped (binary): 2 file(s): img/logo.png, img/other.png") {
		t.Errorf("summary missing skipped aggregate:\n%s", summary)
	}
}

func TestCheckRunPayloadNoSkippedSectionWhenClean(t *testing.T) {
	rec := baseRecord()
	_, summary := CheckRunPayload(rec, model.VerdictPass, "fine")
	if strings.Contains(summary, "Skipped") {
		t.Errorf("summary should not have a skip section:\n%s", summary)
	}
}

func TestBypassSummaryFormat(t *testing.T) {
	got := BypassSummary(model.VerdictCouldNotEvaluate, "alice", "https://github.com/octo/app/actions/runs/9")
	want := "BYPASSED — COULD_NOT_EVALUATE — @alice — https://github.com/octo/app/actions/runs/9"
	if got != want {
		t.Fatalf("BypassSummary = %q, want %q", got, want)
	}
	if BypassLabel != "cite-bypass" {
		t.Fatalf("BypassLabel = %q", BypassLabel)
	}
}

func TestDecideToolFailureTolerated(t *testing.T) {
	// Issue #59: a repository may opt out of a TOOL FAILURE blocking the
	// merge while a real finding still does. The gate expresses the opt-out
	// as NeutralToolFailure: COULD_NOT_EVALUATE keeps its verdict and its
	// reason, but no longer concludes failure. FOUND is never touched.
	rec := baseRecord()
	rec.VerdictReason = "provider 503 after 3 retries"
	// The realistic tool-failure shape: the provider died mid-run, so the
	// job reports it and the gate concludes COULD_NOT_EVALUATE.
	var opts Options
	opts.NeutralToolFailure = true
	opts.ProviderFailed = true
	v, reason := Decide(rec, config.Default(), opts)
	if v != model.VerdictCouldNotEvaluate {
		t.Fatalf("verdict = %s, want COULD_NOT_EVALUATE (the state itself must not change)", v)
	}
	if !strings.Contains(reason, "provider 503") {
		t.Fatalf("reason %q should survive verbatim", reason)
	}
	if got := Conclusion(v, Options{NeutralToolFailure: true}); got != "neutral" {
		t.Fatalf("conclusion = %q, want neutral when the repository opted out", got)
	}
	if got := Conclusion(v, Options{}); got != "failure" {
		t.Fatalf("conclusion = %q, want failure with the opt-out unset", got)
	}
}

func TestDecideToolFailureToleratedStillBlocksOnFinding(t *testing.T) {
	// The case that matters: opting out of tool-failure blocking must not
	// also wave real findings through. A blocking finding decides FOUND
	// regardless of the opt-out, and FOUND still concludes failure.
	rec := baseRecord()
	rec.Findings = []model.ValidatedFinding{{
		Finding: model.Finding{
			ID:         "f1",
			Category:   model.CategoryInjection,
			Title:      "user input reaches shell",
			Confidence: model.ConfidenceCertain,
		},
		Path:   "app/handler.go",
		Blocks: true,
	}}
	v, reason := Decide(rec, config.Default(), Options{NeutralToolFailure: true})
	if v != model.VerdictFound {
		t.Fatalf("verdict = %s, want FOUND; the opt-out covers tool failures only", v)
	}
	if got := Conclusion(v, Options{NeutralToolFailure: true}); got != "failure" {
		t.Fatalf("conclusion = %q, want failure for a real finding even opted out", got)
	}
	if !strings.Contains(reason, "injection") {
		t.Fatalf("reason %q should name the category", reason)
	}
}

func TestConclusionDefaultRemainsFailClosed(t *testing.T) {
	// Red stays the default: with the opt-out unset, COULD_NOT_EVALUATE
	// concludes failure exactly as before (issue #59, requirement 1).
	if got := model.VerdictCouldNotEvaluate.Conclusion(); got != "failure" {
		t.Fatalf("default conclusion for COULD_NOT_EVALUATE = %q, want failure", got)
	}
}

func TestNeedsReaper(t *testing.T) {
	open := []PRState{
		{HeadSHA: "aaa", HasTerminalCheck: true, AgeMinutes: 999},
		{HeadSHA: "bbb", HasTerminalCheck: false, AgeMinutes: 19}, // just under threshold
		{HeadSHA: "ccc", HasTerminalCheck: false, AgeMinutes: 20}, // exactly stale
		{HeadSHA: "ddd", HasTerminalCheck: false, AgeMinutes: 5000},
	}
	got := NeedsReaper(open, 20)
	if len(got) != 2 || got[0].HeadSHA != "ccc" || got[1].HeadSHA != "ddd" {
		t.Fatalf("NeedsReaper = %+v, want ccc and ddd in order", got)
	}
	if r := NeedsReaper(nil, 20); len(r) != 0 {
		t.Fatalf("empty input should reap nothing")
	}
}

// The check-run summary is the one surface a human sees on every run (the
// sticky comment is machine state; the run record is the artifact). Cost and
// cache behaviour belong there: §7 calls caching failure silent, and §15's
// "cost you can see" is the number to put in front of a buyer.
func TestCheckRunPayloadReportsCostAndCacheHit(t *testing.T) {
	rec := baseRecord()
	rec.Model = "deepseek/deepseek-v4-flash-0731"
	rec.CostUSD = 0.0123
	rec.Usage = model.Usage{
		InputTokens: 16000, OutputTokens: 900,
		CacheReadTokens: 9800, CacheWriteTokens: 1200,
	}
	_, summary := CheckRunPayload(rec, model.VerdictPass, "all clear")

	if !strings.Contains(summary, "cost: $0.0123") {
		t.Errorf("summary missing cost line:\n%s", summary)
	}
	if !strings.Contains(summary, "deepseek/deepseek-v4-flash-0731") {
		t.Errorf("summary missing model id:\n%s", summary)
	}
	// 9800/16000 = 61.25% — above the floor and worth showing.
	if !strings.Contains(summary, "61% cache-hit") {
		t.Errorf("summary missing cache-hit percentage:\n%s", summary)
	}
}

func TestCheckRunPayloadOmitsCostLineWithoutUsage(t *testing.T) {
	rec := baseRecord()
	_, summary := CheckRunPayload(rec, model.VerdictPass, "fine")
	if strings.Contains(summary, "cost:") || strings.Contains(summary, "cache-hit") {
		t.Errorf("no usage recorded ⇒ no cost/cache lines:\n%s", summary)
	}
}

func TestCheckRunPayloadCacheHitBelowFloorStillShown(t *testing.T) {
	// A cold miss must be visible, not hidden: the whole point of printing
	// the rate is that a regression shows up on the check itself.
	rec := baseRecord()
	rec.CostUSD = 0.5
	rec.Usage = model.Usage{InputTokens: 10000, OutputTokens: 900, CacheReadTokens: 500}
	_, summary := CheckRunPayload(rec, model.VerdictFound, "found")
	if !strings.Contains(summary, "5% cache-hit") {
		t.Errorf("low cache-hit must still render:\n%s", summary)
	}
}

// Issue #73: the gate's COULD_NOT_EVALUATE reason must surface the judge's
// condition — how many files were never evaluated, by which mechanism, and
// how much bounded retry was spent before giving up — not just a coverage
// count with nothing behind it. This reason flows verbatim into the check
// summary and the sticky comment through the existing plumbing.
func TestErroredReasonNamesMechanismAndBudgetSpent(t *testing.T) {
	rec := baseRecord()
	rec.Coverage = model.Coverage{APIFiles: 3, Reviewed: 1, Complete: false}
	rec.Files = []model.FileOutcome{
		{Path: "ok.go", State: model.FileReviewed},
		{Path: "bad.go", State: model.FileErrored, Reason: "parse_failure", Reasks: 2},
		{Path: "slow.go", State: model.FileErrored, Reason: "deadline_exceeded", Reasks: 1},
	}
	rec.ReasksSpent = 3
	v, reason := Decide(rec, config.Default(), Options{})
	if v != model.VerdictCouldNotEvaluate {
		t.Fatalf("verdict = %s, want COULD_NOT_EVALUATE", v)
	}
	for _, want := range []string{
		"2 file(s) never evaluated",
		"bad.go (parse_failure, 2 re-ask(s) spent)",
		"slow.go (deadline_exceeded, 1 re-ask(s) spent)",
		"3 bounded re-ask(s) spent",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q missing %q", reason, want)
		}
	}
}

// An errored file with no re-asks and no reason still fails closed with the
// path named — the legacy shape must not regress.
func TestErroredWithoutMechanismStillNamesPaths(t *testing.T) {
	rec := baseRecord()
	rec.Coverage = model.Coverage{APIFiles: 2, Reviewed: 1, Complete: false}
	rec.Files = []model.FileOutcome{
		{Path: "ok.go", State: model.FileReviewed},
		{Path: "bad.go", State: model.FileErrored},
	}
	v, reason := Decide(rec, config.Default(), Options{})
	if v != model.VerdictCouldNotEvaluate {
		t.Fatalf("verdict = %s, want COULD_NOT_EVALUATE", v)
	}
	if !strings.Contains(reason, "bad.go") {
		t.Errorf("reason %q must still name the path", reason)
	}
}
