package main

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// The run printout names the upstream providers that served the calls: each
// keeps its own prompt cache, so a run spread across several cannot reach its
// ceiling, and sticky routing should keep this to one name.
func TestUpstreamSuffixCountsCallsPerProvider(t *testing.T) {
	calls := []model.CallEntry{{Provider: "Wafer"}, {Provider: "DeepInfra"}, {Provider: "Wafer"}, {}}
	if got, want := upstreamSuffix(calls), "; upstream: DeepInfra×1, Wafer×2"; got != want {
		t.Fatalf("upstreamSuffix = %q, want %q", got, want)
	}
	if got := upstreamSuffix([]model.CallEntry{{}}); got != "" {
		t.Fatalf("no provider reported ⇒ no suffix, got %q", got)
	}
}
