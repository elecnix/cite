package reviewer

import (
	"regexp"
	"sort"
	"strings"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// anchorsInHunks reports whether every line of a is a line of the diff.
func (fc *fileContext) anchorsInHunks(a model.Anchor) bool {
	for line := a.StartLine; line <= a.EndLine; line++ {
		if !fc.anchorable[line] {
			return false
		}
	}
	return true
}

var identPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)

// reanchor computes where a finding posts when its anchor is outside the
// diff's hunks (issue #220). It tries, in order: the removed line that
// defines an identifier the anchor line uses, the nearest changed line in
// the same file, and, for a file with no hunks, a changed line in another
// file that the finding text names. It returns nil when none applies.
func (fc *fileContext) reanchor(f *model.Finding) *model.Reanchor {
	use := model.UseSite{Line: f.Anchor.StartLine}
	if i := f.Anchor.StartLine - 1; i >= 0 && i < len(fc.lines) {
		use.Text = strings.TrimSpace(fc.lines[i])
	}
	text := f.Title + " " + f.Body
	if fc.diff != nil && len(fc.diff.Hunks) > 0 {
		var span strings.Builder
		for l := f.Anchor.StartLine; l <= f.Anchor.EndLine && l <= len(fc.lines); l++ {
			span.WriteString(fc.lines[l-1])
			span.WriteByte('\n')
		}
		if line, ok := definingRemovedLine(fc.diff, identifiers(span.String(), text)); ok {
			return &model.Reanchor{Path: fc.path, Side: "LEFT", Line: line, Kind: model.ReanchorDeletedDefinition, UseSite: use}
		}
		if side, line, ok := nearestChange(fc.diff, f.Anchor.StartLine); ok {
			return &model.Reanchor{Path: fc.path, Side: side, Line: line, Kind: model.ReanchorNearestChange, UseSite: use}
		}
		return nil
	}
	// A file with no hunks has no line to anchor on: follow the finding's
	// own text to a changed line elsewhere in the diff.
	paths := make([]string, 0, len(fc.otherDiffs))
	for p := range fc.otherDiffs {
		if p != fc.path {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	idents := identifiers(text)
	for _, p := range paths {
		if line, ok := definingRemovedLine(fc.otherDiffs[p], idents); ok {
			return &model.Reanchor{Path: p, Side: "LEFT", Line: line, Kind: model.ReanchorOtherFile, UseSite: use}
		}
	}
	for _, p := range paths {
		for _, h := range fc.otherDiffs[p].Hunks {
			for _, l := range h.Lines {
				if l.Kind == scope.LineAdded && containsAny(l.Content, idents) {
					return &model.Reanchor{Path: p, Side: "RIGHT", Line: l.NewNo, Kind: model.ReanchorOtherFile, UseSite: use}
				}
			}
		}
	}
	return nil
}

// identifiers lists the distinct identifier-shaped words of the texts, in
// order of first appearance.
func identifiers(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, w := range identPattern.FindAllString(t, -1) {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

func containsAny(line string, idents []string) bool {
	for _, id := range idents {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`).MatchString(line) {
			return true
		}
	}
	return false
}

// definingRemovedLine returns the old line number of the first removed line
// that defines one of idents, trying them in order.
func definingRemovedLine(df *scope.DiffFile, idents []string) (int, bool) {
	removed := df.RemovedLines()
	for _, id := range idents {
		re := definitionPattern(id)
		for _, rl := range removed {
			if re.MatchString(rl.Content) && !startsWithStatement(rl.Content) {
				return rl.OldNo, true
			}
		}
	}
	return 0, false
}

// definitionPattern matches a line that declares id: declaration keywords
// and a receiver may precede it, and a call list, assignment, type or block
// opens after it. A line that merely uses id in an expression does not match.
func definitionPattern(id string) *regexp.Regexp {
	q := regexp.QuoteMeta(id)
	return regexp.MustCompile(`^\s*(?:[A-Za-z_]+\s+)*(?:\([^)]*\)\s*)?` + q + `\s*(?:\(|:?=[^=]|:|\{|\s+[A-Za-z\[*])`)
}

var statementWords = map[string]bool{
	"return": true, "if": true, "else": true, "for": true, "while": true, "switch": true,
	"case": true, "go": true, "defer": true, "throw": true, "await": true, "yield": true,
}

// startsWithStatement reports whether the line opens with a statement
// keyword, which makes an identifier after it a use and not a declaration.
func startsWithStatement(line string) bool {
	f := strings.Fields(line)
	return len(f) > 0 && statementWords[f[0]]
}

// nearestChange returns the changed line closest to line in the post-image:
// an added line on the RIGHT side, or, when the file has none, the removed
// line of the hunk nearest line on the LEFT side.
func nearestChange(df *scope.DiffFile, line int) (side string, no int, ok bool) {
	best := -1
	for _, n := range df.AddedLines() {
		if d := abs(n - line); best < 0 || d < best {
			best, no, side = d, n, "RIGHT"
		}
	}
	if best >= 0 {
		return side, no, true
	}
	for _, h := range df.Hunks {
		for _, l := range h.Lines {
			if l.Kind != scope.LineRemoved {
				continue
			}
			if d := abs(h.NewStart - line); best < 0 || d < best {
				best, no, side = d, l.OldNo, "LEFT"
			}
		}
	}
	return side, no, best >= 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
