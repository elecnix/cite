package main

// GitHub Actions step-summary support for dropped findings (issue #42).
//
// When a review produces candidate findings but drops them all as
// anchor_invalid, the signal used to live only in the raw CI log —
// truncated, ANSI-mangled, and not where a reviewer looks. Emitting the
// dropped findings to $GITHUB_STEP_SUMMARY puts them on the run page
// itself, one click from the check result, as the issue's minimum-acceptable
// remediation. This is a safety net alongside the review-body section: the
// body is the primary surface, but the step summary survives even a posting
// failure.

import (
	"fmt"
	"os"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// stepSummaryDropCap keeps the Actions summary scannable. A run that drops
// dozens of findings shows the first few and a remainder count.
const stepSummaryDropCap = 10

// writeDropSummary appends a "Dropped findings" section to w for every
// anchor_invalid drop in ds. It returns false (and writes nothing) when
// there are none to surface, so callers can skip the step entirely.
//
// w is typically a $GITHUB_STEP_SUMMARY file, but the helper is io.Writer-
// based so it is testable without touching the environment.
func writeDropSummary(w *strings.Builder, ds []model.DropEntry) bool {
	var notable []model.DropEntry
	for _, d := range ds {
		if d.Reason == model.DropAnchorInvalid {
			notable = append(notable, d)
		}
	}
	if len(notable) == 0 {
		return false
	}
	fmt.Fprintf(w, "### Dropped findings (anchor_invalid)\n\n")
	fmt.Fprintf(w, "These findings were structurally sound but could not be pinned to a specific diff line, so they were not posted as inline comments. They need manual localization.\n\n")
	shown := notable
	overflow := 0
	if len(shown) > stepSummaryDropCap {
		shown = shown[:stepSummaryDropCap]
		overflow = len(notable) - stepSummaryDropCap
	}
	for _, d := range shown {
		fmt.Fprintf(w, "- `%s` — %s (%s)\n", d.Path, model.SanitizeText(d.Title), d.Category)
	}
	if overflow > 0 {
		fmt.Fprintf(w, "\n…and %d more (see the run log).\n", overflow)
	}
	fmt.Fprintln(w)
	return true
}

// emitDropsToStepSummary writes the anchor_invalid drop summary to
// $GITHUB_STEP_SUMMARY when that variable is set (GitHub Actions). Outside
// Actions the variable is unset and the call is a no-op. A write failure is
// logged to stderr but never fails the run: the step summary is a convenience
// surface, not a required one.
func emitDropsToStepSummary(ds []model.DropEntry) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	var sb strings.Builder
	if !writeDropSummary(&sb, ds) {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logToStderr("warning: could not open GITHUB_STEP_SUMMARY: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(sb.String()); err != nil {
		logToStderr("warning: could not write GITHUB_STEP_SUMMARY: %v", err)
	}
}
