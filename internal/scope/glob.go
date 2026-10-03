package scope

import "path"

// Match reports whether the repository-relative path name matches pattern, in
// the glob dialect Cite pins (CONFORMANCE.md, "Glob dialect": the dialect is
// unspecified upstream, so Cite picks one and writes it down).
//
// This function is the single owner of that grammar. It is the reference
// implementation for paths_ignore and for the applyTo globs and paths
// frontmatter of instruction files, which reach it through
// instructions.Match rather than through a second matcher of their own.
//
//   - patterns are '/'-separated and match against the whole path;
//   - '**' matches any number of whole path segments, including none, and
//     consecutive '**' segments collapse;
//   - within a single segment, '*', '?', and '[...]' behave as in
//     path.Match — '*' never crosses a '/' and a malformed class matches
//     nothing;
//   - brace expansion is not part of the dialect, so braces match literally;
//   - the pattern is normalised before matching, so a leading slash, a './'
//     prefix and repeated separators are cosmetic;
//   - a pattern that normalises to the repository root matches nothing, which
//     covers the empty pattern and every cosmetic spelling of the root: the
//     empty string, '/', '//' and './'.
//
// Examples:
//
//	Match("**/*.gen.go", "a/b/c.gen.go") == true
//	Match("**/*.gen.go", "c.gen.go")     == true
//	Match("docs/*.md",    "docs/a.md")   == true
//	Match("docs/*.md",    "docs/x/a.md") == false
//	Match("/docs/**",     "docs/a/b.md") == true
//	Match("/",           "")             == false
func Match(pattern, name string) bool {
	pat := splitSegments(pattern)
	if len(pat) == 0 {
		// The pattern named nothing to match: it was empty, or it was one
		// of the spellings that normalises to the repository root. Deciding
		// this before normalisation is the mistake the root spellings expose,
		// because a raw-pattern guard catches only the empty string and lets
		// '/', '//' and './' normalise straight back into a pattern that
		// matches the root name.
		return false
	}
	return matchSegments(pat, splitSegments(name))
}

func splitSegments(p string) []string {
	p = path.Clean(strings2Normalize(p))
	if p == "." {
		return nil
	}
	return splitPath(p)
}

// strings2Normalize strips leading slashes so "/docs/**" and "docs/**"
// agree, and maps an all-slash path to the repository root.
func strings2Normalize(p string) string {
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "" {
		return "."
	}
	return p
}

func matchSegments(pat, nam []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Collapse consecutive '**'.
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true // trailing '**' swallows the rest
			}
			for i := 0; i <= len(nam); i++ {
				if matchSegments(pat[1:], nam[i:]) {
					return true
				}
			}
			return false
		}
		if len(nam) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], nam[0])
		if err != nil || !ok {
			return false
		}
		pat, nam = pat[1:], nam[1:]
	}
	return len(nam) == 0
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	out = append(out, p[start:])
	return out
}
