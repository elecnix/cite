// Package metrics computes the measurement instruments that are fully
// derivable from the GitHub API (PLAN.md §15).
//
// The only instrument implemented here is fix_or_argue, the weekly proxy:
//
//	a published finding counts as actioned when a later head SHA changed
//	the normalised content of its anchored span ("fixed"), or when a human
//
// posted a reply over 40 characters that is not a dismissal ("argued").
//
// Both cost the human something, which makes the metric fatigue-resistant
// where dismissal telemetry is not. It gates nothing until validated once
// against a gold campaign with correlation r ≥ 0.5.
package metrics

import (
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// MinReplyChars: a reply over 40 characters counts as argument. Shorter
// replies ("ok", "done", "+1") cost nothing and stay outside the metric.
const MinReplyChars = 40

// Finding is one published finding, identified by fingerprint, with the
// evidence it was published under. It mirrors what parseCiteCommentBody
// recovers from a posted review comment — no RunRecord required.
type Finding struct {
	Fingerprint string
	Path        string
	Evidence    []model.Evidence
}

// Reply is one comment written on a finding's thread by someone who is not
// Cite.
type Reply struct {
	Author string
	Body   string
	IsBot  bool
}

// Report carries two denominators under two names (§15's first rule):
// Published is published_findings; there is no generated_findings here
// because this metric never sees model output directly.
type Report struct {
	Published int // published_findings — the product denominator
	Fixed     int // span changed at head
	Argued    int // qualifying human reply
	Actioned  int // fixed ∪ argued
}

// Rate is fix_or_argue: actioned / published. Zero when nothing was
// published — never NaN.
func (r Report) Rate() float64 {
	if r.Published == 0 {
		return 0
	}
	return float64(r.Actioned) / float64(r.Published)
}

// SpanChanged reports whether any of the finding's evidence quotes failed to
// survive into headContent. Both sides go through the same documented
// normaliser as the evidence cascade, so whitespace drift does not count as
// an edit. Missing content (a deleted file) is the strongest form of gone.
// Empty evidence cannot be verified either way and never claims action.
func SpanChanged(evidence []model.Evidence, headContent []byte) bool {
	if len(evidence) == 0 {
		return false
	}
	if len(headContent) == 0 {
		return true
	}
	lines := strings.Split(string(headContent), "\n")
	normLines := make([]string, len(lines))
	for i, l := range lines {
		normLines[i] = model.NormalizeForFingerprint(l)
	}
	haystack := strings.Join(normLines, "\n")
	for _, e := range evidence {
		q := model.NormalizeForFingerprint(e.Quote)
		if q == "" {
			continue // an empty quote verifies nothing
		}
		if !strings.Contains(haystack, q) {
			return true
		}
	}
	return false
}

// Evaluate classifies each published finding as fixed, argued, both, or
// neither, and returns the aggregate report. heads maps path → file content
// at the current head SHA; replies maps fingerprint → human-visible thread
// replies (bot-authored entries must carry IsBot or be pre-filtered).
func Evaluate(findings []Finding, heads map[string][]byte, replies map[string][]Reply) Report {
	var rep Report
	rep.Published = len(findings)
	for _, f := range findings {
		fixed := SpanChanged(f.Evidence, heads[f.Path])
		argued := false
		for _, r := range replies[f.Fingerprint] {
			if !r.IsBot && !strings.HasSuffix(r.Author, "[bot]") && len([]rune(strings.TrimSpace(r.Body))) > MinReplyChars {
				argued = true
				break
			}
		}
		if fixed {
			rep.Fixed++
		}
		if argued {
			rep.Argued++
		}
		if fixed || argued {
			rep.Actioned++
		}
	}
	return rep
}
