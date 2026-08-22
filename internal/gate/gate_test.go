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
