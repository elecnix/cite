// Package glob is the single owner of Cite's path-glob dialect.
//
// Two exported Match functions used to answer the same question differently:
// internal/scope.Match (behind paths_ignore) normalised a leading slash and
// path-cleaned both sides, while internal/instructions.Match (behind
// instruction frontmatter applyTo and paths) normalised neither and matched
// segments with a hand-rolled matcher that has no character classes. So a
// pattern that worked in paths_ignore matched no file at all in an applyTo, the
// instruction file silently never loaded, and nothing reported the mismatch.
// One dialect, documented here once — the same place CONFORMANCE.md §"Glob
// dialect" and docs/instructions.md §"Glob dialect" point at — so that a
// pattern which works in paths_ignore works in applyTo and vice versa.
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
//     exactly one character, "[...]" matches one character from a class (with
//     "!" or "^" negating it and "a-z" spelling a range), and "\" escapes the
//     next character — exactly the rules of path.Match;
//   - neither "*" nor "?" nor a character class ever crosses a "/", so
//     "docs/*.md" matches "docs/a.md" but not "docs/x/a.md";
//   - "[" and "]" are metacharacters and "{" and "}" are not, so the two
//     literal-name rows are not in tension: a class matches a single
//     character and cannot match a name like "[abc].md", while braces are
//     ordinary characters, so "{a,b}.md" does match a file of that name;
//   - a malformed class matches nothing rather than panicking;
//   - matching is case-sensitive;
//   - an empty pattern (and ".", "/", "./", "docs/..") matches nothing. Cite
//     has no spelling of "match every path" other than "**".
//
// The root rule is about the PATTERN, and it is decided after normalisation,
// so "/" and "//" are rejected alongside "". It says nothing about the NAME,
// which may still be empty: "**" specifies one segment even though it stands
// for zero or more, so Match("**", "") is true while Match("/", "") is false.
// The two rules do not compete, and a reader who carries the first across to
// the second will conclude the matcher is broken when it is not.
//
// Normalisation never confuses a directory with the root. "docs" keeps its
// segment, so Match("docs/**", "docs") is true, while the two names that
// normalise to the root give false: Match("docs/**", "") and
// Match("docs/**", "docs/.."). A leading ".." is resolved the same way and
// cannot escape the repository root, so "../docs" is just "docs" and ".." on
// its own is a root spelling that matches no path.
//
// There is no brace expansion, no "**" inside a segment, and no "~" handling.
//
//	Match("**/*.gen.go", "c.gen.go")     == true
//	Match("docs/*.md",    "docs/a.md")   == true
//	Match("docs/*.md",    "docs/x/a.md") == false
//	Match("/docs/**",     "docs/a/b.md") == true
//	Match("./docs/*.md",  "docs/a.md")   == true
//	Match("**",           "")            == true
//	Match("/",            "")            == false
//	Match("docs/**",      "docs")        == true
//	Match("docs/**",      "")            == false
//
// Both surfaces call Match directly. Neither internal/scope nor
// internal/instructions keeps a wrapper of its own, because a second exported
// entry point into a dialect is how the two came to disagree in the first
// place.
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
		// would be worse than one that matches nothing. Deciding this after
		// normalisation is deliberate — a raw-pattern guard catches only the
		// empty string and lets "/", "//" and "./" normalise straight back
		// into a pattern that matches the root name.
		return false
	}
	return matchSegments(pat, segments(Normalize(name)))
}

// Normalize returns the canonical spelling of pattern: leading "/" and "./"
// removed, "//" collapsed, "." and ".." resolved, trailing "/" dropped. A
// leading ".." cannot escape the repository root, so "../docs" becomes "docs".
// It returns "" exactly when the pattern specifies no segment at all (".", "..",
// "/", "./", "docs/.."), which is the same condition Match treats as matching
// no path, so a "" from Normalize and a false from Match always agree.
//
// Callers that store a pattern the user wrote — instruction frontmatter
// applyTo and paths — normalise at parse time, so the pattern `cite doctor`
// reports is the one paths_ignore would also have understood.
func Normalize(pattern string) string {
	if pattern == "" {
		return ""
	}
	return strings.TrimPrefix(path.Clean("/"+pattern), "/")
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
