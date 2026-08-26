package main

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// TestStepSummaryDropsEmptyWhenNothingDropped: with no drops, nothing is
// written and the helper returns false (no file touched).
func TestStepSummaryDropsEmptyWhenNothingDropped(t *testing.T) {
	var sb strings.Builder
	wrote := writeDropSummary(&sb, nil)
	if wrote {
		t.Fatal("no drops must not write a step summary")
	}
	if sb.Len() != 0 {
		t.Fatalf("unexpected output for no drops: %q", sb.String())
	}
}

// TestStepSummaryDropsOnlyWritesAnchorInvalid: anchor_invalid drops are the
// ones worth surfacing on the run page (structurally sound, just unanchored).
// Other drop reasons are noise on the Actions summary — they are already
// counted in the run record.
func TestStepSummaryDropsOnlyWritesAnchorInvalid(t *testing.T) {
	drops := []model.DropEntry{
		{Path: "a.go", Category: model.CategoryLogicInversion, Title: "Inverted guard", Reason: model.DropAnchorInvalid},
		{Path: "b.go", Category: model.CategoryCrash, Title: "Nil deref", Reason: model.DropAnchorInvalid},
		{Path: "c.go", Category: model.CategoryConvention, Title: "nit", Reason: model.DropSuppressed},
	}
	var sb strings.Builder
	wrote := writeDropSummary(&sb, drops)
	if !wrote {
		t.Fatal("anchor_invalid drops must produce a step summary")
	}
	out := sb.String()
	for _, want := range []string{"a.go", "Inverted guard", "b.go", "Nil deref", "anchor_invalid"} {
		if !strings.Contains(out, want) {
			t.Errorf("step summary missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "nit") || strings.Contains(out, "c.go") {
		t.Errorf("non-anchor_invalid drops must not appear in the step summary:\n%s", out)
	}
}

// TestStepSummaryDropsCapsAndOverflow: more than the cap shows a truncation
// line so the Actions summary stays scannable.
func TestStepSummaryDropsCapsAndOverflow(t *testing.T) {
	drops := make([]model.DropEntry, stepSummaryDropCap+3)
	for i := range drops {
		drops[i] = model.DropEntry{Path: "file.go", Category: model.CategoryCrash, Title: "unanchored", Reason: model.DropAnchorInvalid}
	}
	var sb strings.Builder
	writeDropSummary(&sb, drops)
	out := sb.String()
	if !strings.Contains(out, "more") {
		t.Errorf("overflow drops must mention the remainder:\n%s", out)
	}
}
