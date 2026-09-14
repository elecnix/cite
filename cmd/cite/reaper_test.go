package main

import (
	"testing"

	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/model"
)

// Issue #85: the reaper must honour the repository's tool_failure_blocks
// opt-out, or a repository that opted out of tool failures blocking still
// gets a hard failure from the reaper path. The default concludes failure,
// exactly as before. The opt-out flips the COULD_NOT_EVALUATE conclusion to
// neutral, which branch protection counts as satisfied.
func TestReaperConclusionHonorsToolFailureBlocks(t *testing.T) {
	cases := []struct {
		blocks bool
		want   string
	}{
		{blocks: true, want: "failure"},
		{blocks: false, want: "neutral"},
	}
	for _, tc := range cases {
		outcome := reaperConclusion(tc.blocks)
		if outcome != tc.want {
			t.Fatalf("reaperConclusion(%v) = %q, want %q", tc.blocks, outcome, tc.want)
		}
	}
	if gate.Conclusion(model.VerdictCouldNotEvaluate, gate.Options{}) != "failure" {
		t.Fatalf("gate default conclusion changed; the fail-closed default must stand")
	}
}
