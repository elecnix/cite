package main

// Repro for issue #47: blocking findings on files outside the pull request
// diff. The incremental carry path (carryIntoRecord) re-arms a previous
// run's findings with Blocks = Category.MayBlock() without re-validating
// them against the CURRENT diff: no manifest-membership check, no anchor or
// evidence re-check. A finding recorded on a file that is no longer part of
// the PR's change (force-push shrank the diff, or the file was reverted)
// reaches gate.Decide as blocking — the gate concludes FOUND on a file with
// no diff, exactly the scope leak #47 reports.
//
// The documented contract (docs/noise.md Rule 1, docs/troubleshooting.md) is
// that a finding which does not intersect an added or modified line of the
// change is dropped. A carried finding on an out-of-diff file cannot
// intersect anything: it must be dropped, never re-armed as blocking.

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
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

	carryIntoRecord(rec, prev, toReview)

	for _, f := range rec.Findings {
		if f.Path == "docs/agent-definition.md" && f.Blocks {
			t.Fatalf("carried finding on out-of-diff file %q blocks the gate (issue #47 scope leak)", f.Path)
		}
	}
}
