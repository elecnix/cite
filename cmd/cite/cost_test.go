package main

import (
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

func testCostConfig() *config.Config {
	return &config.Config{
		Providers: map[string]*model.Provider{
			"gateway": {
				Name: "gateway",
				Models: []model.ModelEntry{
					{ID: "vendor/model-x", Cost: &model.Cost{
						Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25,
					}},
					{ID: "cheap", Cost: &model.Cost{Input: 1, Output: 2}},
				},
			},
			"other": {
				Name: "other",
				Models: []model.ModelEntry{
					// A model with an ID but no declared rates.
					{ID: "backup-model"},
				},
			},
		},
	}
}

func TestApplyCostProviderQualifiedReference(t *testing.T) {
	rec := &model.RunRecord{
		Model: "gateway/vendor/model-x",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000,
			CacheReadTokens: 10_000, CacheWriteTokens: 1_000},
	}
	applyCost(rec, testCostConfig())
	// 45k/1e6*5 + 2k/1e6*30 + 10k/1e6*0.5 + 1k/1e6*6.25 = 0.29625
	const want = 0.29625
	if diff := rec.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CostUSD = %v, want %v", rec.CostUSD, want)
	}
}

func TestApplyCostBareIDMatchesSuffixedEntry(t *testing.T) {
	// The record carries the bare id; the entry is provider-qualified.
	rec := &model.RunRecord{Model: "model-x",
		Usage: model.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	applyCost(rec, testCostConfig())
	if rec.CostUSD != 35 {
		t.Errorf("CostUSD = %v, want 35 (1M @ $5 + 1M @ $30)", rec.CostUSD)
	}
}

func TestApplyCostBareEntryID(t *testing.T) {
	rec := &model.RunRecord{Model: "gateway/cheap",
		Usage: model.Usage{InputTokens: 500_000, OutputTokens: 250_000}}
	applyCost(rec, testCostConfig())
	if rec.CostUSD != 1.0 {
		t.Errorf("CostUSD = %v, want 1.0 (0.5M @ $1 + 0.25M @ $2)", rec.CostUSD)
	}
}

func TestApplyCostMissingRatesIsZero(t *testing.T) {
	// Declared model with no cost block: usage stays recorded, cost stays 0.
	rec := &model.RunRecord{Model: "other/backup-model",
		Usage: model.Usage{InputTokens: 999_999, OutputTokens: 9_999}}
	applyCost(rec, testCostConfig())
	if rec.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for a model with no declared rates", rec.CostUSD)
	}
}

func TestApplyCostUnknownModelIsZero(t *testing.T) {
	rec := &model.RunRecord{Model: "nowhere/gpt-unknown",
		Usage: model.Usage{InputTokens: 123, OutputTokens: 456}}
	applyCost(rec, testCostConfig())
	if rec.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for an undeclared model", rec.CostUSD)
	}
}

func TestApplyCostNilConfigAndEmptyModel(t *testing.T) {
	rec := &model.RunRecord{Model: "x/y", Usage: model.Usage{InputTokens: 1}}
	applyCost(rec, nil)
	if rec.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 with nil config", rec.CostUSD)
	}
	empty := &model.RunRecord{}
	applyCost(empty, testCostConfig())
	if empty.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 with empty model id", empty.CostUSD)
	}
}

func TestApplyCostNilRecordNoPanic(t *testing.T) {
	applyCost(nil, testCostConfig()) // must not panic
}

func TestHumanTokens(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{750, "750"},
		{2000, "2k"},
		{45000, "45k"},
		{45600, "45.6k"},
		{999500, "999.5k"}, // one decimal, nearest
		{999950, "1000k"},  // rounds up to the next thousand
	}
	for _, c := range cases {
		if got := humanTokens(c.n); got != c.want {
			t.Errorf("humanTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
