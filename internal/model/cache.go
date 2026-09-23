package model

import (
	"fmt"
	"sort"
)

// MinCeilingFraction is the advisory line for prompt caching (§7): a run
// whose measured hit rate falls below this fraction of its own ceiling is
// missing the cache on a prefix it could have read. The check is relative
// because the ceiling moves with the pull request: the per-file payload is
// never cacheable, so a pull request of large files has a low ceiling and a
// flat floor would call a healthy run a regression.
const MinCeilingFraction = 0.8

// CacheCeiling is the best cache hit rate a run's calls could have reached,
// given the prompts Cite actually sent. Calls are grouped by PrefixID: the
// first call of a group (by start time) pays the cache write and reads
// nothing, and every later call of the group can read its prefix and no
// more. A response reports one prompt-token total per call, so each call's
// cacheable tokens are estimated as InputTokens × PrefixBytes / PromptBytes.
//
// Calls that report no input tokens (failed calls) neither count nor warm a
// group. Reuse across runs is not modelled: a prefix cached by an earlier
// run within the provider's retention window can push the measured rate
// above this ceiling, which is a bonus, not a fault. Zero input gives zero.
func CacheCeiling(calls []CallEntry) float64 {
	ordered := make([]CallEntry, len(calls))
	copy(ordered, calls)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartS < ordered[j].StartS })

	seen := map[string]bool{}
	var input, cacheable float64
	for _, c := range ordered {
		if c.InputTokens <= 0 {
			continue
		}
		input += float64(c.InputTokens)
		if c.PrefixID == "" || c.PromptBytes <= 0 {
			continue
		}
		if seen[c.PrefixID] {
			share := float64(c.PrefixBytes) / float64(c.PromptBytes)
			if share > 1 {
				share = 1
			}
			cacheable += float64(c.InputTokens) * share
		}
		seen[c.PrefixID] = true
	}
	if input == 0 {
		return 0
	}
	return cacheable / input
}

// CacheShortfall reports whether the measured hit rate fell below
// MinCeilingFraction of the ceiling. It is advisory: the run prints it, and
// nothing fails on it. With no measured ceiling there is nothing to fall
// short of.
func CacheShortfall(rate, ceiling float64) bool {
	if ceiling <= 0 {
		return false
	}
	return rate < MinCeilingFraction*ceiling
}

// CacheSummary renders the measured hit rate against the ceiling in one
// phrase, for the check-run summary and the run printout. A record without a
// ceiling (no calls logged) renders the rate alone.
func CacheSummary(rate, ceiling float64) string {
	s := fmt.Sprintf("%d%% cache-hit", int(100*rate))
	if ceiling <= 0 {
		return s
	}
	s += fmt.Sprintf(" of a %d%% ceiling", int(100*ceiling))
	if CacheShortfall(rate, ceiling) {
		s += fmt.Sprintf(" (below %d%% of the ceiling, advisory)", int(100*MinCeilingFraction))
	}
	return s
}
