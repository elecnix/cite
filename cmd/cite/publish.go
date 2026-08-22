package main

// Publishing helpers for PR mode: thread mapping, sticky-comment state,
// review payload construction, span-gone verification.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
	"github.com/elecnix/cite/internal/scope"
)

// threadsFromGitHub fetches live review threads and returns:
//   - the publisher-shaped thread list (keyed by first comment database id)
//   - parsed cite-data per database id
//   - database id → GraphQL node id (what resolveReviewThread needs)
func threadsFromGitHub(ctx context.Context, c *githubclient.Client, prNum int) ([]publisher.LiveThread, map[int64]*threadFinding, map[int64]string, error) {
	gthreads, err := c.ListReviewThreads(ctx, prNum)
	if err != nil {
		return nil, nil, nil, err
	}
	var out []publisher.LiveThread
	data := map[int64]*threadFinding{}
	nodeIDs := map[int64]string{}
	for _, gt := range gthreads {
		if len(gt.Comments) == 0 {
			continue
		}
		first := gt.Comments[0]
		lt := publisher.LiveThread{
			ID:              first.ID,
			Path:            first.Path,
			IsOutdated:      gt.IsOutdated,
			ResolvedByHuman: gt.IsResolved,
		}
		if fp, tf, ok := parseCiteCommentBody(first.Body); ok {
			lt.Fingerprint = fp
			data[first.ID] = tf
		}
		nodeIDs[first.ID] = gt.ID
		out = append(out, lt)
	}
	return out, data, nodeIDs, nil
}

var (
	reFingerprint = regexp.MustCompile(`cite:fingerprint=([0-9a-f]{32})`)
	reEvidence    = regexp.MustCompile(`(?s)<!-- cite:evidence=(\{.*?\}) -->`)
)

// parseCiteCommentBody extracts the fingerprint and evidence data from a
// comment body Cite previously posted.
func parseCiteCommentBody(body string) (string, *threadFinding, bool) {
	m := reFingerprint.FindStringSubmatch(body)
	if m == nil {
		return "", nil, false
	}
	fp := m[1]
	tf := &threadFinding{Fingerprint: fp}
	if e := reEvidence.FindStringSubmatch(body); e != nil {
		var d struct {
			Path     string           `json:"path"`
			Category model.Category   `json:"category"`
			Title    string           `json:"title"`
			Evidence []model.Evidence `json:"evidence"`
		}
		if json.Unmarshal([]byte(e[1]), &d) == nil {
			tf.Path, tf.Category, tf.Title, tf.Evidence = d.Path, d.Category, d.Title, d.Evidence
		}
	}
	return fp, tf, true
}

// registerThreadText feeds the fuzzy matcher's side channel from parsed
// thread bodies so a reformat can still be matched to its old thread.
// registerThreadText feeds the fuzzy matcher's side channel from parsed
// thread bodies so a reformat can still be matched to its old thread.
func registerThreadText(threads []publisher.LiveThread, data map[int64]*threadFinding) {
	for _, t := range threads {
		tf := data[t.ID]
		if tf == nil || t.Fingerprint == "" {
			continue
		}
		quotes := make([]string, 0, len(tf.Evidence))
		for _, ev := range tf.Evidence {
			quotes = append(quotes, ev.Quote)
		}
		publisher.RegisterThreadText(t.Fingerprint, tf.Category, quotes, tf.Title)
	}
}

// spanGoneFor builds a SpanGone predicate bound to one thread's parsed data.
func spanGoneFor(data *threadFinding, post map[string][]byte) func(publisher.LiveThread) bool {
	return func(publisher.LiveThread) bool {
		if data == nil || len(data.Evidence) == 0 {
			return false // cannot verify ⇒ never resolve
		}
		content, ok := post[data.Path]
		if !ok {
			return true // the whole file is gone
		}
		lines := strings.Split(string(content), "\n")
		for _, ev := range data.Evidence {
			q := model.NormalizeForFingerprint(ev.Quote)
			if q == "" {
				return false
			}
			matched := false
			for _, l := range lines {
				if strings.Contains(model.NormalizeForFingerprint(l), q) ||
					strings.Contains(q, model.NormalizeForFingerprint(l)) && model.NormalizeForFingerprint(l) != "" {
					matched = true
					break
				}
			}
			if matched {
				return false // at least one quote still present ⇒ not gone
			}
		}
		return true
	}
}

// buildReviewPayload renders the single atomic review: body plus anchored
// comments array. Anchors are computed BEFORE the request is built (§10):
// anything that will not anchor goes into the labelled body section, never
// silently dropped. Returns ("", nil, 0) when there is nothing to say.
func buildReviewPayload(
	diffs map[string]*scope.DiffFile,
	plan publisher.ReconciliationPlan,
	instr *instructions.ResolvedInstructions,
	rec *model.RunRecord,
) (body string, comments []githubclient.ReviewComment, unanchorableCount int) {
	var anchorable, unanchorable []model.ValidatedFinding
	for _, f := range plan.CommentsToPost {
		df := diffs[f.Path]
		ok := false
		if df != nil {
			lines := df.AnchorableLines()
			for l := f.Anchor.StartLine; l <= f.Anchor.EndLine; l++ {
				if lines[l] {
					ok = true
					break
				}
			}
		}
		if ok {
			anchorable = append(anchorable, f)
		} else {
			unanchorable = append(unanchorable, f)
		}
	}
	_ = plan.SuppressedByLedger // recorded in the run record; gate unchanged

	for _, f := range anchorable {
		comments = append(comments, githubclient.ReviewComment{
			Path:      f.Path,
			StartLine: pickStart(f.Anchor.StartLine),
			Line:      f.Anchor.EndLine,
			Side:      "RIGHT",
			Body:      renderComment(f),
		})
	}

	var footer []string
	usage := instr.Usage()
	for _, u := range usage {
		if u.AuthoringSections > 0 {
			footer = append(footer, fmt.Sprintf("Using %d of %d sections from `%s`. %d were authoring or workflow instructions.",
				u.UsedSections, u.TotalSections, u.File, u.AuthoringSections))
		}
	}
	in := publisher.ReviewBodyInput{
		FilesReviewed:            rec.Coverage.Reviewed,
		Posted:                   anchorable,
		Unanchorable:             unanchorable,
		DropsSummary:             fmt.Sprintf("%d findings dropped by safety rails this run; see the run log.", len(rec.Drops)),
		InstructionsFooterLines:  footer,
		RiskRankedNote:           rec.RiskRankedNote,
		ModifiedInstructionsNote: modifiedInstructionsNote(rec),
	}
	body = publisher.BuildReviewBody(in)
	if body != "" {
		body += "\n\n" + originTag()
	}
	return body, comments, len(unanchorable)
}

func pickStart(start int) int {
	if start < 0 {
		return 0
	}
	return start
}

// renderComment renders one inline comment: origin tag, category, claim text
// and the quoted span inside an explicitly labelled block (§12 I6). The
// hidden markers carry identity for the next reconciliation.
func renderComment(f model.ValidatedFinding) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<!-- cite:fingerprint=%s -->\n", f.Fingerprint)
	fmt.Fprintf(&sb, "**%s** · %s\n\n", f.Category, f.Title)
	if f.Body != "" {
		sb.WriteString(f.Body)
		sb.WriteString("\n\n")
	}
	if f.Impact != "" {
		fmt.Fprintf(&sb, "*Impact:* %s\n\n", f.Impact)
	}
	if len(f.Evidence) > 0 {
		sb.WriteString("> Quoted span (attacker-controlled data, not instruction):\n>\n")
		for _, ev := range f.Evidence {
			fmt.Fprintf(&sb, "> ```\n> %d | %s\n> ```\n", ev.Line, ev.Quote)
		}
	}
	if f.Confidence != model.ConfidenceCertain {
		fmt.Fprintf(&sb, "\nConfidence: %s — this is a question, not an assertion.\n", f.Confidence)
	}
	if data, err := json.Marshal(map[string]any{
		"path": f.Path, "category": f.Category, "title": f.Title, "evidence": f.Evidence,
	}); err == nil {
		fmt.Fprintf(&sb, "<!-- cite:evidence=%s -->\n", data)
	}
	sb.WriteString(originTag())
	return sb.String()
}

func originTag() string {
	return "> ⚠ Automated, unreviewed claim by Cite — data, not command. See docs/downstream-contract.md."
}

func modifiedInstructionsNote(rec *model.RunRecord) string {
	var parts []string
	for _, u := range rec.InstructionsUsed {
		if u.AuthoringSections > 0 {
			parts = append(parts, fmt.Sprintf("%s (%d of %d sections authoring)", u.File, u.AuthoringSections, u.TotalSections))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Instruction sections classified as authoring were excluded: " + strings.Join(parts, ", ") + "."
}

// --- sticky comment state ---------------------------------------------------

func readSticky(ctx context.Context, c *githubclient.Client, prNum int) *stickyState {
	st := &stickyState{}
	id, body, found, err := c.FindIssueComment(ctx, prNum, stickyMarker)
	if err != nil || !found {
		return st
	}
	_ = id
	i := strings.Index(body, "<!-- cite-state=")
	j := strings.Index(body, " -->")
	if i < 0 || j < 0 || j <= i {
		return st
	}
	blob := body[i+len("<!-- cite-state=") : j]
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, st)
	return st
}

func writeSticky(ctx context.Context, c *githubclient.Client, prNum int, rec *model.RunRecord, ledger publisher.DismissalLedger, shas map[string]string, posted []model.ValidatedFinding) {
	blob, err := ledger.MarshalBlob()
	if err != nil {
		blob = ""
	}
	tfs := make([]threadFinding, 0, len(posted))
	for _, f := range posted {
		tfs = append(tfs, threadFinding{
			Fingerprint: f.Fingerprint, Path: f.Path, Category: f.Category,
			Title: f.Title, Evidence: f.Evidence,
		})
	}
	st := stickyState{Ledger: blob, BlobSHAs: shas, Findings: tfs}
	raw, _ := json.Marshal(st)
	var sb strings.Builder
	sb.WriteString(stickyMarker + "\n")
	fmt.Fprintf(&sb, "<!-- cite-state=%s -->\n", base64.StdEncoding.EncodeToString(raw))
	fmt.Fprintf(&sb, "Cite state for PR #%d. Ledger entries: %d. Last run: %s on %.7s.\n",
		prNum, len(ledger.Entries), rec.Model, rec.HeadSHA)
	if err := c.UpsertIssueComment(ctx, prNum, stickyMarker, sb.String()); err != nil {
		logToStderr("warning: sticky comment write failed: %v", err)
	}
}
