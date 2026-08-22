package scope

import (
	"fmt"
	"sort"
)

// RiskRankCutoff is the flagged-file count above which Cite risk-ranks
// instead of reviewing everything (§7).
const RiskRankCutoff = 40

// RankForReview orders entries by added source lines, descending, and splits
// them at limit: the returned first slice is what gets reviewed; the second
// slice is everything below the cutoff.
//
// The cutoff is never applied silently (§7): the caller records every cut
// entry as skipped(reason="risk_rank_cutoff") and appends RiskRankedNote to
// the review body. Ties break by path so the ranking is deterministic — a
// re-run must rank identically or the coverage footer lies.
func RankForReview(entries []ManifestEntry, limit int) (review, cut []ManifestEntry) {
	sorted := make([]ManifestEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Adds != sorted[j].Adds {
			return sorted[i].Adds > sorted[j].Adds
		}
		return sorted[i].Path < sorted[j].Path
	})
	if limit < 0 {
		limit = 0
	}
	if limit >= len(sorted) {
		return sorted, nil
	}
	return sorted[:limit], sorted[limit:]
}

// ShouldRiskRank reports whether this manifest trips the >40 flagged-files
// rule. It counts only files that would otherwise be reviewed: entries with
// added source lines. Pure deletions and renames carry no new code to rank.
func ShouldRiskRank(entries []ManifestEntry) bool {
	n := 0
	for _, e := range entries {
		if e.Adds > 0 {
			n++
		}
	}
	return n > RiskRankCutoff
}

// RiskRankedNote renders the one-line coverage footer (§7). Never silent:
// this line is the difference between a scoped review and a review that
// looks complete.
func RiskRankedNote(reviewed, total int) string {
	return fmt.Sprintf("Risk-ranked: reviewed the top %d of %d flagged files by added source lines; the remaining %d were recorded as skipped(risk_rank_cutoff).", reviewed, total, total-reviewed)
}
