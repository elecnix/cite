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

// Issue #204: a run whose provider reports tokens but no cost, in a config
// that declares no rates, must not print "$0.0000". The zero claims the run
// was free, and the run was billed.
func TestPrintRecordShowsUnknownCost(t *testing.T) {
	rec := &model.RunRecord{
		Model:       "ollama/deepseek-v4",
		Coverage:    model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
		Usage:       model.Usage{InputTokens: 45_000, OutputTokens: 2_000},
		CostUnknown: true,
	}
	out := captureStdout(t, func() { printRecord(rec) })
	if !strings.Contains(out, "cost: unknown") {
		t.Errorf("run printout missing the unknown cost line:\n%s", out)
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("run printout claims an unknown cost was zero:\n%s", out)
	}
}

// A provider-reported zero is a price, not an absence: a free model costs
// nothing, and the run says so.
func TestPrintRecordShowsBilledZeroCost(t *testing.T) {
	rec := &model.RunRecord{
		Model:    "openrouter/vendor/free-model",
		Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
		Usage:    model.Usage{InputTokens: 45_000, OutputTokens: 2_000, CostReported: true},
	}
	out := captureStdout(t, func() { printRecord(rec) })
	if !strings.Contains(out, "cost: $0.0000") {
		t.Errorf("run printout missing the billed zero cost line:\n%s", out)
	}
	if strings.Contains(out, "cost: unknown") {
		t.Errorf("a provider-reported zero must not read as unknown:\n%s", out)
	}
}

// A resolved declared rate prints its number, unchanged.
func TestPrintRecordShowsDeclaredRateCost(t *testing.T) {
	rec := &model.RunRecord{
		Model:    "gateway/vendor/model-x",
		Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
		Usage:    model.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		CostUSD:  35,
	}
	out := captureStdout(t, func() { printRecord(rec) })
	if !strings.Contains(out, "cost: $35.0000") {
		t.Errorf("run printout missing the declared rate cost line:\n%s", out)
	}
}

// The published summary is the pinned comment a human reads in the thread.
// An unknown cost there must read as unknown, not as a zero.
func TestStickyVisibleBodyShowsUnknownCost(t *testing.T) {
	rec := &model.RunRecord{
		Model:       "ollama/deepseek-v4",
		Coverage:    model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
		Samples:     1,
		Usage:       model.Usage{InputTokens: 45_000, OutputTokens: 2_000},
		CostUnknown: true,
	}
	out := stickyVisibleBody(rec)
	if !strings.Contains(out, "cost: unknown") {
		t.Errorf("sticky body missing the unknown cost line:\n%s", out)
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("sticky body claims an unknown cost was zero:\n%s", out)
	}
}

// A provider-reported zero stays a number in the published summary too: a
// free model cost nothing, and that is a fact, not an absence.
func TestStickyVisibleBodyShowsBilledZeroCost(t *testing.T) {
	rec := &model.RunRecord{
		Model:    "openrouter/vendor/free-model",
		Coverage: model.Coverage{APIFiles: 1, Reviewed: 1, Complete: true},
		Samples:  1,
		Usage:    model.Usage{InputTokens: 45_000, OutputTokens: 2_000, CostReported: true},
	}
	out := stickyVisibleBody(rec)
	if !strings.Contains(out, "cost: $0.0000") {
		t.Errorf("sticky body missing the billed zero cost line:\n%s", out)
	}
}
