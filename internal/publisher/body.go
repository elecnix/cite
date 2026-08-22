// The review body (§10): capped at five lines of prose. The 400-word
// "Summary of Changes" preamble is universally disliked and read by nobody.
// If it cannot be said in five lines, the inline comments were supposed to
// say it. Findings that will not anchor go into a labelled section — never
// silently dropped, because one bad anchor must not take the run's findings
// with it.
package publisher

import (
	"fmt"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// MaxProseLines is the hard cap on prose lines in the review body (§10).
const MaxProseLines = 5

// UnanchoredHeading labels the section holding findings that will not anchor
// to a diff line. They are never silently dropped.
const UnanchoredHeading = "Not anchored to a diff line:"

// ReviewBodyInput collects everything the review body needs. Model-authored
// text (finding titles, notes that may embed configuration) is passed through
// model.SanitizeText before it reaches the renderer (§12, I5).
type ReviewBodyInput struct {
	// FilesReviewed and Posted summarise the run in the single opening line.
	FilesReviewed int
	Posted        []model.ValidatedFinding
	// Unanchorable findings go into the labelled section.
	Unanchorable []model.ValidatedFinding
	// DropsSummary points at the run record and its drop log ("why didn't
	// you say that" lives there, not here).
	DropsSummary string
	// InstructionsFooterLines are fixed, tool-authored footer lines; they are
	// not prose and not counted against the cap.
	InstructionsFooterLines []string
	// RiskRankedNote says when risk-ranking capped the reviewed set — never
	// silently (RunRecord.RiskRanked).
	RiskRankedNote string
	// ModifiedInstructionsNote says when instruction sections were dropped by
	// applicability triage — never silently.
	ModifiedInstructionsNote string
}

// BuildReviewBody renders the review body. Prose (the opening line plus the
// two optional notes) is capped at MaxProseLines; the unanchored-findings
// section and the footer are structured, not prose, and follow the cap.
// Returns "" when there is nothing to say — nothing is posted when there is
// nothing to say (§10).
func BuildReviewBody(in ReviewBodyInput) string {
	if len(in.Posted) == 0 && len(in.Unanchorable) == 0 {
		return ""
	}

	blocking := 0
	for _, f := range in.Posted {
		if f.Blocks {
			blocking++
		}
	}

	prose := []string{
		fmt.Sprintf("Cite reviewed %d file(s): %d finding(s) posted (%d blocking). Detail is in the inline comments, not here.",
			in.FilesReviewed, len(in.Posted), blocking),
	}
	if in.RiskRankedNote != "" {
		prose = append(prose, model.SanitizeText(in.RiskRankedNote))
	}
	if in.ModifiedInstructionsNote != "" {
		prose = append(prose, model.SanitizeText(in.ModifiedInstructionsNote))
	}
	// Cap the prose. Truncation is loud, not silent: the last line says so.
	if len(prose) > MaxProseLines {
		prose = append(prose[:MaxProseLines-1], "(review body truncated at the five-line prose cap — see the run record)")
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(prose, "\n"))

	if len(in.Unanchorable) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(UnanchoredHeading)
		sb.WriteString("\n")
		for _, f := range in.Unanchorable {
			fmt.Fprintf(&sb, "- `%s`: %s (%s)\n", f.Path, model.SanitizeText(f.Title), f.Category)
		}
	}

	if in.DropsSummary != "" {
		sb.WriteString("\n")
		sb.WriteString(model.SanitizeText(in.DropsSummary))
		sb.WriteString("\n")
	}

	for _, line := range in.InstructionsFooterLines {
		sb.WriteString("\n")
		sb.WriteString(line)
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}
