package main

import (
	"io"
	"os"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// duplicatedModelConfig declares the same model id under two providers with
// different rates. Two providers legitimately disagree about what a shared
// model id costs: one routes through a paid gateway, the other is a local
// endpoint that bills nothing.
func duplicatedModelConfig() *config.Config {
	entry := func(in, out float64) model.ModelEntry {
		return model.ModelEntry{ID: "openai/gpt-4o-mini", Cost: &model.Cost{Input: in, Output: out}}
	}
	return &config.Config{
		Providers: map[string]*model.Provider{
			"gateway": {Name: "gateway", Models: []model.ModelEntry{entry(5, 30)}},
			"local":   {Name: "local", Models: []model.ModelEntry{entry(0, 0)}},
		},
	}
}

// TestCostRatesForIsDeterministic pins the invariant that one model id and one
// config always resolve to one set of rates. The lookup scans a Go map, whose
// iteration order is randomised per range statement, so a model id declared
// under two providers resolved to whichever provider the runtime happened to
// visit first. The same run, replayed, could report a cost thirty times larger
// than the previous one.
func TestCostRatesForIsDeterministic(t *testing.T) {
	cfg := duplicatedModelConfig()
	seen := map[float64]bool{}
	for i := 0; i < 200; i++ {
		rate := costRatesFor("openai/gpt-4o-mini", cfg)
		if rate == nil {
			t.Fatal("costRatesFor returned no rates for a declared model")
		}
		seen[rate.Input] = true
	}
	if len(seen) > 1 {
		t.Errorf("costRatesFor resolved one model id to %d different rate sets %v; a cost report must not depend on map iteration order", len(seen), seen)
	}
}

// TestCostRatesForPrefersTheQualifiedReference checks the resolution order. A
// provider-qualified reference ("gateway/openai/gpt-4o-mini") names the
// provider, so its rates win over a bare id that any provider may claim.
func TestCostRatesForPrefersTheQualifiedReference(t *testing.T) {
	cfg := duplicatedModelConfig()
	for i := 0; i < 200; i++ {
		rate := costRatesFor("gateway/openai/gpt-4o-mini", cfg)
		if rate == nil {
			t.Fatal("costRatesFor returned no rates for a declared model")
		}
		if rate.Input != 5 {
			t.Fatalf("costRatesFor(\"gateway/openai/gpt-4o-mini\").Input = %v, want 5 (the named provider's rates)", rate.Input)
		}
	}
}

// captureStderr collects everything a function writes to os.Stderr. The cost
// report is printed on stdout by printRecord, which cannot be reached from
// here, so the mismatch note is written where every other Cite diagnostic goes.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = saved
	w.Close()
	out := <-done
	r.Close()
	return out
}

// TestApplyCostWarnsWhenTheRunModelHasNoRates covers the silent zero. The model
// client reads MODEL_ID from the environment and never consults the providers
// block, so a repository whose config declares one set of models can easily run
// a model that block never declared. applyCost found no rates and returned 0,
// and the run then printed "cost: $0.0000" as though zero were the price.
func TestApplyCostWarnsWhenTheRunModelHasNoRates(t *testing.T) {
	rec := &model.RunRecord{Model: "openai/gpt-5-mini",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000}}
	out := captureStderr(t, func() { applyCost(rec, testCostConfig()) })
	if rec.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for a model with no declared rates", rec.CostUSD)
	}
	if out == "" {
		t.Fatal("applyCost reported no cost and said nothing; an absent rate is not a price of zero")
	}
}

// TestApplyCostStaysQuietWhenNoProvidersDeclared keeps the note narrow. With no
// providers block there are no declared rates anywhere, so a zero cost is the
// documented outcome and warning about it on every run would be noise.
func TestApplyCostStaysQuietWhenNoProvidersDeclared(t *testing.T) {
	rec := &model.RunRecord{Model: "openai/gpt-4o-mini",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000}}
	out := captureStderr(t, func() { applyCost(rec, &config.Config{}) })
	if out != "" {
		t.Errorf("applyCost wrote %q with no providers declared; want silence", out)
	}
}

// TestApplyCostStaysQuietWhenRatesResolve keeps the note narrow in the other
// direction: a run whose model is declared and priced says nothing.
func TestApplyCostStaysQuietWhenRatesResolve(t *testing.T) {
	rec := &model.RunRecord{Model: "gateway/vendor/model-x",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000}}
	out := captureStderr(t, func() { applyCost(rec, testCostConfig()) })
	if rec.CostUSD == 0 {
		t.Fatalf("CostUSD = 0 for a declared, priced model")
	}
	if out != "" {
		t.Errorf("applyCost wrote %q for a run whose rates resolved; want silence", out)
	}
}
