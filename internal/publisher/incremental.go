// Incremental re-review (§10, end): on a new push, re-review only files whose
// CONTENT changed, comparing path → blob_sha maps rather than commits. That
// is immune to force-push, rebase, squash and to the old commit having been
// garbage-collected — git diff <sha> <sha> fails all four, and a depth-1
// checkout does not even have the object.
//
// When in doubt, incremental fails toward re-review, never toward
// carry-forward: a silently un-raised finding is the failure mode that ends
// the tool's credibility in one afternoon.
package publisher

import (
	"sort"

	"github.com/elecnix/cite/internal/model"
)

// FilesToReview returns the paths whose blob_sha differs between the previous
// reviewed state and the current head — new files included, deleted files
// excluded (there is no content left to review). Output is sorted so runs are
// deterministic regardless of map iteration order.
func FilesToReview(prev, cur map[string]string) []string {
	var out []string
	for path, sha := range cur {
		if prev[path] != sha {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// CarryForward splits the previous run's findings after an incremental pass.
//
// A finding on a file that was NOT re-reviewed comes back as a carried
// candidate requiring verification before anything is resolved or dropped —
// incremental fails toward re-review, never toward silently dropping.
//
// A finding on a file that WAS re-reviewed is resolvedPending: its file got a
// fresh review, so if the finding did not reappear it may resolve — but only
// once the caller verifies the underlying span is actually gone from the file
// (the same bar Reconcile applies). It is pending resolution, never presumed
// resolved.
func CarryForward(prevFindings []model.ValidatedFinding, reviewedPaths map[string]bool) (carried, resolvedPending []model.ValidatedFinding) {
	for _, f := range prevFindings {
		if reviewedPaths[f.Path] {
			resolvedPending = append(resolvedPending, f)
		} else {
			carried = append(carried, f)
		}
	}
	return carried, resolvedPending
}
