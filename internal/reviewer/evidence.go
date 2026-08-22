package reviewer

import (
	"errors"
	"strings"
	"unicode"

	"github.com/elecnix/cite/internal/model"
)

// The evidence cascade (PLAN.md §8, "Evidence discipline, as a cascade rather
// than a rule"). Every evidence {line, quote} pair is matched against the
// post-image the model was actually shown (truncation included), and the
// recorded level is published on the finding:
//
//	exact       byte substring of the post-image line at the given line
//	normalized  both sides through one documented normaliser (below)
//	elided      quote split on an ellipsis marker; every segment matches at
//	            `normalized`, in order, spanning ≤ 40 lines
//	fail        dropped, and logged with its reason — never re-asked. Asking
//	            the model to re-quote converts a detectable failure into an
//	            undetectable one: it has the file in context and will produce
//	            a matching quote for the same wrong claim.
//
// Supporting rules enforced here: a quote must contain ≥ 12 bytes and at
// least one non-whitespace, non-punctuation token (a model quoting "}"
// matches everywhere); more than one match site with no line hint is
// ambiguous and drops.

var (
	errNoMatch   = errors.New("quote does not match the post-image")
	errAmbiguous = errors.New("quote matches more than one site with no line hint")
)

const (
	minQuoteBytes      = 12
	maxElidedSpanLines = 40
)

// matchCascade runs one evidence pair through the cascade. It returns the
// recorded level on success; on failure it returns model.EvidenceFailed and
// either errAmbiguous (drop with DropAmbiguousQuote) or errNoMatch (drop
// with DropEvidenceMismatch).
func matchCascade(e model.Evidence, lines []string, n *normalizer) (model.EvidenceLevel, error) {
	q := e.Quote
	if len(q) < minQuoteBytes {
		return model.EvidenceFailed, errNoMatch
	}
	if !hasContentToken(q) {
		return model.EvidenceFailed, errNoMatch
	}
	hinted := e.Line >= 1 && e.Line <= len(lines)
	noHint := e.Line <= 0

	// Level 1: exact — byte substring of the post-image line.
	if hinted && strings.Contains(lines[e.Line-1], q) {
		return model.EvidenceExact, nil
	}
	if noHint {
		sites := matchSites(lines, func(l string) bool { return strings.Contains(l, q) })
		if len(sites) == 1 {
			return model.EvidenceExact, nil
		}
		if len(sites) > 1 {
			return model.EvidenceFailed, errAmbiguous
		}
	}

	// Level 2: normalized — both sides through the documented normaliser.
	if nq := n.norm(q); nq != "" {
		if hinted && strings.Contains(n.norm(lines[e.Line-1]), nq) {
			return model.EvidenceNormalized, nil
		}
		if noHint {
			sites := matchSites(lines, func(l string) bool { return strings.Contains(n.norm(l), nq) })
			if len(sites) == 1 {
				return model.EvidenceNormalized, nil
			}
			if len(sites) > 1 {
				return model.EvidenceFailed, errAmbiguous
			}
		}
	}

	// Level 3: elided — segments match in order across ≤ 40 lines.
	if segs := splitElision(q); len(segs) >= 2 {
		if _, _, ok := matchElided(segs, lines, n); ok {
			return model.EvidenceElided, nil
		}
	}

	return model.EvidenceFailed, errNoMatch
}

// matchSites returns the indices of lines where f holds.
func matchSites(lines []string, f func(string) bool) []int {
	var out []int
	for i, l := range lines {
		if f(l) {
			out = append(out, i)
		}
	}
	return out
}

// splitElision splits a quote on an ellipsis marker ("..." or "…") into
// trimmed, non-empty segments. Fewer than two non-empty segments means the
// quote is not elided and the level does not apply.
func splitElision(q string) []string {
	q = strings.ReplaceAll(q, "…", "...")
	parts := strings.Split(q, "...")
	var segs []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

// matchElided walks the segments in order through the normalised lines,
// greedily: each segment must appear on some line at or after the previous
// segment's line (segments never share a line), and the whole span must
// cover ≤ maxElidedSpanLines line steps.
func matchElided(segs []string, lines []string, n *normalizer) (first, last int, ok bool) {
	normed := make([]string, len(lines))
	for i, l := range lines {
		normed[i] = n.norm(l)
	}
	cursor := 0
	for _, s := range segs {
		ns := n.norm(s)
		if ns == "" {
			return 0, 0, false
		}
		found := -1
		for i := cursor; i < len(normed); i++ {
			if strings.Contains(normed[i], ns) {
				found = i
				break
			}
		}
		if found < 0 {
			return 0, 0, false
		}
		if first == 0 && last == 0 {
			first = found
		}
		last = found
		cursor = found + 1
	}
	if last-first > maxElidedSpanLines {
		return 0, 0, false
	}
	return first, last, true
}

// hasContentToken reports whether s contains at least one rune that is
// neither whitespace nor punctuation nor a symbol. A quote of pure
// punctuation matches everywhere and grounds nothing.
func hasContentToken(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		return true
	}
	return false
}

// The documented normaliser (§8, "normalized" row). Applied identically to
// the post-image line and to the quote:
//
//   - CR stripped everywhere (CRLF → LF)
//   - zero-width, bidi, BOM and soft-hyphen characters stripped
//   - NBSP (U+00A0) → space
//   - tabs expanded to the file's dominant indent width (approximated by
//     the most common positive leading-space count among file lines;
//     default 4)
//   - trailing whitespace stripped
//   - the file's common leading indent removed from both sides
//
// NFC normalisation is deliberately NOT performed: this build is stdlib-only
// (no golang.org/x/text dependency) and composing equivalence therefore
// remains a known residual mismatch source. It is recorded here rather than
// hidden behind a partial reimplementation of the Unicode tables.
type normalizer struct {
	tabWidth int
	dedent   int
}

func newNormalizer(lines []string) *normalizer {
	n := &normalizer{tabWidth: 4}
	counts := map[int]int{}
	for _, l := range lines {
		c := 0
		for c < len(l) && l[c] == ' ' {
			c++
		}
		if c > 0 {
			counts[c]++
		}
	}
	best, bestN := 0, 0
	for c, k := range counts {
		if k > bestN || (k == bestN && c < best) {
			best, bestN = c, k
		}
	}
	if best > 0 {
		n.tabWidth = best
	}
	minInd := -1
	for _, l := range lines {
		e := n.expand(l)
		body := strings.TrimLeft(e, " ")
		if body == "" {
			continue // blank line: not counted toward common indent
		}
		ind := len(e) - len(body)
		if minInd < 0 || ind < minInd {
			minInd = ind
		}
	}
	if minInd > 0 {
		n.dedent = minInd
	}
	return n
}

// expand replaces tabs with spaces at tabWidth columns.
func (n *normalizer) expand(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			w := n.tabWidth - col%n.tabWidth
			for i := 0; i < w; i++ {
				b.WriteByte(' ')
				col++
			}
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

func (n *normalizer) norm(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 0x200B, 0x200C, 0x200D, 0x200E, 0x200F,
			0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
			0x2060, 0x2061, 0x2062, 0x2063,
			0xFEFF, 0x00AD:
			continue
		case 0x00A0:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s = n.expand(b.String())
	s = strings.TrimRight(s, " ")
	if n.dedent > 0 {
		body := strings.TrimLeft(s, " ")
		ind := len(s) - len(body)
		if body != "" && ind >= n.dedent {
			s = s[n.dedent:] // leading spaces are single-byte; slicing is safe
		}
	}
	return s
}
