package main

import (
	"os"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// captureStdout collects what a function writes to os.Stdout. The run
// printout goes to stdout, so this is how a test reads the cost line a
// human sees in the CI log.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b := make([]byte, 4096)
		var got strings.Builder
		for {
			n, err := r.Read(b)
			if n > 0 {
				got.Write(b[:n])
			}
			if err != nil {
				break
			}
		}
		done <- got.String()
	}()
	fn()
	os.Stdout = saved
	w.Close()
	out := <-done
	r.Close()
	return out
}

// costLineCase is one shape of run and the cost line it must produce. The
// three shapes are the ones issue #204 separates, and the two surfaces
// below read the same case table so they cannot drift apart.
type costLineCase struct {
	name string
	rec  *model.RunRecord
	// want is the cost line the surface must print.
	want string
	// forbid is a cost line the surface must not print.
	forbid string
}

// The cost cases, shared by the stdout summary and the published summary.
func costLineCases() []costLineCase {
	return []costLineCase{
		{
			// Issue #204: a run whose provider reports tokens but no
			// cost, in a config that declares no rates, must not print
			// "$0.0000". The zero claims the run was free, and the run
			// was billed.
			name: "unpriced run",
			rec: &model.RunRecord{
				Model:       "ollama/deepseek-v4",
				Coverage:    model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
				Usage:       model.Usage{InputTokens: 45_000, OutputTokens: 2_000},
				CostUnknown: true,
			},
			want:   "cost: unknown",
			forbid: "$0.0000",
		},
		{
			// A provider-reported zero is a price, not an absence: a
			// free model costs nothing, and the run says so.
			name: "billed zero",
			rec: &model.RunRecord{
				Model:    "openrouter/vendor/free-model",
				Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
				Usage:    model.Usage{InputTokens: 45_000, OutputTokens: 2_000, CostReported: true},
			},
			want:   "cost: $0.0000",
			forbid: "cost: unknown",
		},
		{
			// A resolved declared rate prints its number, unchanged.
			name: "declared rate",
			rec: &model.RunRecord{
				Model:    "gateway/vendor/model-x",
				Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
				Usage:    model.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
				CostUSD:  35,
			},
			want:   "cost: $35.0000",
			forbid: "cost: unknown",
		},
	}
}

// The stdout summary is the run printout a human reads in the CI log.
func TestPrintRecordCostLine(t *testing.T) {
	for _, tc := range costLineCases() {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() { printRecord(tc.rec) })
			if !strings.Contains(out, tc.want) {
				t.Errorf("run printout missing %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.forbid) {
				t.Errorf("run printout contains %q, which it must not:\n%s", tc.forbid, out)
			}
		})
	}
}

// The published summary is the pinned comment a human reads in the thread.
// It must say the same thing about the cost as the stdout summary does.
func TestStickyVisibleBodyCostLine(t *testing.T) {
	for _, tc := range costLineCases() {
		t.Run(tc.name, func(t *testing.T) {
			rec := *tc.rec
			rec.Samples = 1
			out := stickyVisibleBody(&rec)
			if !strings.Contains(out, tc.want) {
				t.Errorf("sticky body missing %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.forbid) {
				t.Errorf("sticky body contains %q, which it must not:\n%s", tc.forbid, out)
			}
		})
	}
}
