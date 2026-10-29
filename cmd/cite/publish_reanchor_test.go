package main

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
	"github.com/elecnix/cite/internal/scope"
)

// Issue #220: a re-anchored finding posts inline on the deleted line, quotes
// the use site, and no longer appears under "Notable but unanchored".
func TestBuildReviewPayloadPostsReanchoredFindingInline(t *testing.T) {
	plan := publisher.ReconciliationPlan{
		CommentsToPost: []model.ValidatedFinding{{
			Path:        "a.go",
			Fingerprint: "0123456789abcdef0123456789abcdef",
			Finding: model.Finding{
				Category: model.CategoryCrash,
				Title:    "retryLimit no longer exists",
				Anchor:   model.Anchor{StartLine: 30, EndLine: 30},
			},
			Reanchor: &model.Reanchor{
				Path: "a.go", Side: "LEFT", Line: 3, Kind: model.ReanchorDeletedDefinition,
				UseSite: model.UseSite{Line: 30, Text: "n := retryLimit * 2"},
			},
		}},
	}
	body, comments, unanchored := buildReviewPayload(map[string]*scope.DiffFile{}, plan, &instructions.ResolvedInstructions{}, &model.RunRecord{})
	if unanchored != 0 || len(comments) != 1 {
		t.Fatalf("unanchored=%d comments=%d, want 0 and 1", unanchored, len(comments))
	}
	c := comments[0]
	if c.Path != "a.go" || c.Side != "LEFT" || c.Line != 3 || c.StartLine != 0 {
		t.Errorf("comment = %+v, want a.go LEFT line 3", c)
	}
	if !strings.Contains(c.Body, "n := retryLimit * 2") || !strings.Contains(c.Body, "line 30") {
		t.Errorf("comment must quote the use site:\n%s", c.Body)
	}
	if strings.Contains(body, "unanchored") {
		t.Errorf("review body must not list the finding as unanchored:\n%s", body)
	}
}
