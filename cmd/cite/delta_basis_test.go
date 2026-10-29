package main

import (
	"context"
	"errors"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

type fakeComparer struct {
	status string
	err    error
	calls  [][2]string
}

func (f *fakeComparer) CompareStatus(_ context.Context, base, head string) (string, error) {
	f.calls = append(f.calls, [2]string{base, head})
	return f.status, f.err
}

func deltaPrev() *stickyState {
	return &stickyState{
		DeltaSafe:    true,
		HeadSHA:      "head1",
		MergeBaseSHA: "mb1",
		BlobSHAs:     map[string]string{"a.go": "A1", "b.go": "B1"},
	}
}

func deltaEntries() ([]scope.ManifestEntry, map[string]githubclient.FileExtra) {
	return []scope.ManifestEntry{{Status: "M", Path: "a.go"}, {Status: "M", Path: "b.go"}},
		map[string]githubclient.FileExtra{"a.go": {BlobSHA: "A1"}, "b.go": {BlobSHA: "B2"}}
}

// A plain push on the same base: the old head is an ancestor of the new one,
// so the file nobody touched is skipped and the touched file is reviewed.
func TestDeltaBasisHoldsForAFastForwardPush(t *testing.T) {
	cmp := &fakeComparer{status: "ahead"}
	if !deltaBasisHolds(context.Background(), cmp, deltaPrev(), "head2", "mb1") {
		t.Fatal("a fast-forward push on the same merge base must keep the delta mode")
	}
	if len(cmp.calls) != 1 || cmp.calls[0] != [2]string{"head1", "head2"} {
		t.Fatalf("compare calls = %v, want the old head against the new one", cmp.calls)
	}
	entries, extras := deltaEntries()
	got := unchangedFiles(entries, extras, deltaPrev())
	if !got["a.go"] || got["b.go"] || len(got) != 1 {
		t.Fatalf("unchanged = %v, want only a.go", got)
	}
}

// Everything the issue lists as a reason to review in full, plus the cases
// where the state cannot be trusted. Each one must fail toward a full review.
func TestDeltaBasisFallsBackToAFullReview(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*stickyState)
		status    string
		err       error
		mergeBase string
	}{
		{"force push replaced history", nil, "diverged", nil, "mb1"},
		{"reset to an older commit", nil, "behind", nil, "mb1"},
		{"the old head is gone from GitHub", nil, "", errors.New("404"), "mb1"},
		{"the base moved under a rebase", nil, "ahead", nil, "mb2"},
		{"state records no head", func(s *stickyState) { s.HeadSHA = "" }, "ahead", nil, "mb1"},
		{"state records no merge base", func(s *stickyState) { s.MergeBaseSHA = "" }, "ahead", nil, "mb1"},
		{"state predates delta safety", func(s *stickyState) { s.DeltaSafe = false }, "ahead", nil, "mb1"},
		{"the current merge base is unknown", nil, "ahead", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := deltaPrev()
			if tc.mutate != nil {
				tc.mutate(prev)
			}
			cmp := &fakeComparer{status: tc.status, err: tc.err}
			if deltaBasisHolds(context.Background(), cmp, prev, "head2", tc.mergeBase) {
				t.Fatal("delta mode held; it must fall back to a full review")
			}
		})
	}
}

// A missing or unreadable state block parses to an empty state.
func TestDeltaBasisFailsOnAnEmptyState(t *testing.T) {
	if deltaBasisHolds(context.Background(), &fakeComparer{status: "ahead"}, &stickyState{}, "head2", "mb1") {
		t.Fatal("an empty state must never ground a delta review")
	}
}

// The state names which files it covers, so a file the state does not list
// is reviewed, and so is a deleted file's path reappearing as new content.
func TestUnchangedFilesIgnoresUnlistedAndDeletedFiles(t *testing.T) {
	prev := deltaPrev()
	entries := []scope.ManifestEntry{
		{Status: "M", Path: "a.go"}, {Status: "A", Path: "new.go"}, {Status: "D", Path: "b.go"},
	}
	extras := map[string]githubclient.FileExtra{
		"a.go": {BlobSHA: "A1"}, "new.go": {BlobSHA: "N1"}, "b.go": {BlobSHA: "B1"},
	}
	got := unchangedFiles(entries, extras, prev)
	if len(got) != 1 || !got["a.go"] {
		t.Fatalf("unchanged = %v, want only a.go", got)
	}
}

// The verdict of a delta review equals the verdict of a full review of the
// same head: the skipped file still counts toward coverage and the finding
// it carried still holds the block it earned when posted.
func TestDeltaVerdictMatchesAFullReview(t *testing.T) {
	cfg := config.Default()
	entries := []scope.ManifestEntry{{Status: "M", Path: "a.go"}, {Status: "M", Path: "b.go"}}
	diffs := carryDiffs()
	carried := &stickyState{Findings: []threadFinding{{
		Fingerprint: "abcd1234", Path: "a.go", Category: model.CategoryInjection,
		Title: "carried", Evidence: []model.Evidence{{Line: 1, Quote: "new logic"}}, Blocks: true,
	}}}

	full := &model.RunRecord{Files: []model.FileOutcome{
		{Path: "a.go", State: model.FileReviewed}, {Path: "b.go", State: model.FileReviewed},
	}}
	// The full review raises the same finding on a.go that the earlier run did.
	carryIntoRecord(full, carried, []string{"b.go"}, map[string]bool{"a.go": true, "b.go": true}, diffs, carryBlockingSet(cfg))

	delta := &model.RunRecord{Files: []model.FileOutcome{
		{Path: "a.go", State: model.FileSkipped, Reason: scope.SkipReasonUnchanged},
		{Path: "b.go", State: model.FileReviewed},
	}}
	carryIntoRecord(delta, carried, []string{"b.go"}, map[string]bool{"a.go": true, "b.go": true}, diffs, carryBlockingSet(cfg))

	for _, rec := range []*model.RunRecord{full, delta} {
		rec.Coverage = scope.ComputeCoverage(rec.Files, len(entries))
	}
	fv, fr := gate.Decide(full, cfg, gate.Options{})
	dv, dr := gate.Decide(delta, cfg, gate.Options{})
	if fv != dv || fr != dr {
		t.Fatalf("delta verdict %s (%s) differs from full verdict %s (%s)", dv, dr, fv, fr)
	}
	if dv != model.VerdictFound {
		t.Fatalf("verdict = %s, want FOUND from the carried blocking finding", dv)
	}
}
