package instructions

import "github.com/elecnix/cite/internal/scope"

// Match reports whether name (a slash-separated repository-relative path)
// matches pattern, in the glob dialect Cite pins (PLAN.md §5: the dialect
// is unspecified upstream, so Cite picks one and writes it down).
//
// The grammar lives in scope.Match, which owns it for paths_ignore as well.
// This package used to carry a second matcher of its own that split the
// pattern on '/' with no normalisation, so an applyTo such as "./docs/**"
// matched no file at all and the instruction file silently never loaded.
// Delegating keeps one dialect behind both configuration surfaces.
func Match(pattern, name string) bool {
	return scope.Match(pattern, name)
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
