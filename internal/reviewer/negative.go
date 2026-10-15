package reviewer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// Negative-existence claim verification (issue #45). A finding whose
// narrative asserts a negative existential statement about the reviewed
// file — "X is imported but never called", "the full file contains no other
// occurrence", "X is undefined" — is mechanically checkable against the
// post-image the reviewer already received. The model can fabricate the
// absence claim while the file sits in memory, and a fabricated negative
// became a merge blocker (issue #45). So before a finding may block, its
// negative claims are grepped against the file: a contradicted claim drops
// the finding; a claim that cannot be verified (truncated view, no
// extractable symbol) fails open to a note — it may survive, but never
// block.
//
// Each phrase carries the occurrence shape it denies, because a claim is
// falsified only by the shape it is about: a definition answers "X is
// undefined", a call site answers "X is never called". Reading one shape
// for the other is what let a finding call a shell helper undefined while
// quoting that helper's own definition as its evidence, and block on it
// (issue #201).

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

// checkNegativeClaims runs the issue-#45 verification for one finding. It
// returns (drop, detail): drop=true means the finding is mechanically
// falsified and must be dropped; drop=false with verified=false means the
// claim could not be checked and the finding must not block. verified=true
// means the claim was checked (or is absent) and the ordinary formula
// applies.
func checkNegativeClaims(f *model.Finding, lines []string, partial bool) (drop bool, verified bool, detail string) {
	text := strings.Join([]string{f.Title, f.Body, f.Impact, f.MissingAssertion}, " ")
	phrase, ok := negativeClaimPhrase(text)
	if !ok {
		return false, true, ""
	}
	symbols := negativeClaimSymbols(text, phrase)
	if len(symbols) == 0 {
		// No symbol could be extracted, so the claim cannot be checked
		// against the file at all — the absence was never mechanically
		// observed, whatever the file's length. Fail open: the finding
		// may survive as a note but can never ground a blocker.
		if partial {
			return false, false, fmt.Sprintf("negative claim %q on a partially-sent file cannot be verified", phrase.phrase)
		}
		return false, false, fmt.Sprintf("negative claim %q has no extractable symbol to verify against", phrase.phrase)
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
			return true, false, fmt.Sprintf(
				"negative claim %q is falsified: %q %s at line %d", phrase.phrase, sym, falsifiedShape(phrase.shape), n)
		}
	}
	if partial {
		return false, false, fmt.Sprintf("negative claim %q for %q not contradicted in the visible prefix of a partially-sent file", phrase.phrase, symbols[0])
	}
	// Full file searched, no contradiction: the absence claim is true as
	// far as the file is concerned; the ordinary blocking formula applies.
	return false, true, ""
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
