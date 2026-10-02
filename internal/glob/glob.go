// Package glob is the single owner of Cite's path-glob dialect.
//
// Two exported Match functions used to answer the same question differently:
// internal/scope.Match (behind paths_ignore) normalised a leading slash and
// path-cleaned both sides, while internal/instructions.Match (behind
// instruction frontmatter applyTo and paths) normalised neither and matched
// segments with a hand-rolled matcher that has no character classes. One
// dialect, documented here once, so that a pattern which works in
// paths_ignore works in applyTo and vice versa.
//
// # The grammar
//
// Patterns are '/'-separated and match a whole repository-relative path.
//
// Normalisation is applied to the pattern and to the name before matching: a
// leading "/", a leading "./", repeated "//", "." and ".." elements are
// resolved and a trailing "/" is dropped. So "/docs/**", "./docs/**",
// "docs//**" and "docs/" name the same thing as "docs/**".
//
//   - "**", as a whole segment, matches zero or more whole segments, so
//     "**/*.gen.go" matches both "c.gen.go" and "a/b/c.gen.go";
//   - "**" may also stand alone as the entire pattern, matching every path;
//   - inside one segment "*" matches any run of characters, "?" matches
//     exactly one character, "[...]" matches one character from a class ("^"
//     negates it, "a-z" spells a range; path.Match has no "!" form), and
//     "\" escapes the next character;
//   - neither "*" nor "?" nor a character class ever crosses a "/", so
//     "docs/*.md" matches "docs/a.md" but not "docs/x/a.md";
//   - matching is case-sensitive;
//   - an empty pattern (and ".", "/", "./", "docs/..") matches nothing. Cite
//     has no spelling of "match every path" other than "**".
//
// There is no brace expansion, no "**" inside a segment, and no "~" handling.
//
//	Match("**/*.gen.go", "c.gen.go")     == true
//	Match("docs/*.md",    "docs/a.md")   == true
//	Match("docs/*.md",    "docs/x/a.md") == false
//	Match("/docs/**",     "docs/a/b.md") == true
//	Match("./docs/*.md",  "docs/a.md")   == true
package glob

import (
	"path"
	"strings"
)

// Match reports whether the repository-relative path name matches pattern,
// under the dialect documented on the package.
func Match(pattern, name string) bool {
	pat := segments(Normalize(pattern))
	if len(pat) == 0 {
		// An absent or degenerate pattern matches nothing. It does NOT match
		// everything: a `applyTo: .` that silently matched the whole repo
		// would be worse than one that matches nothing.
		return false
	}
	return matchSegments(pat, segments(Normalize(name)))
}

// Normalize returns the canonical spelling of pattern: leading "/" and "./"
// removed, "//" collapsed, "." and ".." resolved, trailing "/" dropped. It
// returns "" for a pattern that can never match any path, which is exactly
// the condition Inert reports.
func Normalize(pattern string) string {
	if pattern == "" {
		return ""
	}
	return strings.TrimPrefix(path.Clean("/"+pattern), "/")
}

// Inert reports whether pattern can never match any repository-relative path,
// because its canonical spelling has no segments at all ("" , ".", "/",
// "./", "//", "docs/.."). An applyTo or paths list made only of inert
// patterns applies to nothing; Unmatched is how that becomes visible.
func Inert(pattern string) bool {
	return Normalize(pattern) == ""
}

// Unmatched returns the patterns that match none of paths, in the order they
// were given and without repeats. It is the diagnosis for a silently inert
// instruction file: a glob that never matches anything is indistinguishable
// from a glob that is simply not applicable to the files under review unless
// somebody says so, and the only place that knows the changed paths is the
// resolver. Callers report the result through the existing warning channel.
func Unmatched(patterns, paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range patterns {
		if seen[p] {
			continue
		}
		seen[p] = true
		hit := false
		for _, n := range paths {
			if Match(p, n) {
				hit = true
				break
			}
		}
		if !hit {
			out = append(out, p)
		}
	}
	return out
}

// segments splits a normalised path into its segments; "" has none.
func segments(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
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
