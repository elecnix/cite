package main

import (
	"testing"

	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/model"
)

// tool_failure_blocks: false concludes a COULD_NOT_EVALUATE check run
// neutral; the process must then exit zero too, or the job that ran it is
// the red check that blocks the merge. A finding always fails.
func TestGateExit(t *testing.T) {
	neutral := gate.Options{NeutralToolFailure: true}
	cases := []struct {
		v       model.Verdict
		opts    gate.Options
		wantErr bool
	}{
		{model.VerdictPass, gate.Options{}, false},
		{model.VerdictCouldNotEvaluate, gate.Options{}, true},
		{model.VerdictCouldNotEvaluate, neutral, false},
		{model.VerdictFound, neutral, true},
		{model.VerdictFound, gate.Options{}, true},
	}
	for _, tc := range cases {
		if err := gateExit(tc.v, tc.opts); (err != nil) != tc.wantErr {
			t.Errorf("gateExit(%s, neutral=%v) = %v, want error=%v", tc.v, tc.opts.NeutralToolFailure, err, tc.wantErr)
		}
	}
}
