package main

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// The sticky comment's visible text must surface the verdict and per-file
// errors on its own — a human reading the thread should not have to open the
// check run to learn that a run failed or why.
func TestStickyVisibleBodyShowsVerdictAndErrors(t *testing.T) {
	rec := &model.RunRecord{
		Model:         "m",
		Verdict:       model.VerdictCouldNotEvaluate,
		VerdictReason: "coverage incomplete",
		Coverage:      model.Coverage{APIFiles: 4, Reviewed: 3, Errored: 1},
		Samples:       1,
		Files: []model.FileOutcome{
			{Path: "a.go", State: model.FileReviewed, Findings: 0},
			{Path: "big.go", State: model.FileErrored, Reason: "output truncated at token cap"},
		},
	}
	out := stickyVisibleBody(rec)
	for _, want := range []string{
		"**Last verdict:** COULD_NOT_EVALUATE — coverage incomplete",
		"Coverage: 3/4 files reviewed, **1 errored**",
		"`big.go` errored (output truncated at token cap)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sticky body missing %q\n---\n%s", want, out)
		}
	}
}

// When the run executes inside GitHub Actions, the sticky comment must link
// to the Actions run that produced it, so a maintainer reading a finding can
// open the reviewer's log.
func TestStickyVisibleBodyLinksRun(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "elecnix/cite")
	t.Setenv("GITHUB_RUN_ID", "1234567890")
	rec := &model.RunRecord{
		Model:    "m",
		Verdict:  model.VerdictPass,
		Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
	}
	out := stickyVisibleBody(rec)
	want := "https://github.com/elecnix/cite/actions/runs/1234567890"
	if !strings.Contains(out, want) {
		t.Errorf("sticky body missing run link %q\n---\n%s", want, out)
	}
}

// Outside GitHub Actions (local run, other CI) the link must be absent —
// silently, with no broken or empty link markdown.
func TestStickyVisibleBodyNoRunLinkWithoutEnv(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("GITHUB_RUN_ID", "")
	rec := &model.RunRecord{
		Model:    "m",
		Verdict:  model.VerdictPass,
		Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
	}
	out := stickyVisibleBody(rec)
	if strings.Contains(out, "actions/runs") {
		t.Errorf("sticky body must not link a run outside Actions:\n%s", out)
	}
	if strings.Contains(out, "](") || strings.Contains(out, "()") {
		t.Errorf("sticky body must not contain malformed link markdown:\n%s", out)
	}
}

// GITHUB_RUN_ID is ambient environment input. GitHub itself sets it to a
// bare decimal, but a hand-crafted value containing markdown metacharacters
// must never reach the rendered comment: no link at all, no injected text.
func TestStickyVisibleBodyRejectsHostileRunID(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "elecnix/cite")
	t.Setenv("GITHUB_RUN_ID", "1) [x] (https://evil.example)")
	rec := &model.RunRecord{
		Model:    "m",
		Verdict:  model.VerdictPass,
		Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
	}
	out := stickyVisibleBody(rec)
	if strings.Contains(out, "Reviewer log") || strings.Contains(out, "actions/runs") {
		t.Errorf("hostile run ID must suppress the link entirely:\n%s", out)
	}
	if strings.Contains(out, "evil.example") || strings.Contains(out, "[x]") {
		t.Errorf("hostile run ID must not leak into the comment body:\n%s", out)
	}
}

// Clean runs must not render an errors section or stray formatting.
func TestStickyVisibleBodyCleanRun(t *testing.T) {
	rec := &model.RunRecord{
		Model:         "m",
		Verdict:       model.VerdictPass,
		VerdictReason: "all in-scope files reviewed; nothing blocks",
		Coverage:      model.Coverage{APIFiles: 2, Reviewed: 2, Complete: true},
		Samples:       1,
		Files: []model.FileOutcome{
			{Path: "a.go", State: model.FileReviewed},
			{Path: "b.go", State: model.FileReviewed},
		},
	}
	out := stickyVisibleBody(rec)
	if strings.Contains(out, "errored") || strings.Contains(out, "⚠️") {
		t.Errorf("clean run should show no error lines:\n%s", out)
	}
	if !strings.Contains(out, "**Last verdict:** PASS") {
		t.Errorf("missing verdict line:\n%s", out)
	}
}
