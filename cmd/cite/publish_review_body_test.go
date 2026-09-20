package main

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
	"github.com/elecnix/cite/internal/scope"
)

// The review body's drops line must link the GitHub Actions run that
// produced it when Cite executes inside Actions (PLAN.md: the review body
// links the run artifact), so a reviewer reading "findings dropped by
// safety rails" can click straight to the drop log instead of hunting for
// the run. Outside Actions there is no run to link, so the line falls back
// to plain text.
func TestBuildReviewPayloadLinksRunLog(t *testing.T) {
	rec := &model.RunRecord{
		Drops: []model.DropEntry{
			{Path: "a.go", Category: model.CategoryCrash, Title: "unanchored", Reason: model.DropAnchorInvalid},
		},
	}
	plan := publisher.ReconciliationPlan{
		CommentsToPost: []model.ValidatedFinding{
			{
				Path:        "a.go",
				Fingerprint: "0123456789abcdef0123456789abcdef",
				Finding: model.Finding{
					Category: model.CategoryCrash,
					Title:    "Off by one",
					Anchor:   model.Anchor{StartLine: 1, EndLine: 2},
				},
			},
		},
	}

	t.Run("inside Actions", func(t *testing.T) {
		t.Setenv("GITHUB_SERVER_URL", "https://github.com")
		t.Setenv("GITHUB_REPOSITORY", "elecnix/gridkeeper")
		t.Setenv("GITHUB_RUN_ID", "1234567890")
		body, _, _ := buildReviewPayload(map[string]*scope.DiffFile{}, plan, &instructions.ResolvedInstructions{}, rec)
		want := "[the run log](https://github.com/elecnix/gridkeeper/actions/runs/1234567890)"
		if !strings.Contains(body, want) {
			t.Fatalf("review body must link the run log, want %q in:\n%s", want, body)
		}
	})

	t.Run("outside Actions", func(t *testing.T) {
		t.Setenv("GITHUB_SERVER_URL", "")
		t.Setenv("GITHUB_REPOSITORY", "")
		t.Setenv("GITHUB_RUN_ID", "")
		body, _, _ := buildReviewPayload(map[string]*scope.DiffFile{}, plan, &instructions.ResolvedInstructions{}, rec)
		if !strings.Contains(body, "1 findings dropped by safety rails this run; see the run log.") {
			t.Fatalf("plain-text drops line missing:\n%s", body)
		}
		if strings.Contains(body, "actions/runs") {
			t.Fatalf("no Actions env must mean no run link:\n%s", body)
		}
	})
}
