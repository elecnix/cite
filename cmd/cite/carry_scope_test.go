package main

// Repro for issue #47: blocking findings on files outside the pull request
// diff. The incremental carry path (carryIntoRecord) re-armed a previous
// run's findings with Blocks = Category.MayBlock() without re-validating
// them against the CURRENT diff: no manifest-membership check, no anchor or
// evidence re-check. A finding recorded on a file that is no longer part of
// the PR's change (force-push shrank the diff, or the file was reverted)
// reached gate.Decide as blocking — the gate concluded FOUND on a file with
// no diff, exactly the scope leak #47 reports.
//
// The documented contract (docs/noise.md Rule 1, docs/troubleshooting.md) is
// that a finding which does not intersect an added or modified line of the
// change is dropped. A carried finding on an out-of-diff file cannot
// intersect anything: it must be dropped, never re-armed as blocking. A
// carried finding on an in-diff file blocks only when one of its quoted
// evidence lines is an ADDED line of the current diff — the same bar
// validateFindings applies to fresh findings (this also stops run-1
// non-blocking notes from being re-armed as run-2 blockers).

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

func TestCarryIntoRecordDoesNotBlockOnOutOfDiffFile(t *testing.T) {
	rec := &model.RunRecord{}
	prev := &stickyState{
		Findings: []threadFinding{
			{
				Fingerprint: "aaaa1111",
				Path:        "docs/agent-definition.md", // no longer in the PR diff
				Category:    model.CategoryCrash,
				Title:       "stale pre-existing issue",
				Evidence:    []model.Evidence{{Line: 3, Quote: "pre-existing line"}},
			},
		},
	}
	// Current run re-reviews only changed.go; docs/agent-definition.md is
	// not part of this PR's diff at all.
	toReview := []string{"changed.go"}
	manifest := map[string]bool{"changed.go": true}

	carryIntoRecord(rec, prev, toReview, manifest, nil)

	for _, f := range rec.Findings {
		if f.Path == "docs/agent-definition.md" {
			t.Fatalf("carried finding on out-of-diff file %q was re-added to the record", f.Path)
		}
	}
	found := false
	for _, d := range rec.Drops {
		if d.Path == "docs/agent-definition.md" && d.Reason == model.DropAnchorNotAddedLine {
			found = true
		}
	}
	if !found {
		t.Fatalf("out-of-diff carried finding was not dropped with a logged reason (drops=%v)", rec.Drops)
	}
}

// A carried finding on a file still in the diff may block only when one of
// its quoted evidence lines intersects an ADDED line of the current diff —
// the same bar validateFindings applies to fresh findings. Previously the
// carry path re-armed Blocks = Category.MayBlock() unconditionally, which
// also promoted run-1 non-blocking notes to run-2 blockers.
func TestCarryIntoRecordBlockingRequiresAddedLineIntersection(t *testing.T) {
	diffs := map[string]*scope.DiffFile{
		"changed.go": {
			Path: "changed.go",
			Hunks: []*scope.Hunk{
				{
					OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 3,
					Lines: []scope.Line{
						{Kind: scope.LineContext, OldNo: 1, NewNo: 1, Content: "package main"},
						{Kind: scope.LineAdded, NewNo: 2, Content: "new logic line"},
						{Kind: scope.LineContext, OldNo: 2, NewNo: 3, Content: "untouched line"},
					},
				},
			},
		},
	}
	manifest := map[string]bool{"changed.go": true}

	rec := &model.RunRecord{}
	prev := &stickyState{Findings: []threadFinding{
		{
			Fingerprint: "aaaa1111",
			Path:        "changed.go",
			Category:    model.CategoryCrash, // MayBlock
			Title:       "anchored on an added line",
			Evidence:    []model.Evidence{{Line: 2, Quote: "new logic line"}},
		},
		{
			Fingerprint: "bbbb2222",
			Path:        "changed.go",
			Category:    model.CategoryCrash,
			Title:       "anchored on a context line only",
			Evidence:    []model.Evidence{{Line: 3, Quote: "untouched line"}},
		},
	}}
	carryIntoRecord(rec, prev, nil, manifest, diffs)

	byFP := map[string]model.ValidatedFinding{}
	for _, f := range rec.Findings {
		byFP[f.Fingerprint] = f
	}
	if f, ok := byFP["aaaa1111"]; !ok || !f.Blocks {
		t.Fatalf("carried finding anchored on an added line should block: got %+v (ok=%t)", f, ok)
	}
	if f, ok := byFP["bbbb2222"]; !ok || f.Blocks {
		t.Fatalf("carried finding anchored only on a context line must not block: got %+v (ok=%t)", f, ok)
	}

	// Fail closed: with no parsed diff for the file the intersection cannot
	// be checked, so the finding carries as a note, never a blocker.
	rec2 := &model.RunRecord{}
	carryIntoRecord(rec2, prev, nil, manifest, nil)
	for _, f := range rec2.Findings {
		if f.Blocks {
			t.Fatalf("carried finding on file without parsed diff must not block: %+v", f)
		}
	}
}
