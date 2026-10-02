package scope

import "github.com/elecnix/cite/internal/glob"

// Match reports whether name matches pattern. Patterns are '/'-separated
// globs used by paths_ignore, in the dialect owned — and documented — by
// internal/glob, which instruction frontmatter (applyTo, paths) uses too, so
// one pattern cannot mean one thing in cite.yml and another in an
// instructions file:
//
//   - '**' matches any number of whole path segments, including none;
//   - within a single segment, '*', '?', '[...]' and '\' behave as in
//     path.Match — '*' never crosses a '/'.
//
// This function is the paths_ignore entry point into that one matcher; it
// stays here because internal/scope is the package that answers "is this
// file in scope?".
//
//	Match("**/*.gen.go", "a/b/c.gen.go") == true
//	Match("**/*.gen.go", "c.gen.go")     == true
//	Match("docs/*.md",    "docs/a.md")   == true
//	Match("docs/*.md",    "docs/x/a.md") == false
//	Match("/docs/**",     "docs/a/b.md") == true
func Match(pattern, name string) bool {
	return glob.Match(pattern, name)
}
