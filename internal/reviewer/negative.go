package reviewer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// Absence-claim verification (issues #45, #201, #203).
//
// A finding whose claim is a negative existential — "X is imported but
// never called", "the full file contains no other occurrence", "X is
// undefined" — is mechanically checkable against the post-image the
// reviewer already received. The model can fabricate the absence while the
// file sits in memory, and a fabricated absence became a merge blocker
// (issue #45). So before a finding may block, its absence claims are
// checked against the file: a contradicted claim drops the finding; a
// claim that cannot be verified (truncated view, no extractable symbol)
// fails open to a note — it may survive, but never block.
//
// The shape of the claim comes from the model, not from English.
//
// Issue #203: the guard used to recover the shape of the absence from the
// finding's prose, by substring-matching a table of English phrases
// (negativeClaimPhrases) and guessing the subject from the token before the
// phrase (subjectStopWords). That made coverage a function of the
// narrative vocabulary, and the direction that failed was the dangerous
// one: a missing phrase meant the ordinary blocking formula applied to an
// absence claim nobody had checked. It only ever worked in English, and
// every fix was a new word — #45, #65, #68 and #202 all closed by widening
// the table.
//
// So the absence shapes are now claim types the model declares
// (model.ClaimSymbolNotDefined, model.ClaimSymbolNotCalled,
// model.ClaimOccurrenceCountZero), with the subject carried as a structured
// field (symbol, path). The mechanical check runs on the declared type and
// the declared subject, in every language.
//
// The phrase tables stay, as a fallback, and the fallback is strictly
// weaker than the declared claim. It keeps the falsifying power it always
// had — a phrase-matched claim the file contradicts is dropped — and it
// loses the power to LET a claim block: a phrase-matched absence that no
// declared claim covers was never mechanically observed, so the finding
// fails open to a note.
//
// The two directions are therefore separated:
//
//   - a claim the file contradicts is dropped, declared or not;
//   - an absence that was never declared cannot block, however it is
//     phrased and in whatever language it is phrased.
//
// The second is the point of issue #203: a finding that asserts an absence
// and declares no claim is not mechanically checkable, and an unverifiable
// absence must never block a merge.

// claimShape is the occurrence shape a negative claim denies, and therefore
// the only shape that can falsify it.
type claimShape int

const (
	// shapeOccurrence: any word occurrence.
	shapeOccurrence claimShape = iota
	// shapeCall: a call or use site. A definition does not falsify, or every
	// true "defined but never called" finding would falsify itself.
	shapeCall
	// shapeDefinition: a definition. A bare call site does not falsify, or
	// every true "called but never defined" finding would falsify itself.
	shapeDefinition
)

// negativePhrase is one narrative signal that a finding asserts a negative
// existential about file contents, with the occurrence shape that answers
// it.
type negativePhrase struct {
	phrase string
	shape  claimShape
}

// negativeClaimPhrases are the narrative signals, grouped by the shape that
// falsifies them.
//
// FROZEN. This table is a fallback, not a mechanism: a match here can only
// downgrade a finding to a note, never let an absence claim block (issue
// #203). Adding a word to it does not make a claim checkable, so a word
// added here buys nothing and hides the real gap: the model did not declare
// the claim.
var negativeClaimPhrases = []negativePhrase{
	// any occurrence outside the quoted/anchored span falsifies
	{"no other occurrence", shapeOccurrence},
	{"no occurrence", shapeOccurrence},
	{"no occurrences", shapeOccurrence},
	// falsified by a call/use-shaped occurrence
	{"never called", shapeCall},
	{"never invoked", shapeCall},
	{"never used", shapeCall},
	{"never referenced", shapeCall},
	{"never read", shapeCall},
	{"never executed", shapeCall},
	{"not called", shapeCall},
	{"not invoked", shapeCall},
	{"not used", shapeCall},
	{"not referenced", shapeCall},
	{"no callers", shapeCall},
	{"no call site", shapeCall},
	{"no call sites", shapeCall},
	{"no usage", shapeCall},
	{"is unused", shapeCall},
	{"are unused", shapeCall},
	// falsified by a definition: a symbol the finding says does not exist
	// while the file defines it (issue #201, in a shell test helper). The
	// copula belongs to the phrase so the subject sits immediately before
	// it: with "not defined" alone, "notok is not defined" would offer
	// "is" as the symbol, and subjectBefore would find nothing to search
	// for. That is why the longer "is never defined" / "is not defined"
	// forms come first: the first match in the list wins.
	{"is undefined", shapeDefinition},
	{"are undefined", shapeDefinition},
	{"is never defined", shapeDefinition},
	{"are never defined", shapeDefinition},
	{"is not defined", shapeDefinition},
	{"are not defined", shapeDefinition},
	{"was not defined", shapeDefinition},
	{"were not defined", shapeDefinition},
	{"was never defined", shapeDefinition},
	{"never defined", shapeDefinition},
	{"not defined", shapeDefinition},
	{"has no definition", shapeDefinition},
	{"have no definition", shapeDefinition},
	{"not declared", shapeDefinition},
	{"never declared", shapeDefinition},
}

// negativeClaimPhrase returns the first negative-existence phrase present
// in the finding's narrative text, if any, with the shape that falsifies it.
func negativeClaimPhrase(text string) (negativePhrase, bool) {
	lower := strings.ToLower(text)
	for _, p := range negativeClaimPhrases {
		if strings.Contains(lower, p.phrase) {
			return p, true
		}
	}
	return negativePhrase{}, false
}

var (
	backtickSpanRe = regexp.MustCompile("`([^`\n]+)`")
	identTokenRe   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.]*`)
	// identLikeRe distinguishes identifier-shaped candidates from plain
	// English words: an identifier has an uppercase letter, a digit, an
	// underscore, or a dot-qualified path. Plain lowercase words are only
	// candidates when the model quoted them in backticks.
	identLikeRe = regexp.MustCompile(`[A-Z_0-9]|\w\.\w`)
)

// negativeClaimSymbols extracts the symbol candidates a negative claim is
// about: backtick-quoted spans first, then identifier tokens appearing in
// the text immediately before the phrase. Conservative by design — when no
// candidate extracts, the claim simply cannot be mechanically checked and
// the finding fails open to a note. A definition-absence claim adds the
// token immediately before the phrase (see subjectBefore), which the
// identifier-shape filter would otherwise drop for an all-lowercase shell
// helper.
func negativeClaimSymbols(text string, matched negativePhrase) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(s) < 2 || strings.ContainsAny(s, " \t\n") {
			return
		}
		if seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range backtickSpanRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	lower := strings.ToLower(text)
	for _, p := range negativeClaimPhrases {
		idx := 0
		for {
			rel := strings.Index(lower[idx:], p.phrase)
			if rel < 0 {
				break
			}
			i := idx + rel
			seg := text[idx:i]
			if len(seg) > 80 {
				seg = seg[len(seg)-80:]
			}
			for _, tok := range identTokenRe.FindAllString(seg, -1) {
				if identLikeRe.MatchString(tok) {
					add(tok)
				}
			}
			idx = i + len(p.phrase)
		}
	}
	if matched.shape == shapeDefinition {
		if subject, ok := subjectBefore(text, matched.phrase); ok {
			add(subject)
		}
	}
	return out
}

// subjectBefore returns the token immediately before phrase: the subject of
// a sentence such as "notok is undefined", which no English word separates
// from the phrase. A plain lowercase name is returned, because shell
// helpers are named that way and identLikeRe rejects them for the other
// phrases, where a lowercase word before the phrase is usually prose ("the
// loop is never called"). Filler words return nothing: they name no symbol,
// and a candidate that is not a symbol would only make the definition
// search answer a question nobody asked.
func subjectBefore(text, phrase string) (string, bool) {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(phrase))
	if idx <= 0 {
		return "", false
	}
	segment := text[:idx]
	if len(segment) > 80 {
		segment = segment[len(segment)-80:]
	}
	tokens := identTokenRe.FindAllString(segment, -1)
	if len(tokens) == 0 {
		return "", false
	}
	last := tokens[len(tokens)-1]
	if subjectStopWords[strings.ToLower(last)] {
		return "", false
	}
	return last, true
}

// subjectStopWords sits where a symbol would sit and names no symbol, so no
// search should be run for it: the words of "the function is not defined",
// where the subject of the sentence is somewhere else or absent.
//
// FROZEN, like the phrase table: this list only feeds the fallback
// downgrade, so a word added here buys no coverage of any claim that
// matters.
var subjectStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "be": true,
	"because": true, "been": true, "but": true, "code": true, "command": true,
	"function": true, "had": true, "has": true, "have": true, "is": true,
	"it": true, "its": true, "method": true, "name": true, "never": true,
	"no": true, "not": true, "or": true, "so": true, "symbol": true,
	"that": true, "the": true, "there": true, "this": true, "value": true,
	"variable": true, "was": true, "were": true, "which": true,
	"with": true, "without": true,
}

var (
	// defHeadRe matches a definition head immediately before the symbol:
	// "func name(", Go receivers ("func (r *T) name("), "def name(",
	// "function name(", "class name(" and friends. An occurrence there is
	// the definition itself, not a call.
	defHeadRe = regexp.MustCompile(`(?i)(func|def|function|fn|class|struct|interface|enum|type|sub|proc|method)\s+(\([^)]*\)\s*)?\s*$`)
)

// falsifyingOccurrence scans the post-image lines for an occurrence of the
// symbol in the shape the claim denies, and returns the 1-based line number
// of the first one.
//
// The lines the finding anchored or quoted are excluded only for the
// occurrence shape: a true "no other occurrence" claim quotes the one
// occurrence it is about as its basis, so without the exclusion it would
// falsify itself. A call claim gains nothing from that exclusion (issue
// #68) — an import line is neither call-shaped nor a definition head — and
// the exclusion only hides a fabricated claim that quotes its own
// contradicting call site. A definition claim ignores it too, and has to:
// issue #201's finding quoted the definition of the very symbol it called
// undefined as its own evidence, so an exclusion there would hide exactly
// the falsifier this rule exists for.
func falsifyingOccurrence(lines []string, excluded map[int]bool, symbol string, shape claimShape) (int, bool) {
	if shape == shapeDefinition {
		return definitionOccurrence(lines, symbol)
	}
	if shape == shapeOccurrence {
		wordRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
		for i, line := range lines {
			n := i + 1
			if excluded[n] {
				continue
			}
			if wordRe.MatchString(line) {
				return n, true
			}
		}
		return 0, false
	}
	callRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b[[:space:]]*\(`)
	for i, line := range lines {
		n := i + 1
		if excluded[n] {
			continue
		}
		for _, loc := range callRe.FindAllStringIndex(line, -1) {
			prefix := line[:loc[0]]
			if defHeadRe.MatchString(prefix) {
				continue
			}
			return n, true
		}
	}
	return 0, false
}

// definitionOccurrence scans the post-image for an occurrence that DEFINES
// symbol, the shape an "is undefined" claim denies: a declaration keyword
// before the name, a function body after it (notok() {), or an assignment
// to it. A bare call is not a definition. "foo is undefined" about an
// undefined foo the code calls is the finding a reviewer gets right, and it
// must not be falsified by its own call site, which is the one line it can
// quote.
func definitionOccurrence(lines []string, symbol string) (int, bool) {
	s := regexp.QuoteMeta(symbol)
	head := regexp.MustCompile(`(?i)(^|[^[:alnum:]_.])(func|def|function|fn|sub|proc|method|class|struct|interface|enum|type|const|let|var|local|declare)[[:space:]]+(\([^)]*\)[[:space:]]+)?` + s + `\b`)
	body := regexp.MustCompile(`(^|[^[:alnum:]_.])` + s + `[[:space:]]*\(\)[[:space:]]*\{`)
	assign := regexp.MustCompile(`(^|[^[:alnum:]_.])` + s + `[[:space:]]*:?=[^=>]`)
	for i, line := range lines {
		if head.MatchString(line) || body.MatchString(line) || assign.MatchString(line) {
			return i + 1, true
		}
	}
	return 0, false
}

// absenceResult is the outcome of checking a finding's absence claims.
type absenceResult struct {
	// Drop is true when an absence claim was mechanically falsified, so
	// the finding is a false claim and must not be published.
	Drop bool
	// Verified is true when every absence claim on the finding was
	// checked against the reviewed content and held. A claim that could
	// not be checked leaves this false, and the finding can never ground
	// a block.
	Verified bool
	// Detail explains the outcome, for the drop log.
	Detail string
	// Claims holds the absence claims, each with its Verified flag and
	// Disposition filled in, so the caller can record them on the
	// finding.
	Claims []model.ExternalClaim
}

// checkAbsenceClaims runs the absence-claim verification for one finding
// (issues #45, #201, #203).
//
// The declared claims are the mechanism: a symbol_not_defined /
// symbol_not_called / occurrence_count_zero claim is checked mechanically,
// on its own type and its own structured subject. A falsified declared
// claim DROPS the finding, because a claim the file contradicts is a false
// claim, and a note that names a helper the file defines is still a false
// claim published, only quieter.
//
// The narrative phrase tables are the fallback, and the fallback is
// strictly weaker. It keeps the falsifying power it always had — a
// phrase-matched claim the file contradicts is dropped — and it loses the
// power to LET a claim block: a phrase-matched absence that no declared
// claim covers was never mechanically observed, so the finding fails open
// to a note.
//
// The two directions are therefore separated:
//
//   - a claim the file contradicts is dropped, declared or not;
//   - an absence that was never declared cannot block, however it is
//     phrased and in whatever language it is phrased.
//
// The second is the point of issue #203: a finding that asserts an absence
// and declares no claim is not mechanically checkable, and an unverifiable
// absence must never block a merge.
func checkAbsenceClaims(f *model.Finding, path string, lines []string, partial bool) absenceResult {
	declared := declaredAbsenceClaims(f)
	if len(declared) > 0 {
		// The declared claim is the mechanism, so the prose fallback does
		// not run: the model said what it claimed, and that is what gets
		// checked.
		verified := true
		out := make([]model.ExternalClaim, 0, len(declared))
		for _, c := range declared {
			drop, ok, detail := checkDeclaredAbsenceClaim(c, path, f, lines, partial)
			if drop {
				return absenceResult{Drop: true, Detail: detail}
			}
			if ok {
				t := true
				c.Verified = &t
				c.Disposition = "verified"
			} else {
				verified = false
				c.Disposition = "note"
			}
			out = append(out, c)
		}
		return absenceResult{Verified: verified, Claims: out}
	}

	// No declared claim. The finding may still assert an absence, and the
	// fallback finds it in the prose.
	res := checkProseAbsenceClaim(f, lines, partial)
	if res.Drop {
		return res
	}
	// The structural marker: evidence_kind "absent" says the finding's
	// mechanism IS an absence. It is a field, not a phrase, so it holds in
	// every language: this is the gate that closes the gap the English
	// phrase table leaves. Such a finding needs a declared claim to be
	// checkable, and without one it cannot block.
	//
	// A non-empty missing_assertion says the same thing, and it is
	// checked too: the field is where the model writes what is missing,
	// so a finding that filled it in asserted an absence whether or not
	// it also set evidence_kind. Leaving the marker out of the check
	// would make the requirement depend on a second field being
	// remembered.
	if declaresAbsence(f) {
		res.Verified = false
		res.Detail = "the finding asserts an absence and declares no absence claim, " +
			"so it cannot be verified and cannot block"
	}
	return res
}

// declaresAbsence reports whether the finding says, in a field rather than
// in prose, that its mechanism is an absence.
//
// Two fields say so, and both are checked because a model that writes one
// of them has asserted an absence:
//
//   - evidence_kind "absent", the branch the schema defines for a claim
//     that has no span to quote;
//   - a non-empty missing_assertion, the field that says what is missing.
//
// Neither depends on the language the finding is written in, which is the
// whole point (issue #203): the English phrase table cannot tell an
// absence from a presence in French.
func declaresAbsence(f *model.Finding) bool {
	return f.EvidenceKind == model.EvidenceAbsent || strings.TrimSpace(f.MissingAssertion) != ""
}

// declaredAbsenceClaims returns the absence claims the model declared.
func declaredAbsenceClaims(f *model.Finding) []model.ExternalClaim {
	var out []model.ExternalClaim
	for _, c := range f.ExternalClaims {
		if c.Type.IsAbsenceClaim() {
			out = append(out, c)
		}
	}
	return out
}

// checkDeclaredAbsenceClaim checks one declared absence claim mechanically
// and reports whether it is falsified and whether it was checkable.
//
// The check is on the type and the structured subject, never on prose:
//
//   - symbol_not_defined    falsified by a definition of the symbol
//   - symbol_not_called     falsified by a call of the symbol
//   - occurrence_count_zero falsified by any occurrence of the symbol
//
// A claim with no usable subject cannot be checked at all. It never drops
// the finding (an uncheckable claim is not a falsified one) and it never
// blocks it: the finding falls open to a note.
func checkDeclaredAbsenceClaim(c model.ExternalClaim, path string, f *model.Finding, lines []string, partial bool) (drop bool, verified bool, detail string) {
	sym, ok := symbolSubject(absenceClaimSymbol(c))
	if !ok {
		// The subject is not a name. The definition search can neither
		// confirm nor refute it, so zero hits is no evidence of
		// fabrication: the finding is not dropped, and the claim earns
		// the note disposition.
		return false, false, fmt.Sprintf("%s claim %q names no symbol, so it cannot be checked", c.Type, c.Symbol)
	}
	if c.Type == model.ClaimOccurrenceCountZero && c.Count != nil && *c.Count != 0 {
		// The type says the count is zero and the count field says
		// otherwise: the claim contradicts itself, so there is nothing
		// to check and nothing to trust. Note, never a block.
		return false, false, fmt.Sprintf("%s claim %q declares a count of %d, want 0", c.Type, c.Symbol, *c.Count)
	}
	if c.Path != "" && c.Path != path {
		// The claim is about another file, and Cite checked the file
		// under review. Zero hits in the reviewed file say nothing about
		// the other one, so the claim cannot be checked here: note, never
		// a block.
		return false, false, fmt.Sprintf("%s claim %q is about %q, not the file under review", c.Type, c.Symbol, c.Path)
	}
	shape, ok := absenceClaimShape(c.Type)
	if !ok {
		return false, false, fmt.Sprintf("%s claim has no checkable shape", c.Type)
	}
	// The quoted/anchored spans are excluded from the falsification scan
	// only for the occurrence shape (issue #68); see falsifyingOccurrence
	// for why the other two shapes must not exclude them.
	excluded := map[int]bool{}
	if shape == shapeOccurrence {
		for l := f.Anchor.StartLine; l <= f.Anchor.EndLine; l++ {
			excluded[l] = true
		}
		for _, e := range f.Evidence {
			excluded[e.Line] = true
		}
	}
	if n, hit := falsifyingOccurrence(lines, excluded, sym, shape); hit {
		return true, false, fmt.Sprintf(
			"%s claim %q is falsified: %q %s at line %d", c.Type, c.Symbol, sym, falsifiedShape(shape), n)
	}
	if partial {
		// The file was sent truncated, so an absence about the whole file
		// was never actually observed. The claim cannot be confirmed
		// either: it fails open to a note, never to a block.
		return false, false, fmt.Sprintf("%s claim %q not contradicted in the visible prefix of a partially-sent file", c.Type, c.Symbol)
	}
	return false, true, ""
}

// absenceClaimSymbol returns the symbol an absence claim is about. The
// structured field is the subject of the claim; the generic subject string
// is the fallback for a model that filled only that one.
func absenceClaimSymbol(c model.ExternalClaim) string {
	if s := strings.TrimSpace(c.Symbol); s != "" {
		return s
	}
	return strings.TrimSpace(c.Subject)
}

// absenceClaimShape maps a declared absence-claim type to the occurrence
// shape that falsifies it.
func absenceClaimShape(t model.ClaimType) (claimShape, bool) {
	switch t {
	case model.ClaimSymbolNotDefined:
		return shapeDefinition, true
	case model.ClaimSymbolNotCalled:
		return shapeCall, true
	case model.ClaimOccurrenceCountZero:
		return shapeOccurrence, true
	}
	return shapeOccurrence, false
}

// checkProseAbsenceClaim is the fallback: the narrative phrase tables
// (issue #45, #201), now demoted to a path that can never let a claim
// block.
//
// A phrase match still identifies the shape of the absence and still finds
// the subject the old way, so a claim the file contradicts is still
// dropped. What it no longer does is grant "verified": a phrase-matched
// absence that no declared claim covers was never mechanically observed by
// Cite, so the finding survives as a note and can never ground a blocker.
//
// That is the change issue #203 asks for, and it is the whole point of it:
// outside English no phrase matches, so the guard used to be absent rather
// than weaker, and an unverified absence claim blocked a merge.
func checkProseAbsenceClaim(f *model.Finding, lines []string, partial bool) absenceResult {
	text := strings.Join([]string{f.Title, f.Body, f.Impact, f.MissingAssertion}, " ")
	phrase, ok := negativeClaimPhrase(text)
	if !ok {
		return absenceResult{Verified: true}
	}
	symbols := negativeClaimSymbols(text, phrase)
	if len(symbols) == 0 {
		// No symbol could be extracted, so the claim cannot be checked
		// against the file at all — the absence was never mechanically
		// observed, whatever the file's length. Fail open: the finding
		// may survive as a note but can never ground a blocker.
		if partial {
			return absenceResult{Verified: false,
				Detail: fmt.Sprintf("negative claim %q on a partially-sent file cannot be verified", phrase.phrase)}
		}
		return absenceResult{Verified: false,
			Detail: fmt.Sprintf("negative claim %q has no extractable symbol to verify against", phrase.phrase)}
	}
	// Quoted/anchored spans are excluded from the falsification scan only
	// for occurrence-phrases (issue #68); see falsifyingOccurrence for why
	// the other two shapes must not exclude them.
	excluded := map[int]bool{}
	if phrase.shape == shapeOccurrence {
		for l := f.Anchor.StartLine; l <= f.Anchor.EndLine; l++ {
			excluded[l] = true
		}
		for _, e := range f.Evidence {
			excluded[e.Line] = true
		}
	}
	for _, sym := range symbols {
		if n, hit := falsifyingOccurrence(lines, excluded, sym, phrase.shape); hit {
			return absenceResult{Drop: true, Detail: fmt.Sprintf(
				"negative claim %q is falsified: %q %s at line %d", phrase.phrase, sym, falsifiedShape(phrase.shape), n)}
		}
	}
	// No contradiction. The claim is not falsified, but it was never
	// checked either: it was asserted in prose, so it fails open to a
	// note (issue #203). A declared claim of the same shape would be
	// checked and could still block.
	return absenceResult{Verified: false, Detail: fmt.Sprintf(
		"negative claim %q is asserted in prose with no declared claim, so it cannot be verified and cannot block",
		phrase.phrase)}
}

// falsifiedShape names, for the drop detail, what the falsifying occurrence
// is: the shape the claim denied.
func falsifiedShape(shape claimShape) string {
	switch shape {
	case shapeDefinition:
		return "is defined"
	case shapeCall:
		return "is called"
	default:
		return "occurs"
	}
}
