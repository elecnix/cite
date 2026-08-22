package scope

import "github.com/elecnix/cite/internal/model"

// ComputeCoverage computes the coverage arithmetic in code, never attested by
// the model (§7, §12): count(reviewed ∪ approved-skip) == count(api files).
//
// Complete is true only when every API file was reviewed or carries an
// approved skip reason. Errored files are terminal but not covered, so any
// error breaks completeness — the gate maps that to COULD_NOT_EVALUATE (§11).
//
// Zero in-scope files is explicitly flagged: Complete is false when
// apiCount is 0, and EmptyInScope distinguishes the dangerous shape — a PR
// that changed files but resolved to an empty in-scope set, which is exactly
// a path-filter bypass and must never go green (§11).
func ComputeCoverage(files []model.FileOutcome, apiCount int) model.Coverage {
	c := model.Coverage{APIFiles: apiCount}
	for _, f := range files {
		switch f.State {
		case model.FileReviewed:
			c.Reviewed++
		case model.FileSkipped:
			if IsApprovedSkipReason(f.Reason) {
				c.ApprovedSkip++
			}
			// An unapproved skip reason counts toward nothing: it will
			// break completeness and the gate fails closed.
		case model.FileErrored:
			c.Errored++
		}
	}
	switch {
	case apiCount <= 0:
		c.Complete = false // zero changed files: flagged, not passed
	default:
		c.Complete = c.Reviewed+c.ApprovedSkip == c.APIFiles && c.Errored == 0
	}
	return c
}

// EmptyInScope reports the path-filter-bypass shape: the GitHub API listed
// changed files, yet nothing was reviewed or approved-skipped (§11).
func EmptyInScope(c model.Coverage) bool {
	return c.APIFiles > 0 && c.Reviewed == 0 && c.ApprovedSkip == 0
}

// CoverageHolds restates the §7 assertion as a single boolean, for tests and
// gate checks alike.
func CoverageHolds(c model.Coverage) bool {
	return c.Complete
}
