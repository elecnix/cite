package main

// The agreement test between the two paths that decide whether a finding
// blocks a merge. Fresh findings are decided in internal/reviewer by
// model.ComputeBlocks, the §8 formula; carried findings keep the verdict the
// sticky comment recorded when they were posted, re-checked against the
// repository's blocking set and the current diff (issue #153).

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
	return carryWith(t, cat, line, true, carryBlockingSet(nil))
}

func carryWith(t *testing.T, cat model.Category, line int, blockedWhenPosted bool, set map[model.Category]bool) model.ValidatedFinding {
	t.Helper()
	rec := &model.RunRecord{}
	prev := &stickyState{Findings: []threadFinding{{
		Fingerprint: "abcd1234",
		Path:        "a.go",
		Category:    cat,
		Title:       "carried finding",
		Evidence:    []model.Evidence{{Line: line, Quote: "new logic"}},
		Blocks:      blockedWhenPosted,
	}}}
	carryIntoRecord(rec, prev, nil, map[string]bool{"a.go": true}, carryDiffs(), set)
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

// Issue #153: a finding that did not block when it was posted (likely or
// question confidence, or a verifier that did not support it) never starts
// blocking through carry-forward, and a category the repository removed from
// its blocking set stops blocking even when it blocked before.
func TestCarriedFindingKeepsOnlyTheBlockItEarned(t *testing.T) {
	if f := carryWith(t, model.CategoryInjection, 1, false, carryBlockingSet(nil)); f.Blocks {
		t.Fatal("a finding posted as a note blocks after carry-forward")
	}
	shrunk := map[model.Category]bool{model.CategoryCrash: true}
	if f := carryWith(t, model.CategoryInjection, 1, true, shrunk); f.Blocks {
		t.Fatal("a category outside the repository's blocking set blocks after carry-forward")
	}
	f := carryWith(t, model.CategoryInjection, 1, true, carryBlockingSet(nil))
	if !f.Blocks {
		t.Fatal("a finding that blocked when posted, still on an added line, must keep blocking")
	}
	rec := &model.RunRecord{Findings: []model.ValidatedFinding{carryWith(t, model.CategoryInjection, 1, false, carryBlockingSet(nil))}}
	if verdict, reason := gate.Decide(rec, nil, gate.Options{}); verdict == model.VerdictFound {
		t.Fatalf("gate concluded FOUND on a carried note: %s", reason)
	}
}
