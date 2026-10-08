package main

// The agreement test between the two paths that decide whether a finding
// blocks a merge. Fresh findings are decided in internal/reviewer by
// model.ComputeBlocks, the §8 formula; carried findings are re-derived here
// from the sticky comment. Two copies of one policy are one copy too many,
// so this file pins the conjuncts the two paths decide together, and names
// the ones they cannot: the repository's blocking_categories and the
// confidence bar have no input on the carry path, because the sticky state
// records only what is needed to match a live thread.

import (
	"testing"

	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

func carryDiffs() map[string]*scope.DiffFile {
	return map[string]*scope.DiffFile{
		"a.go": {
			Path: "a.go",
			Hunks: []*scope.Hunk{{
				OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 3,
				Lines: []scope.Line{
					{Kind: scope.LineAdded, NewNo: 1, Content: "new logic"},
					{Kind: scope.LineContext, NewNo: 2, Content: "untouched line"},
				},
			}},
		},
	}
}

func carryOne(t *testing.T, cat model.Category, line int) model.ValidatedFinding {
	t.Helper()
	rec := &model.RunRecord{}
	prev := &stickyState{Findings: []threadFinding{{
		Fingerprint: "abcd1234",
		Path:        "a.go",
		Category:    cat,
		Title:       "carried finding",
		Evidence:    []model.Evidence{{Line: line, Quote: "new logic"}},
	}}}
	carryIntoRecord(rec, prev, nil, map[string]bool{"a.go": true}, carryDiffs())
	if len(rec.Findings) != 1 {
		t.Fatalf("expected exactly one carried finding, got %d", len(rec.Findings))
	}
	return rec.Findings[0]
}

// Where the two paths can both decide, they agree. A category that may not
// block never blocks through carry-forward, and neither does a finding whose
// quoted line is not an added line of the current diff.
func TestCarriedPathAgreesWithFreshPathOnCategoryAndAnchor(t *testing.T) {
	cases := []struct {
		name      string
		category  model.Category
		line      int
		wantBlock bool
	}{
		{"blocking category on an added line", model.CategoryInjection, 1, true},
		{"blocking category on a context line", model.CategoryInjection, 2, false},
		{"non-blocking category on an added line", model.CategoryConcurrency, 1, false},
		{"convention on an added line", model.CategoryConvention, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := carryOne(t, c.category, c.line)
			if f.Blocks != c.wantBlock {
				t.Fatalf("carried %s finding at line %d: Blocks=%t, want %t", c.category, c.line, f.Blocks, c.wantBlock)
			}
			// The fresh path reaches the same answer for the same shape,
			// with every remaining conjunct established.
			fresh := model.BlockInputs{
				Category: c.category,
				BlockingSet: map[model.Category]bool{
					model.CategorySecretExposure: true, model.CategoryInjection: true,
					model.CategoryAuthBypass: true, model.CategoryDestructiveOp: true,
					model.CategoryCrash: true, model.CategoryLogicInversion: true,
				},
				EvidenceMatches:        true,
				AnchorOnAddedLine:      c.line == 1,
				ClaimsVerified:         true,
				NegativeClaimsVerified: true,
				Confidence:             model.ConfidenceCertain,
				VerifierSupported:      true,
			}
			if got := model.ComputeBlocks(fresh); got != c.wantBlock {
				t.Fatalf("fresh path disagrees with the carried path for %s: ComputeBlocks=%t, carried Blocks=%t", c.name, got, f.Blocks)
			}
		})
	}
}

// The divergence, pinned. A carried finding is recorded as likely because
// nothing re-established certainty this run, and the confidence bar of §8
// is not an input on this path, so it still blocks. gate.Decide trusts the
// Blocks flag, which is what makes the divergence reach the verdict: a
// finding the fresh path would have refused to block concludes the run as
// FOUND on the second push, when its file was not re-reviewed.
func TestCarriedFindingBlocksBelowTheConfidenceBar(t *testing.T) {
	f := carryOne(t, model.CategoryInjection, 1)
	if f.Confidence != model.ConfidenceLikely {
		t.Fatalf("carried finding confidence = %q, want %q: the sticky state records no confidence, so the path writes the one it can justify", f.Confidence, model.ConfidenceLikely)
	}
	if !f.Blocks {
		t.Fatal("carried finding no longer blocks; if this is the intended repair, update this test and the carryIntoRecord comment together")
	}

	// The same finding through the shared formula, with every other conjunct
	// established, is not a blocking finding.
	shared := model.ComputeBlocks(model.BlockInputs{
		Category:               f.Category,
		BlockingSet:            map[model.Category]bool{f.Category: true},
		EvidenceMatches:        true,
		AnchorOnAddedLine:      true,
		ClaimsVerified:         true,
		NegativeClaimsVerified: true,
		Confidence:             f.Confidence,
		VerifierSupported:      true,
	})
	if shared {
		t.Fatalf("model.ComputeBlocks blocked a %q finding; the carried path and the shared formula now agree", f.Confidence)
	}

	rec := &model.RunRecord{Findings: []model.ValidatedFinding{f}}
	verdict, reason := gate.Decide(rec, nil, gate.Options{})
	if verdict != model.VerdictFound {
		t.Fatalf("gate verdict = %s (%s); the gate reads the Blocks flag rather than re-deriving it", verdict, reason)
	}
}
