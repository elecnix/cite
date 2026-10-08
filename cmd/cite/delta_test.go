package main

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
	"github.com/elecnix/cite/internal/scope"
)

// Only a completed file's SHA is recorded: an errored file must be reviewed
// again next time rather than skipped as unchanged.
func TestCompletedSHAsLeavesErroredFilesOut(t *testing.T) {
	cur := map[string]string{"a.go": "1", "b.go": "2", "c.go": "3", "d.lock": "4"}
	got := completedSHAs([]model.FileOutcome{
		{Path: "a.go", State: model.FileReviewed},
		{Path: "b.go", State: model.FileErrored, Reason: "runaway_generation"},
		{Path: "c.go", State: model.FileSkipped, Reason: scope.SkipReasonUnchanged},
		{Path: "d.lock", State: model.FileSkipped, Reason: scope.SkipReasonLockfile},
	}, cur)
	if got["a.go"] != "1" || got["c.go"] != "3" || got["d.lock"] != "4" {
		t.Fatalf("completed files missing: %v", got)
	}
	if _, ok := got["b.go"]; ok {
		t.Fatal("an errored file was recorded as reviewed")
	}
}

// The sticky keeps every standing finding, carried ones whose thread already
// exists included, and drops what a dismissal or a resolved thread absorbed.
func TestStandingFindingsKeepCarriedOnesAndDropAbsorbedOnes(t *testing.T) {
	fresh := model.ValidatedFinding{Fingerprint: "fresh"}
	carried := model.ValidatedFinding{Fingerprint: "carried"}
	dismissed := model.ValidatedFinding{Fingerprint: "dismissed"}
	resolved := model.ValidatedFinding{Fingerprint: "resolved"}
	plan := publisher.ReconciliationPlan{
		CommentsToPost:     []model.ValidatedFinding{fresh},
		SuppressedByLedger: []model.ValidatedFinding{dismissed},
		MatchedResolved:    []model.ValidatedFinding{resolved},
	}
	got := standingFindings([]model.ValidatedFinding{fresh, carried, dismissed, resolved, carried}, plan)
	if len(got) != 2 || got[0].Fingerprint != "fresh" || got[1].Fingerprint != "carried" {
		t.Fatalf("standing = %+v", got)
	}
}
