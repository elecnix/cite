package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// The triage pass (PLAN.md §7, "The unit: triage wide, review narrow"): one
// cheap whole-diff call, carrying the full manifest, that flags which files
// are worth a close look and is the pass that can see cross-file structure.
// It runs FIRST and SERIALIZED: more requests must not fan out before the
// first response begins.
//
// If the call fails or returns unusable output, the run falls back to
// reviewing every reviewable file batched — never to silence.

const (
	triageMaxLinesPerFile = 400  // per-file diff excerpt cap for the cheap pass
	maxFileLines          = 2000 // envelope truncation point: context="partial" beyond this
	batchSize             = 6    // files per batched frontier wave (§7: roughly six)
	pinnedTemperature     = 0.0  // temperature is pinned; identical inputs still vary under batching/expert routing (§8)
	// Output caps. These are floors, not ceilings: a model entry declaring
	// max_tokens overrides them in either direction (see roleSettings).
	//
	// A per-file review must be able to emit the schema's worst case without
	// running out of room — see DefaultReviewMaxOutputTokens in internal/config
	// for the sizing rationale. The review deadline derives from this same cap
	// (config.DerivedReviewTimeout, issue #28), so the two numbers are
	// calibrated together.
	defaultReviewMaxTokens = config.DefaultReviewMaxOutputTokens
	// Triage emits one path plus one sentence per flagged file, so it scales
	// with file count rather than file size; 2048 is tight on a
	// hundred-file pull request.
	defaultTriageMaxTokens = 8192
)

const triageSystemPrompt = `You are the triage stage of a code reviewer. You see the changed-file
manifest of ONE pull request and a bounded excerpt of each file's diff.

Flag every file whose diff deserves a close, file-by-file review: new logic,
security-relevant surfaces, error handling, concurrency, config or CI changes,
anything cross-file. Files that are pure formatting, comment-only, doc-only,
or trivially mechanical should NOT be flagged.

Output JSON only, matching the schema: {"schema_version":1,"flagged":
[{"path":"...","reason":"one short sentence"}]}. Every path MUST be copied
verbatim from the manifest. Flag nothing you are not sure about — flagged
files cost more, unflagged files are still reviewed.`

type triageResult struct {
	SchemaVersion int `json:"schema_version"`
	Flagged       []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"flagged"`
}

func triageResponseSchema() json.RawMessage {
	root := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"schema_version", "flagged"},
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer"},
			"flagged": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"path", "reason"},
					"properties": map[string]any{
						"path":   map[string]any{"type": "string"},
						"reason": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	return json.RawMessage(canonicalJSON(root))
}

// diffExcerpt renders one file's hunks as a bounded unified-diff excerpt.
func diffExcerpt(df *scope.DiffFile, maxLines int) string {
	var b strings.Builder
	n := 0
	for _, h := range df.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			if n >= maxLines {
				b.WriteString("... (excerpt truncated)\n")
				return b.String()
			}
			switch l.Kind {
			case scope.LineAdded:
				b.WriteByte('+')
			case scope.LineRemoved:
				b.WriteByte('-')
			default:
				b.WriteByte(' ')
			}
			b.WriteString(l.Content)
			b.WriteByte('\n')
			n++
		}
	}
	return b.String()
}

// runTriage performs the single serialized whole-diff call and returns the
// flagged path set plus whether the output was usable.
func (r *Reviewer) runTriage(ctx context.Context, in *Inputs) (flagged map[string]bool, usable bool) {
	timeout, _, maxTokens := r.roleSettings(model.RoleTriage, config.DefaultTriageTimeout, 1, defaultTriageMaxTokens)

	var b strings.Builder
	b.WriteString(r.contextSegment(in))
	b.WriteString("\n\n<diff_excerpts>\n")
	for _, e := range in.Manifest {
		df := in.Diffs[e.Path]
		fmt.Fprintf(&b, "<diff path=%q status=%q>\n", e.Path, e.Status)
		if df != nil {
			b.WriteString(diffExcerpt(df, triageMaxLinesPerFile))
		} else {
			b.WriteString("(no parsed hunks)\n")
		}
		b.WriteString("</diff>\n")
	}
	b.WriteString("</diff_excerpts>\n")

	req := model.CompletionRequest{
		System:          triageSystemPrompt,
		User:            b.String(),
		MaxOutputTokens: maxTokens,
		Temperature:     pinnedTemperature,
		ResponseSchema:  triageResponseSchema(),
	}
	resp, err := r.completeWithRetry(ctx, unitTriage, req, timeout)
	if err != nil {
		r.logf("triage call failed (%v); falling back to reviewing all files batched", err)
		return nil, false
	}
	var tr triageResult
	if err := json.Unmarshal(model.UnwrapJSON([]byte(resp.Text)), &tr); err != nil {
		r.logf("triage output unparsable (%v); falling back to reviewing all files batched", err)
		return nil, false
	}
	if tr.SchemaVersion != model.SchemaVersion {
		r.logf("triage output schema version %d unusable; falling back to reviewing all files batched", tr.SchemaVersion)
		return nil, false
	}
	inManifest := map[string]bool{}
	for _, e := range in.Manifest {
		inManifest[e.Path] = true
	}
	flagged = make(map[string]bool)
	for _, f := range tr.Flagged {
		if !inManifest[f.Path] {
			// A flag naming a file the manifest does not list means the
			// model is hallucinating paths: the manifest is the ONLY
			// authority on which files exist (§7). Treat the whole answer
			// as unusable rather than trusting the rest of it.
			r.logf("triage flagged unknown path %q; output unusable, reviewing all files batched", f.Path)
			return nil, false
		}
		flagged[f.Path] = true
	}
	return flagged, true
}
