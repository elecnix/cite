package publisher

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/elecnix/cite/internal/model"
)

// InlineComment is a GitHub-free rendering of one diff-anchored comment. The
// sink interface deliberately does not reference githubclient types: local
// destinations must be able to render a run without importing anything
// GitHub-shaped.
type InlineComment struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"` // 0 means no multi-line range
	Line      int    `json:"line"`
	Side      string `json:"side,omitempty"`
	Body      string `json:"body"`
}

// ReportPayload is everything a run produced that a destination might publish:
// the review body as it would appear on the pull request, the inline comments
// that would accompany it, and the count of findings demoted into the body
// because their anchors did not land on added lines.
type ReportPayload struct {
	Body         string          `json:"body,omitempty"`
	Comments     []InlineComment `json:"comments,omitempty"`
	Unanchorable int             `json:"unanchorable,omitempty"`
}

// Sink consumes a finished review run. GitHub is one implementation (the
// check-run + review + sticky-comment flow in cmd/cite); local report sinks
// write the same outcome to a file or stdout so a run can be inspected
// without deploying anything to the target repository.
type Sink interface {
	Publish(rec *model.RunRecord, payload ReportPayload) error
}

// JSONReportSink writes {run, review} as one JSON document. The run record is
// serialized exactly as it is persisted elsewhere, so tooling that reads run
// artifacts can read this unchanged.
func JSONReportSink(w io.Writer) Sink { return jsonSink{w: w} }

type jsonSink struct{ w io.Writer }

func (s jsonSink) Publish(rec *model.RunRecord, payload ReportPayload) error {
	enc := json.NewEncoder(s.w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Run    *model.RunRecord `json:"run"`
		Review ReportPayload    `json:"review"`
	}{Run: rec, Review: payload})
}

// MarkdownReportSink renders a human-readable report: verdict header, coverage
// and cost summary, findings grouped blocking-first with evidence quotes,
// then drops and the payload's own body.
func MarkdownReportSink(w io.Writer) Sink { return mdSink{w: w} }

type mdSink struct{ w io.Writer }

func (s mdSink) Publish(rec *model.RunRecord, payload ReportPayload) error {
	target := fmt.Sprintf("%s", rec.Repository)
	if rec.PRNumber > 0 {
		target = fmt.Sprintf("%s#%d", rec.Repository, rec.PRNumber)
	}
	fmt.Fprintf(s.w, "## Cite review — %s\n\n", target)
	fmt.Fprintf(s.w, "**Verdict:** %s", rec.Verdict)
	if rec.VerdictReason != "" {
		fmt.Fprintf(s.w, " — %s", rec.VerdictReason)
	}
	fmt.Fprint(s.w, "\n\n")

	c := rec.Coverage
	fmt.Fprintf(s.w, "Coverage: %d/%d in-scope files reviewed", c.Reviewed, c.APIFiles)
	if c.ApprovedSkip > 0 {
		fmt.Fprintf(s.w, ", %d approved-skipped", c.ApprovedSkip)
	}
	if c.Errored > 0 {
		fmt.Fprintf(s.w, ", %d errored", c.Errored)
	}
	if !c.Complete {
		fmt.Fprint(s.w, " — **incomplete**")
	}
	fmt.Fprintf(s.w, " · samples: %d · cost: $%.4f (in %d out %d)\n",
		rec.Samples, rec.CostUSD, rec.Usage.InputTokens, rec.Usage.OutputTokens)

	if len(rec.Findings) == 0 {
		fmt.Fprint(s.w, "\nNo findings.\n")
	} else {
		blocked := make([]model.ValidatedFinding, 0, len(rec.Findings))
		notes := make([]model.ValidatedFinding, 0, len(rec.Findings))
		for _, f := range rec.Findings {
			if f.Blocks {
				blocked = append(blocked, f)
			} else {
				notes = append(notes, f)
			}
		}
		sort.SliceStable(blocked, func(i, j int) bool { return blocked[i].Path < blocked[j].Path })
		sort.SliceStable(notes, func(i, j int) bool { return notes[i].Path < notes[j].Path })
		if len(blocked) > 0 {
			fmt.Fprint(s.w, "\n### Blocking findings\n\n")
			writeFindings(s.w, blocked)
		}
		if len(notes) > 0 {
			fmt.Fprint(s.w, "\n### Notes\n\n")
			writeFindings(s.w, notes)
		}
	}

	if len(rec.Drops) > 0 {
		fmt.Fprintf(s.w, "\n### Dropped by safety rails (%d)\n\n", len(rec.Drops))
		for _, d := range rec.Drops {
			line := "- `" + d.Path + "`"
			if d.Title != "" {
				line += " — " + d.Title
			}
			line += " (`" + string(d.Reason) + "`)"
			fmt.Fprintln(s.w, line)
		}
	}

	if len(payload.Comments) > 0 || payload.Body != "" || payload.Unanchorable > 0 {
		fmt.Fprintf(s.w, "\n### Would publish (%d inline comment(s))\n\n", len(payload.Comments))
		for _, c := range payload.Comments {
			loc := fmt.Sprintf("`%s:%d`", c.Path, c.Line)
			if c.StartLine > 0 && c.StartLine != c.Line {
				loc = fmt.Sprintf("`%s:%d-%d`", c.Path, c.StartLine, c.Line)
			}
			fmt.Fprintf(s.w, "- %s\n", loc)
		}
		if payload.Unanchorable > 0 {
			fmt.Fprintf(s.w, "- %d finding(s) demoted into the review body (no anchorable added line)\n", payload.Unanchorable)
		}
		if payload.Body != "" {
			fmt.Fprintf(s.w, "\n<details><summary>Review body</summary>\n\n%s\n\n</details>\n", payload.Body)
		}
	}
	return nil
}

func writeFindings(w io.Writer, fs []model.ValidatedFinding) {
	for _, f := range fs {
		fmt.Fprintf(w, "#### `%s` — %s (%s, confidence %s)\n\n", f.Path, f.Title, f.Category, f.Confidence)
		if f.Body != "" {
			fmt.Fprintln(w, f.Body+"\n")
		}
		if f.Impact != "" {
			fmt.Fprintf(w, "*Impact:* %s\n\n", f.Impact)
		}
		for _, e := range f.Evidence {
			fmt.Fprintf(w, "- `%s:%d`: `%s`\n", f.Path, e.Line, e.Quote)
		}
		if len(f.Evidence) > 0 {
			fmt.Fprint(w, "\n")
		}
	}
}
