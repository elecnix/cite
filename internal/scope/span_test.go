package scope

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

func ev(line int, quote string) model.Evidence {
	return model.Evidence{Line: line, Quote: quote}
}

// A multi-line quote survives a file that still contains it. Normalising the
// file line by line and rejoining with a literal newline left the haystack
// holding "a b\nc" while the quote normalised to "a b c", so every multi-line
// quote read as gone no matter what the file contained.
func TestQuoteSurvivesMultiLine(t *testing.T) {
	content := []byte("if !escaped {\n\tel.innerHTML = user.displayName\n}\n")
	if !QuoteSurvives(content, "if !escaped {\n\tel.innerHTML = user.displayName") {
		t.Fatal("a two-line quote present verbatim must survive")
	}
}

// Both sides are normalised as one string, so a span that was reflowed onto
// one line at head still counts as present.
func TestQuoteSurvivesReflowedMultiLine(t *testing.T) {
	content := []byte("if !escaped { el.innerHTML = user.displayName }\n")
	if !QuoteSurvives(content, "if !escaped {\n\t\tel.innerHTML = user.displayName") {
		t.Fatal("reflowed whitespace is drift, not a change")
	}
}

// A quote that moved to a different line is present.
func TestQuoteSurvivesMoved(t *testing.T) {
	if !QuoteSurvives([]byte("x\nwrite_queue(read_queue())\n"), "write_queue(read_queue())") {
		t.Fatal("a quote that moved lines is still present")
	}
}

// Forward containment only: a short file line that is a substring of the
// quote says nothing about the span.
func TestQuoteSurvivesShortLineIsNotEvidence(t *testing.T) {
	if QuoteSurvives([]byte("fi\nit\ncmd\n"), "setsid sleep infinity > \"$d/err.log\" &") {
		t.Fatal("short lines inside the quote must not keep the span alive")
	}
}

// A quote of pure punctuation normalises to nothing and grounds nothing.
func TestQuoteSurvivesEmptyNormalisedQuote(t *testing.T) {
	if QuoteSurvives([]byte("anything at all"), "***") {
		t.Fatal("an empty normalized quote verifies nothing")
	}
}

func TestEvidenceGoneEdgeCases(t *testing.T) {
	if EvidenceGone([]byte("x"), nil) {
		t.Fatal("no evidence is unverifiable, never gone")
	}
	if EvidenceGone([]byte("anything"), []model.Evidence{ev(1, "***")}) {
		t.Fatal("an empty normalized quote is unverifiable, never gone")
	}
	if !EvidenceGone(nil, []model.Evidence{ev(1, "old line")}) {
		t.Fatal("content with no bytes holds no quoted span")
	}
	if EvidenceGone([]byte("still here\nold line\n"), []model.Evidence{ev(1, "old line")}) {
		t.Fatal("a surviving quote keeps the span alive")
	}
}

// Unverifiable evidence blocks a verdict in either direction: a quote Cite
// cannot read is never treated as proof that the span is gone, and never as
// proof that it changed.
func TestEvidenceGoneUnverifiableQuoteBlocksVerdict(t *testing.T) {
	e := []model.Evidence{ev(1, "***"), ev(2, "old line")}
	if EvidenceGone([]byte("something else\n"), e) {
		t.Fatal("an unverifiable quote must not resolve a thread on its own")
	}
	if EvidenceGone([]byte("something else\nold line\n"), e) {
		t.Fatal("a real surviving quote still keeps the span alive")
	}
	if AnyQuoteGone([]byte("something else\n"), e) {
		t.Fatal("an unverifiable quote must not spend a fixed finding")
	}
}

func TestAnyQuoteGoneEdgeCases(t *testing.T) {
	if AnyQuoteGone([]byte("code"), nil) {
		t.Fatal("no evidence claims nothing")
	}
	if !AnyQuoteGone(nil, []model.Evidence{ev(1, "x")}) {
		t.Fatal("content with no bytes holds no quoted span")
	}
	if AnyQuoteGone([]byte("old line\n"), []model.Evidence{ev(1, "***")}) {
		t.Fatal("an unverifiable quote must not claim a change")
	}
	if AnyQuoteGone([]byte("old line\n"), []model.Evidence{ev(1, "old line")}) {
		t.Fatal("a surviving quote is not a change")
	}
	// One vanished quote is a change even when another survives: the anchored
	// span no longer reads the way it was published.
	if !AnyQuoteGone([]byte("a survived\n"), []model.Evidence{ev(1, "a survived"), ev(2, "vanished")}) {
		t.Fatal("one vanished quote marks the span changed")
	}
}

// The two aggregations are deliberately different and must not be confused
// with each other: Gone is for resolving a thread, Changed is for the metric.
func TestGoneAndChangedDifferOnlyByAggregation(t *testing.T) {
	content := []byte("if !escaped {\n\tel.innerHTML = user.displayName\n}\n")
	multi := []model.Evidence{ev(2, "if !escaped {\n\tel.innerHTML = user.displayName")}
	if Gone := EvidenceGone(content, multi); Gone {
		t.Fatal("a surviving multi-line span is not gone")
	}
	if AnyQuoteGone(content, multi) {
		t.Fatal("a surviving multi-line span is not changed")
	}
	split := []model.Evidence{ev(2, "if !escaped {"), ev(3, "el.innerHTML = user.displayName")}
	if EvidenceGone(content, split) {
		t.Fatal("both halves present: the span is not gone")
	}
}
