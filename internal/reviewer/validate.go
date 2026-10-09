package reviewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

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
	related    []scope.RelatedSnippet // what the review call was shown from other files
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
findingsLoop:
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
		//
		// First gate (issue #60): the anchor must sit inside the reviewed
		// file's ACTUAL line range — 1..len(fc.lines). The model only ever
		// saw the post-image, so an anchor past EOF (or a non-positive start,
		// or an end before the start) references a line the model was never
		// shown. That is degraded output about ONE finding, not a schema
		// violation of the whole response: it is dropped with reason
		// anchor_out_of_range the same way anchor_invalid drops are, keeping
		// the healthy findings and the file's verdict.
		if f.Anchor.StartLine <= 0 || f.Anchor.EndLine < f.Anchor.StartLine || f.Anchor.EndLine > len(fc.lines) {
			drop(f, model.DropAnchorOutOfRange,
				fmt.Sprintf("anchor [%d,%d] is outside the reviewed file's line range (1..%d post-image lines for %s)",
					f.Anchor.StartLine, f.Anchor.EndLine, len(fc.lines), fc.path))
			continue
		}
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
		// a block. A symbol_exists subject that is not a name (see
		// symbolSubject) has nothing to search for and is note-only.
		// config_key / ci_behavior / convention are note-only:
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
				sym, ok := symbolSubject(c.Subject)
				if !ok {
					// The subject is a sentence, not a name: a toolchain
					// or language fact filed under the wrong type. A
					// definition search can neither confirm nor refute
					// it, so zero hits is no evidence of fabrication.
					// It gets the note disposition a convention claim
					// gets, and the finding can never block.
					c.Disposition = "note"
					claimsOK = false
					continue
				}
				if r.o.Verifier == nil || !r.o.Verifier.SymbolExists(sym) {
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

		// Negative-existence claims (issue #45): a narrative claim that a
		// symbol is "never called" / "no other occurrence" is mechanically
		// checkable against the post-image. A contradicted claim drops the
		// finding; a claim that cannot be checked (partial view, no
		// extractable symbol) can never ground a block — fail open to a
		// note, never to a merge blocker.
		negDrop, negVerified, negDetail := checkNegativeClaims(f, fc.lines, fc.partial)
		if negDrop {
			drop(f, model.DropNegativeClaimFalsified, negDetail)
			continue
		}

		// Self-negating findings (issue #65): a finding that says there is
		// nothing wrong contradicts itself. Across the user's repositories
		// such findings were posted as comments at every confidence, about
		// one in fifty ("Impact: None.", "No defect in new-line rule"), and
		// each one costs a reader the time to establish that it says
		// nothing. So the rule covers every finding, not only blocking
		// candidates. It stays narrow in what it reads: the impact field
		// must OPEN with the disclaimer, and the title must open or close
		// with one, so a sentence that merely contains "no" or "impact" or
		// "correct" is never caught. Runs AFTER the negative-claims check so
		// a finding that both fabricates a claim about the file and
		// disclaims impact is recorded under the stronger reason,
		// negative_claim_falsified.
		if why := selfNegation(f); why != "" {
			drop(f, model.DropSelfNegating, why)
			continue findingsLoop
		}

		// Blocking formula (§8), computed exactly as written and spelled
		// once for the whole product, in the model package:
		//
		//   blocks = category ∈ gate.blocking_categories
		//          ∧ every evidence quote matches the file
		//          ∧ the anchor is on an added line
		//          ∧ external_claims is empty, or every claim verified true
		//          ∧ confidence == "certain"
		//          ∧ the discriminative verifier returned "supported"
		//
		// The sixth conjunct is applied immediately below rather than
		// inside the conjunction: an "unsupported" verdict drops the
		// finding from the record, which is not a boolean this expression
		// can fold in, and the verifier is asked about every finding a
		// reader would see, blocking or not.
		// With no DiscriminativeVerifier (CITE_VERIFY=0) the conjunct is
		// not applied, and each finding's verifier_result stays empty.
		blocks := model.BlockingCandidate(model.BlockInputs{
			Category:               f.Category,
			BlockingSet:            r.blockingSet,
			EvidenceMatches:        evidenceOK,
			AnchorOnAddedLine:      anchorHasAddedLine(f.Anchor, fc.added),
			ClaimsVerified:         claimsOK,
			NegativeClaimsVerified: negVerified,
			Confidence:             f.Confidence,
		})

		vf := model.ValidatedFinding{Finding: *f}
		vf.Finding.Title = model.SanitizeText(f.Title)
		vf.Finding.Body = model.SanitizeText(f.Body)
		vf.Finding.Impact = model.SanitizeText(f.Impact)
		vf.Finding.MissingAssertion = model.SanitizeText(f.MissingAssertion)
		vf.Path = fc.path
		vf.EvidenceLevel = level

		// The verifier pass runs on every finding a reader would see, not
		// only on blocking candidates: a wrong note costs a reader the same
		// time as a wrong block, and half the notes people replied to were
		// wrong. Convention findings are questions about the repository's
		// own rules, which no trace through the code can settle.
		if r.o.DiscVerifier != nil && f.Category != model.CategoryConvention {
			res, why, err := r.o.DiscVerifier.Verify(VerifyInput{
				Path: fc.path, Finding: vf.Finding, Lines: fc.lines, Added: fc.added, Related: fc.related,
			})
			if err != nil {
				// Verifier failure fails open to a note, never to a block:
				// an unverifiable verdict must not become a merge blocker.
				vf.VerifierResult = string(VerifierError)
				blocks = false
				r.logf("verifier error for %s/%s: %v", fc.path, f.ID, err)
			} else {
				vf.VerifierResult = string(res)
				switch res {
				case VerifierSupported:
					// blocks stays as computed
				case VerifierUnsupported:
					drop(&vf.Finding, model.DropVerifierUnsupported, model.SanitizeText(why))
					continue
				default:
					// VerifierNeedsContextNotProvided (and any other
					// answer) is genuinely the right answer often — the
					// reviewer only saw one file. It cannot block; it
					// stays a note.
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
		Prior:   in.PriorThreads[e.Path],
	}
	if in.Related != nil {
		for _, sn := range in.Related.Related(e.Path, lines, added) {
			env.Related = append(env.Related, scope.RelatedSnippet{
				Path: sn.Path, Symbol: sn.Symbol, Kind: string(sn.Kind),
				StartLine: sn.StartLine, Lines: sn.Lines,
			})
		}
	}
	if partial {
		env.Context = "partial"
	}
	return &fileContext{
		related:    env.Related,
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

// impactDisclaimers open an impact field that says the finding has none.
var impactDisclaimers = []string{
	"no impact", "no defect", "no mismatch", "no functional", "no runtime", "no behavioral",
	"no behavioural", "no user-visible", "no observable", "no practical", "not a defect", "not a bug",
	"no wrong outcome", "no incorrect", "nothing breaks", "nothing is wrong",
}

// titleDisclaimerPrefixes and titleDisclaimerSuffixes bound a title that
// says the code is fine.
var (
	titleDisclaimerPrefixes = []string{"no defect", "no issue", "no bug", "no problem", "no mismatch", "not a defect", "not a bug"} // plurals share the prefix
	titleDisclaimerSuffixes = []string{" is correct", " are correct", " is fine", " are fine", " works as intended", " no change needed"}
)

// selfNegation returns why f disclaims its own defect, or "" when it does
// not. "None" counts only as the whole first clause, so "None of the
// retries run" is still an impact, and a disclaimer that the impact goes on
// to contradict ("No impact on the caller, but the retry budget is now
// shared") keeps the finding.
func selfNegation(f *model.Finding) string {
	imp := strings.ToLower(strings.TrimLeft(strings.TrimSpace(f.Impact), "*_ "))
	if strings.Contains(imp, " but ") || strings.Contains(imp, " however") {
		return titleNegation(f)
	}
	for _, pre := range impactDisclaimers {
		if strings.HasPrefix(imp, pre) {
			return fmt.Sprintf("impact field disclaims a defect: %q", model.SanitizeText(f.Impact))
		}
	}
	for _, bare := range []string{"none", "n/a"} {
		rest, ok := strings.CutPrefix(imp, bare)
		if ok && (rest == "" || !unicode.IsLetter([]rune(rest)[0]) && rest[0] != ' ' || strings.HasPrefix(rest, " —") || strings.HasPrefix(rest, " -")) {
			return fmt.Sprintf("impact field disclaims a defect: %q", model.SanitizeText(f.Impact))
		}
	}
	return titleNegation(f)
}

// titleNegation is the title half of selfNegation.
func titleNegation(f *model.Finding) string {
	title := strings.ToLower(strings.TrimRight(strings.TrimSpace(f.Title), ".!"))
	for _, pre := range titleDisclaimerPrefixes {
		if strings.HasPrefix(title, pre) {
			return fmt.Sprintf("title disclaims a defect: %q", model.SanitizeText(f.Title))
		}
	}
	for _, suf := range titleDisclaimerSuffixes {
		if strings.HasSuffix(title, suf) {
			return fmt.Sprintf("title disclaims a defect: %q", model.SanitizeText(f.Title))
		}
	}
	return ""
}

// symbolName is the shape a definition search can check: an identifier,
// optionally qualified with "." or "::" (pkg.Func, Type::method).
var symbolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:(?:\.|::)[A-Za-z_][A-Za-z0-9_]*)*$`)

// symbolSubject returns the name a symbol_exists claim asks about, with
// the decoration a model adds around a name (backticks, a trailing "()")
// removed. ok is false when the subject is not a name at all, such as a
// sentence about how a tool behaves: no definition search can check it.
func symbolSubject(subject string) (name string, ok bool) {
	s := strings.TrimSpace(subject)
	s = strings.TrimSpace(strings.Trim(s, "`"))
	s = strings.TrimSuffix(s, "()")
	if !symbolName.MatchString(s) {
		return "", false
	}
	return s, true
}
