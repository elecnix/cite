package reviewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// Per-finding validation (PLAN.md §8): the anchor check, the absence-claim
// branch, the external-claims disposition table, the nits suppression
// (§13.4), the per-file finding cap, and the blocking formula — all computed
// in code from fields that can be checked, never attested by the model.

// maxFindingsPerFile is the per-file cap from the schema prompt (§8 OUTPUT:
// "At most 8 findings — a cap, not a target"). Extras are dropped with
// DropParseFailure detail "over per-file cap".
const maxFindingsPerFile = 8

// fileContext is everything is everything the validator needs about one file's envelope:
// the post-image lines the model actually saw, whether the view was
// truncated, and what the parsed diff allows.
type fileContext struct {
	path       string
	lines      []string
	partial    bool
	anchorable map[int]bool // post-change lines present in the hunks (added or context)
	added      map[int]bool // post-change lines this change added
	norm       *normalizer
}

// validateFindings runs the full pipeline over one parsed FileReview and
// returns the validated findings and the drop-log entries it produced.
func (r *Reviewer) validateFindings(fc *fileContext, fr *model.FileReview) ([]model.ValidatedFinding, []model.DropEntry) {
	var (
		out   []model.ValidatedFinding
		drops []model.DropEntry
	)
	drop := func(f *model.Finding, reason model.DropReason, detail string) {
		drops = append(drops, model.DropEntry{
			Path:     fc.path,
			Category: f.Category,
			Title:    model.SanitizeText(f.Title),
			Reason:   reason,
			Detail:   detail,
		})
	}

	occurrence := map[string]int{}
	for i := range fr.Findings {
		f := &fr.Findings[i]

		// Per-file cap (§8 OUTPUT): a cap, not a target. Extras never
		// reach a human; they are logged so the recall cost is measurable.
		if i >= maxFindingsPerFile {
			drop(f, model.DropParseFailure, "over per-file cap")
			continue
		}

		// Nits suppression (§13.4): convention and error-swallow are off
		// by default — not ranked lower, off. They consume no budget and
		// never block (Category.MayBlock is false for both), so when nits
		// are disabled they are excluded from the findings list entirely.
		if !r.o.Cfg.Nits && (f.Category == model.CategoryConvention || f.Category == model.CategoryErrorSwallow) {
			drop(f, model.DropSuppressed, "nits off")
			continue
		}

		// Absence claims (§8): no span to quote, so they need their own
		// branch — a quoted anchor span that passes the cascade (checked
		// below with the ordinary evidence path) plus an explicit missing
		// assertion. When the file was sent truncated (context="partial")
		// absence claims are unsayable and the harness enforces it: the
		// model has not seen the rest of the file.
		if f.EvidenceKind == model.EvidenceAbsent {
			if fc.partial {
				drop(f, model.DropAbsenceOnPartial, "file context is partial; absence claims are unsayable")
				continue
			}
			if strings.TrimSpace(f.MissingAssertion) == "" {
				drop(f, model.DropParseFailure, "absence claim without missing_assertion")
				continue
			}
		}

		// Anchor validation (§8): anchor lines must be added-or-context
		// lines within the parsed diff hunks for this path. A line outside
		// the hunks was neither seen nor touched by this change.
		anchorOK := true
		for line := f.Anchor.StartLine; line <= f.Anchor.EndLine; line++ {
			if !fc.anchorable[line] {
				drop(f, model.DropAnchorInvalid,
					fmt.Sprintf("anchor line %d is not in the parsed diff hunks for %s", line, fc.path))
				anchorOK = false
				break
			}
		}
		if !anchorOK {
			continue
		}

		// Evidence cascade. Every quote must pass; the recorded level is
		// the worst level any quote needed.
		level := model.EvidenceExact
		evidenceOK := true
		for _, e := range f.Evidence {
			lv, err := matchCascade(e, fc.lines, fc.norm)
			if err != nil {
				if errors.Is(err, errAmbiguous) {
					drop(f, model.DropAmbiguousQuote,
						fmt.Sprintf("evidence quote at line %d matches more than one site with no line hint", e.Line))
				} else {
					drop(f, model.DropEvidenceMismatch,
						fmt.Sprintf("evidence quote at line %d does not match the post-image at any cascade level", e.Line))
				}
				evidenceOK = false
				break
			}
			if evidenceRank(lv) > evidenceRank(level) {
				level = lv
			}
		}
		if !evidenceOK {
			continue
		}

		// External-claims disposition (§8 table). path_exists /
		// symbol_exists are mechanically checked; false or unverifiable
		// drops the finding — a wrong path or symbol claim is a
		// fabrication, and a claim the harness cannot resolve never grounds
		// a block. config_key / ci_behavior / convention are note-only:
		// the finding survives as a note but may never block.
		// version_behavior was already rejected at parse time.
		claimDropped := false
		claimsOK := true // external_claims empty, or every claim verified true
		for j := range f.ExternalClaims {
			c := &f.ExternalClaims[j]
			switch c.Type {
			case model.ClaimPathExists:
				if r.o.Verifier == nil || !r.o.Verifier.PathExists(c.Subject) {
					drop(f, model.DropClaimUnverified,
						fmt.Sprintf("path_exists claim %q is not true at head", c.Subject))
					claimDropped = true
					break
				}
				t := true
				c.Verified = &t
				c.Disposition = "verified"
			case model.ClaimSymbolExists:
				if r.o.Verifier == nil || !r.o.Verifier.SymbolExists(c.Subject) {
					drop(f, model.DropClaimUnverified,
						fmt.Sprintf("symbol_exists claim %q has zero definition-shaped hits", c.Subject))
					claimDropped = true
					break
				}
				t := true
				c.Verified = &t
				c.Disposition = "verified"
			case model.ClaimConfigKey, model.ClaimCIBehavior, model.ClaimConvention:
				// Note only, never blocking: the claim cannot be resolved
				// mechanically, so "every claim verified true" can never
				// hold while it is present.
				c.Disposition = "note"
				claimsOK = false
			default:
				// Unknown types were rejected at parse time; anything left
				// is unreachable, but fail closed regardless.
				c.Disposition = "rejected"
				claimsOK = false
			}
		}
		if claimDropped {
			continue
		}

		// Blocking formula (§8), computed exactly as written:
		//
		//   blocks = category ∈ gate.blocking_categories
		//          ∧ every evidence quote matches the file
		//          ∧ the anchor is on an added line
		//          ∧ external_claims is empty, or every claim verified true
		//          ∧ confidence == "certain"
		//          ∧ the discriminative verifier returned "supported"
		blocks := f.Category.MayBlock() &&
			r.blockingSet[f.Category] &&
			evidenceOK &&
			anchorHasAddedLine(f.Anchor, fc.added) &&
			claimsOK &&
			f.Confidence == model.ConfidenceCertain

		vf := model.ValidatedFinding{Finding: *f}
		vf.Finding.Title = model.SanitizeText(f.Title)
		vf.Finding.Body = model.SanitizeText(f.Body)
		vf.Finding.Impact = model.SanitizeText(f.Impact)
		vf.Finding.MissingAssertion = model.SanitizeText(f.MissingAssertion)
		vf.Path = fc.path
		vf.EvidenceLevel = level

		if blocks && r.o.DiscVerifier != nil {
			res, err := r.o.DiscVerifier.Verify(r.runCtx, fc.path, vf.Finding)
			if err != nil {
				// Verifier failure fails open to a note, never to a block:
				// an unverifiable verdict must not become a merge blocker.
				vf.VerifierResult = "error"
				blocks = false
				r.logf("discriminative verifier error for %s/%s: %v", fc.path, f.ID, err)
			} else {
				vf.VerifierResult = res
				switch res {
				case "supported":
					// blocks stays true
				case "unsupported":
					drop(&vf.Finding, model.DropVerifierUnsupported, "discriminative verifier returned unsupported")
					continue
				default:
					// "needs-context-not-provided" (and any other answer)
					// is genuinely the right answer often — the reviewer
					// only saw one file. It cannot block; it stays a note.
					blocks = false
				}
			}
		}
		vf.Blocks = blocks

		// Occurrence disambiguates two identical findings in one file.
		fp := vf.FingerprintOf()
		vf.Fingerprint = fp
		occurrence[fp]++
		vf.Occurrence = occurrence[fp]

		out = append(out, vf)
	}
	return out, drops
}

// anchorHasAddedLine reports whether the anchor range contains at least one
// added line — the §8 requirement for blocking. The
// existing_line_made_wrong exception anchors the added line that caused the
// problem while quoting existing lines as evidence, so the anchor side is
// what matters here.
func anchorHasAddedLine(a model.Anchor, added map[int]bool) bool {
	for line := a.StartLine; line <= a.EndLine; line++ {
		if added[line] {
			return true
		}
	}
	return false
}

// evidenceRank orders cascade levels worst-to-best for recording the level
// a finding needed. token is not produced by this cascade (opt-in per
// language, notes only) and ranks below exact for completeness.
func evidenceRank(l model.EvidenceLevel) int {
	switch l {
	case model.EvidenceElided:
		return 3
	case model.EvidenceNormalized:
		return 2
	case model.EvidenceToken:
		return 1
	default:
		return 0
	}
}

// buildFileContext assembles the validator's view of one file from the run
// inputs: the post-image lines (truncated to maxFileLines with
// context="partial" when longer), the added-line marks and removed lines
// from the parsed diff, and the anchorable set.
func buildFileContext(e scope.ManifestEntry, in *Inputs) (*fileContext, *scope.EnvelopeFile) {
	lines := splitLines(in.PostImage[e.Path])
	partial := false
	if len(lines) > maxFileLines {
		lines = lines[:maxFileLines]
		partial = true
	}
	added := map[int]bool{}
	var removed []scope.RemovedLine
	anchorable := map[int]bool{}
	if df := in.Diffs[e.Path]; df != nil {
		for _, n := range df.AddedLines() {
			added[n] = true
		}
		anchorable = df.AnchorableLines()
		for _, rl := range df.RemovedLines() {
			removed = append(removed, scope.RemovedLine{OldNo: rl.OldNo, Content: rl.Content})
		}
	}
	envLines := make([]scope.EnvelopeLine, len(lines))
	for i, l := range lines {
		envLines[i] = scope.EnvelopeLine{No: i + 1, Content: l, Added: added[i+1]}
	}
	env := &scope.EnvelopeFile{
		Path:    e.Path,
		OldPath: e.OldPath,
		Status:  e.Status,
		Lines:   envLines,
		Removed: removed,
	}
	if partial {
		env.Context = "partial"
	}
	return &fileContext{
		path:       e.Path,
		lines:      lines,
		partial:    partial,
		anchorable: anchorable,
		added:      added,
		norm:       newNormalizer(lines),
	}, env
}

// splitLines splits post-image bytes into envelope lines, dropping the
// trailing newline and CR carriage returns.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// canonicalJSON marshals v with map keys sorted, so schema key order — part
// of the rendered cache prefix — is deterministic across runs (§7).
func canonicalJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	var g any
	if err := json.Unmarshal(raw, &g); err != nil {
		return raw
	}
	var b strings.Builder
	writeCanonical(&b, g)
	return []byte(b.String())
}

func writeCanonical(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStrings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			writeCanonical(b, t[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, e)
		}
		b.WriteByte(']')
	default:
		jb, err := json.Marshal(t)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(jb)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
