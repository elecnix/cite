package main

import (
	"strconv"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// applyCost stores the run's USD cost on the record. A provider that bills
// per call (OpenRouter's usage.cost) reports what it charged, and that sum is
// the cost (issue #103). Otherwise the cost comes from the declared
// per-million rates (§6), so cost reporting works for a model Cite has never
// heard of. A model with neither costs 0, never a guessed number (§15).
func applyCost(rec *model.RunRecord, cfg *config.Config) {
	if rec == nil {
		return
	}
	if rec.Usage.CostReported {
		rec.CostUSD = rec.Usage.CostUSD
		return
	}
	rate := costRatesFor(rec.Model, cfg)
	if rate == nil {
		warnMissingRates(rec.Model, cfg)
		return
	}
	u := rec.Usage
	rec.CostUSD = float64(u.InputTokens)/1e6*rate.Input +
		float64(u.OutputTokens)/1e6*rate.Output +
		float64(u.CacheReadTokens)/1e6*rate.CacheRead +
		float64(u.CacheWriteTokens)/1e6*rate.CacheWrite
}

// warnMissingRates says out loud that a run's cost is unknown. The model
// client reads MODEL_ID from the environment and never consults the providers
// block, so the model a run actually used is frequently one that no provider
// declared. applyCost then has no rates to apply, and printRecord goes on to
// print "cost: $0.0000" as though the run had been free. A declared rate of
// zero is a price; the absence of a rate is not, so the difference is reported
// rather than folded into the number (§15).
//
// The note stays silent when no provider declares rates at all: there is
// nothing to be mismatched against, and a zero cost is then the documented
// outcome rather than a silent surprise.
func warnMissingRates(modelID string, cfg *config.Config) {
	if !declaresAnyRates(cfg) {
		return
	}
	logToStderr("cost: no declared rates for model %q: this run's cost is unknown, not zero. Declare the model under a provider, or set MODEL_ID to a declared model.", modelID)
}

// declaresAnyRates reports whether any declared model carries a cost block.
func declaresAnyRates(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, p := range cfg.Providers {
		if p == nil {
			continue
		}
		for _, m := range p.Models {
			if m.Cost != nil {
				return true
			}
		}
	}
	return false
}

// costRatesFor finds the Cost rates for a model id among the declared
// providers.
//
// The record may carry either the bare id ("vendor/model-x") or the
// provider-qualified reference ("gateway/vendor/model-x"), so there are two
// passes. A qualified reference names its provider and is resolved exactly
// against that provider first: two providers may legitimately declare the same
// vendor-qualified id at different prices, and the reference says which one the
// run used. Only then does a bare id fall back to the trailing segment, matched
// across every provider in sorted order — sorted, because the providers map has
// no order of its own, and an unsorted scan made the resolved rate depend on
// Go's randomised map iteration.
func costRatesFor(modelID string, cfg *config.Config) *model.Cost {
	if modelID == "" || cfg == nil {
		return nil
	}
	if provider, id, ok := config.SplitModelRef(modelID); ok {
		if p := cfg.Providers[provider]; p != nil {
			for _, m := range p.Models {
				if m.ID == id {
					return m.Cost
				}
			}
		}
	}
	short := trailingID(modelID)
	if short == "" {
		return nil
	}
	for _, name := range cfg.ProviderNames() {
		p := cfg.Providers[name]
		if p == nil {
			continue
		}
		for _, m := range p.Models {
			if m.Cost != nil && trailingID(m.ID) == short {
				return m.Cost
			}
		}
	}
	return nil
}

// trailingID reduces a model reference to its last path segment, so that a bare
// id ("model-x") and a vendor-qualified one ("vendor/model-x") meet. An entry
// id without '/' matches only itself.
func trailingID(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// humanTokens renders a token count compactly for the cost line:
// 45000 -> "45k", 2000 -> "2k", 750 -> "750".
func humanTokens(n int) string {
	if n >= 1000 {
		k := n / 1000
		rem := n % 1000
		if rem == 0 {
			return strconv.Itoa(k) + "k"
		}
		tenths := (rem + 50) / 100 // one decimal, nearest
		if tenths >= 10 {
			return strconv.Itoa(k+1) + "k"
		}
		return strconv.Itoa(k) + "." + strconv.Itoa(tenths) + "k"
	}
	return strconv.Itoa(n)
}
