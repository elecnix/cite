package instructions

import "strings"

// Match reports whether name (a slash-separated repository-relative path)
// matches pattern, in the glob dialect Cite pins (PLAN.md §5: the dialect
// is unspecified upstream, so Cite picks one and writes it down):
//
//   - patterns are `/`-separated and match against the whole path;
//   - `*` matches any run of characters within a single segment;
//   - `?` matches exactly one character within a segment;
//   - a `**` segment matches zero or more whole segments.
func Match(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for len(p) > 0 && p[0] == "**" {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(n); i++ {
				if matchSegs(p, n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 || !matchToken(p[0], n[0]) {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

// matchToken matches one path segment with the classic * / ? backtracking.
func matchToken(pat, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		if px < len(pat) && (pat[px] == '?' || pat[px] == s[sx]) {
			px++
			sx++
		} else if px < len(pat) && pat[px] == '*' {
			star = px
			mark = sx
			px++
		} else if star != -1 {
			mark++
			sx = mark
			px = star + 1
		} else {
			return false
		}
	}
	for px < len(pat) && pat[px] == '*' {
		px++
	}
	return px == len(pat)
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
