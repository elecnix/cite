package scope

import (
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// Span survival has one owner. Two callers ask the same question — did the
// text a finding quoted survive into the file's new content? — and they need
// opposite answers from it:
//
//   - thread reconciliation, in cmd/cite, resolves a review thread only on a
//     verified basis, and "no quoted span survives" is that basis (§10).
//   - the fix_or_argue instrument, in internal/metrics, counts a finding as
//     fixed when a later head SHA changed the content of its anchored span
//     (§15).
//
// The comparison itself is therefore written once, here, and both callers
// supply only their polarity and their aggregation over several quotes. The
// edge policy is fixed by this file:
//
//   - No evidence at all: unverifiable. Neither Gone nor Changed claims a
//     verdict; an unresolvable thread stays open and no metric is spent.
//   - Content that is absent or empty: a file with no bytes contains no
//     quoted span, so the span is gone. A deleted file is simply the limit
//     case and needs no separate rule.
//   - A quote that normalises to nothing (pure punctuation or symbols)
//     verifies nothing. It is neither a surviving span nor a vanished one, so
//     it blocks a gone or changed verdict outright: nothing unverifiable is
//     ever counted as resolved or as fixed.
//   - Multi-line quotes: both sides are normalised as one string, so the
//     newlines inside a quote collapse to spaces exactly as the newlines
//     inside the file do. Normalising line by line and rejoining with a
//     literal newline leaves a multi-line quote that no file content can
//     ever contain, which reads as a fixed span for code that never moved.
//
// Normalisation is containment in the forward direction only: a fragment of
// the file that happens to be a substring of a quote says nothing about the
// span's presence, which is why short lines like "fi" or "cmd" must not keep
// a span alive.

// QuoteSurvives reports whether the quoted span is still present in content.
// The quote is checked against the whole normalised content, not per line, so
// a span that merely moved still counts as present. A quote that normalises
// to nothing grounds nothing and never survives.
func QuoteSurvives(content []byte, quote string) bool {
	q := model.NormalizeForFingerprint(quote)
	if q == "" {
		return false // an empty normalized quote grounds nothing
	}
	return strings.Contains(model.NormalizeForFingerprint(string(content)), q)
}

// verifiable reports whether every quote grounds a comparison at all. One
// unverifiable quote makes the whole finding unverifiable, in either
// direction: a span Cite cannot read cannot be shown to be gone, and cannot
// be shown to have changed either.
func verifiable(evidence []model.Evidence) bool {
	for _, ev := range evidence {
		if model.NormalizeForFingerprint(ev.Quote) == "" {
			return false
		}
	}
	return true
}

// EvidenceGone reports whether NO quoted span survives content — the verified
// basis on which a review thread may be resolved (§10). One surviving quote
// keeps the span alive: a span is gone only when none of the text Cite
// anchored is left to look at.
func EvidenceGone(content []byte, evidence []model.Evidence) bool {
	if len(evidence) == 0 || !verifiable(evidence) {
		return false // cannot verify, so never claim gone
	}
	for _, ev := range evidence {
		if QuoteSurvives(content, ev.Quote) {
			return false // at least one quote still present
		}
	}
	return true
}

// AnyQuoteGone reports whether SOME quoted span is absent from content, the
// shape of "the anchored span changed" that fix_or_argue counts as fixed
// (§15). It is not the negation of EvidenceGone: with several quotes on one
// finding, one vanishing quote is a change even when the others survive,
// because the span Cite pointed at no longer reads the way it was published.
// On a single verifiable quote the two agree exactly, which is the property
// the two callers depend on.
func AnyQuoteGone(content []byte, evidence []model.Evidence) bool {
	if len(evidence) == 0 || !verifiable(evidence) {
		return false // nothing to verify, so no action claimed
	}
	for _, ev := range evidence {
		if !QuoteSurvives(content, ev.Quote) {
			return true
		}
	}
	return false
}
