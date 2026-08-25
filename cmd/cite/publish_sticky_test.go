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
