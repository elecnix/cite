// Package publisher holds the pure core of the publishing pipeline (§10):
// comment budgets, fingerprint reconciliation, the dismissal ledger,
// incremental re-review bookkeeping and the review-body builder.
//
// This file implements the noise contract's budget (§13.3): a budget, not a
// threshold, with a hard per-file cap. Numbers live here so docs/noise.md and
// the code cannot drift apart silently.
package publisher

import (
	"fmt"

	"github.com/elecnix/cite/internal/model"
)

// Budget constants (§13.3).
const (
	// MinCommentBudget: a 40-line pull request still gets up to 3 comments.
	MinCommentBudget = 3
	// MaxCommentBudget: never more than 10 under any configuration.
	MaxCommentBudget = 10
	// LinesPerComment: one extra comment slot per 250 changed lines.
	LinesPerComment = 250
	// MaxConfiguredComments is the schema ceiling on max_comments (§13.3):
	// configuration may shrink the budget below the formula, never grow it
	// past 20. The README states this; the schema enforces it; this clamps it
	// a third time because belt-and-braces is cheap.
	MaxConfiguredComments = 20
	// MaxPerFile: at most 2 comments per file. Ten comments in one file is a
	// rewrite request, and it goes in the body as one paragraph instead.
	MaxPerFile = 2
)

// CommentBudget computes N = clamp(3, 3 + floor(changed_lines/250), 10),
// further clamped by cfgMax (itself schema-capped at 20). cfgMax <= 0 means
// "no configured limit".
func CommentBudget(changedLines, cfgMax int) int {
	if changedLines < 0 {
		changedLines = 0
	}
	budget := MinCommentBudget + changedLines/LinesPerComment
	if budget > MaxCommentBudget {
		budget = MaxCommentBudget
	}
	limit := MaxConfiguredComments
	if cfgMax > 0 && cfgMax < limit {
		limit = cfgMax
	}
	if budget > limit {
		budget = limit
	}
	return budget
}

// AllocationDrop pairs a dropped finding with its drop-log entry.
type AllocationDrop struct {
	Finding model.ValidatedFinding
	Entry   model.DropEntry
}

// AllocateBudget walks ranked findings in the order given (callers rank
// first; this is a capper, not a second reviewer — §13.5) and picks until
// either the total budget or the per-file cap is exhausted. Every cut
// produces a DropEntry with a reason: DropBudget when the overall budget ran
// out, DropPerFileBudget when a single file hit its cap.
//
// perFile <= 0 disables the per-file cap (used by tests); production callers
// pass MaxPerFile.
func AllocateBudget(ranked []model.ValidatedFinding, total, perFile int) ([]model.ValidatedFinding, []AllocationDrop) {
	chosen := make([]model.ValidatedFinding, 0, len(ranked))
	var drops []AllocationDrop
	perFileCount := map[string]int{}

	for _, f := range ranked {
		reason := model.DropReason("")
		switch {
		case len(chosen) >= total:
			reason = model.DropBudget
		case perFile > 0 && perFileCount[f.Path] >= perFile:
			reason = model.DropPerFileBudget
		default:
			chosen = append(chosen, f)
			perFileCount[f.Path]++
			continue
		}
		drops = append(drops, AllocationDrop{
			Finding: f,
			Entry: model.DropEntry{
				Path:     f.Path,
				Category: f.Category,
				Title:    f.Title,
				Reason:   reason,
				Detail: fmt.Sprintf("cut by %s: total budget %d, per-file cap %d",
					reason, total, perFile),
			},
		})
	}
	return chosen, drops
}
