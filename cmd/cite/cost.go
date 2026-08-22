package main

import (
	"strconv"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// applyCost computes the run's USD cost from the declared per-million rates
// (§6) and stores it on the record. Cost as first-class configuration means
// cost reporting works for a model Cite has never heard of — and that a model
// with no declared rates costs 0, never a guessed number (§15).
func applyCost(rec *model.RunRecord, cfg *config.Config) {
	if rec == nil {
		return
	}
	rate := costRatesFor(rec.Model, cfg)
	if rate == nil {
		return
	}
	u := rec.Usage
	rec.CostUSD = float64(u.InputTokens)/1e6*rate.Input +
		float64(u.OutputTokens)/1e6*rate.Output +
		float64(u.CacheReadTokens)/1e6*rate.CacheRead +
		float64(u.CacheWriteTokens)/1e6*rate.CacheWrite
}

// costRatesFor finds the Cost rates for a model id by scanning the declared
// providers. The record may carry either the bare id ("vendor/model-x") or
// the provider-qualified reference ("gateway/vendor/model-x"), so matching is
// on the suffix after the last '/'; a bare entry id without '/' matches only
// itself.
func costRatesFor(modelID string, cfg *config.Config) *model.Cost {
	if modelID == "" || cfg == nil {
		return nil
	}
	short := modelID
	if i := strings.LastIndex(short, "/"); i >= 0 {
		short = short[i+1:]
	}
	if short == "" {
		return nil
	}
	for _, p := range cfg.Providers {
		if p == nil {
			continue
		}
		for _, m := range p.Models {
			id := m.ID
			if j := strings.LastIndex(id, "/"); j >= 0 {
				id = id[j+1:]
			}
			if id == short && m.Cost != nil {
				return m.Cost
			}
		}
	}
	return nil
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
