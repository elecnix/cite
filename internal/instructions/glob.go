package instructions

import (
	"github.com/elecnix/cite/internal/glob"
)

// Match reports whether name (a slash-separated repository-relative path)
// matches pattern, in the glob dialect Cite pins (PLAN.md §5: the dialect is
// unspecified upstream, so Cite picks one and writes it down). The dialect is
// owned and documented by internal/glob, the same matcher paths_ignore uses,
// so a pattern cannot work in cite.yml and quietly match nothing here:
//
//   - patterns are '/'-separated and match against the whole path, after
//     normalising a leading "/", a leading "./", repeated "//", "." and "..",
//     and a trailing "/" — "/docs/**", "./docs/**" and "docs//**" are one
//     pattern;
//   - `**` as a whole segment matches zero or more whole segments;
//   - within a segment, `*` matches any run of characters, `?` matches one
//     character, `[...]` a character class, and `\` escapes — as in
//     path.Match, so none of them cross a "/".
//
// A frontmatter pattern is canonicalised at parse time (frontmatter.go), so
// the pattern `cite doctor` reports is the one paths_ignore would have
// understood too, and glob.Unmatched names the ones that matched none of the
// changed files: a match-nothing applyTo is disclosed, not invisible.
func Match(pattern, name string) bool {
	return glob.Match(pattern, name)
}

// wildcards counts `*` and `?` occurrences. Specificity ordering (§5,
// documented best-effort): between two *.instructions.md whose applyTo both
// match a changed file, the most specific glob comes first — fewest
// wildcards, then longest pattern — then lexical path.
func wildcards(pattern string) int {
	n := 0
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' || pattern[i] == '?' {
			n++
		}
	}
	return n
}
