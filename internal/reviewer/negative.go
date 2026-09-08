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
// occurrence" — is mechanically checkable against the post-image the
// reviewer already received. The model can fabricate the absence claim
// while the file sits in memory, and a fabricated negative became a merge
// blocker (issue #45). So before a finding may block, its negative claims
// are grepped against the file: a contradicted claim drops the finding;
// a claim that cannot be verified (truncated view, no extractable symbol)
// fails open to a note — it may survive, but never block.

// negativeClaimPhrases are the narrative signals that a finding asserts a
// negative existential about file contents. Strong phrases assert the
// absence of ANY occurrence ("no other occurrence"); weak phrases assert
// the absence of a use ("never called"), where definition-shaped
// occurrences are expected and do not contradict the claim.
var negativeClaimPhrases = []string{
	// strong: any occurrence outside the quoted/anchored span falsifies
	"no other occurrence",
	"no occurrence",
	"no occurrences",
	// weak: falsified only by a call/use-shaped occurrence
	"never called",
	"never invoked",
	"never used",
	"never referenced",
	"never read",
	"never executed",
	"not called",
	"not invoked",
	"not used",
	"not referenced",
	"no callers",
	"no call site",
	"no call sites",
	"no usage",
	"is unused",
	"are unused",
}

// isStrongOccurrencePhrase reports whether the matched phrase claims the
// absence of any occurrence, not merely the absence of a call.
func isStrongOccurrencePhrase(phrase string) bool {
	return strings.Contains(phrase, "occurrence")
}

// negativeClaimPhrase returns the first negative-existence phrase present
// in the finding's narrative text, if any.
func negativeClaimPhrase(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, p := range negativeClaimPhrases {
		if strings.Contains(lower, p) {
			return p, true
		}
	}
	return "", false
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
// the finding fails open to a note.
func negativeClaimSymbols(text string) []string {
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
			rel := strings.Index(lower[idx:], p)
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
			idx = i + len(p)
		}
	}
	return out
}

var (
	callShapedRe = func(sym string) *regexp.Regexp {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(sym) + `\b[[:space:]]*\(`)
	}
	// defHeadRe matches a definition head immediately before the symbol:
	// "func name(", Go receivers ("func (r *T) name("), "def name(",
	// "function name(", "class name(" and friends. An occurrence there is
	// the definition itself, not a call.
	defHeadRe = regexp.MustCompile(`(?i)(func|def|function|fn|class|struct|interface|enum|type|sub|proc|method)\s+(\([^)]*\)\s*)?\s*$`)
)

// falsifyingOccurrence scans the post-image lines for an occurrence of the
// symbol that contradicts the negative claim. Lines the finding anchored or
// quoted are excluded — the reviewer saw those and used them as its own
// basis. It returns the 1-based line number of the first contradiction.
//
// For a strong phrase ("no other occurrence"), any word occurrence
// falsifies. For a weak call-phrase ("never called"), only a call-shaped
// occurrence — the symbol followed by "(", and not a definition head —
// falsifies, so "the definition sits in this file" cannot drop a true
// "defined but never called" finding.
func falsifyingOccurrence(lines []string, excluded map[int]bool, symbol string, strong bool) (int, bool) {
	wordRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	callRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b[[:space:]]*\(`)
	for i, line := range lines {
		n := i + 1
		if excluded[n] {
			continue
		}
		if strong {
			if wordRe.MatchString(line) {
				return n, true
			}
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
	symbols := negativeClaimSymbols(text)
	if len(symbols) == 0 {
		if partial {
			// The claim is about the full file; the reviewer saw only part
			// of it, so the absence was never actually observed (§8).
			return false, false, fmt.Sprintf("negative claim %q on a partially-sent file cannot be verified", phrase)
		}
		return false, true, ""
	}
	excluded := map[int]bool{}
	for l := f.Anchor.StartLine; l <= f.Anchor.EndLine; l++ {
		excluded[l] = true
	}
	for _, e := range f.Evidence {
		excluded[e.Line] = true
	}
	strong := isStrongOccurrencePhrase(phrase)
	for _, sym := range symbols {
		if n, hit := falsifyingOccurrence(lines, excluded, sym, strong); hit {
			return true, false, fmt.Sprintf(
				"negative claim %q is falsified: %q occurs at line %d outside the quoted span", phrase, sym, n)
		}
	}
	if partial {
		return false, false, fmt.Sprintf("negative claim %q for %q not contradicted in the visible prefix of a partially-sent file", phrase, symbols[0])
	}
	// Full file searched, no contradiction: the absence claim is true as
	// far as the file is concerned; the ordinary blocking formula applies.
	return false, true, ""
}
