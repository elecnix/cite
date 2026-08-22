package scope

import "path"

// Match reports whether name matches pattern. Patterns are '/'-separated
// globs used by paths_ignore:
//
//   - '**' matches any number of whole path segments, including none;
//   - within a single segment, '*', '?', and '[...]' behave as in
//     path.Match — '*' never crosses a '/'.
//
// Examples:
//
//	Match("**/*.gen.go", "a/b/c.gen.go") == true
//	Match("**/*.gen.go", "c.gen.go")     == true
//	Match("docs/*.md",    "docs/a.md")   == true
//	Match("docs/*.md",    "docs/x/a.md") == false
func Match(pattern, name string) bool {
	return matchSegments(splitSegments(pattern), splitSegments(name))
}

func splitSegments(p string) []string {
	p = path.Clean(strings2Normalize(p))
	if p == "." {
		return nil
	}
	return splitPath(p)
}

// strings2Normalize strips a leading slash so "/docs/**" and "docs/**" agree.
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
