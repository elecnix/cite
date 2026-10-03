package scope

import "testing"

// TestMatchDialect pins the glob dialect that internal/scope owns. This is
// the reference table for the whole repository: internal/instructions.Match is
// required to agree with every row here, because CONFORMANCE.md documents one
// dialect for both applyTo globs and paths_ignore. TestMatch in manifest_test.go
// covers the ordinary segment cases; this file adds the normalisation and '**'
// collapse rules that were previously pinned nowhere.
func TestMatchDialect(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		// Ordinary segment matching.
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/x/a.md", false},
		{"docs/*.md", "other/a.md", false},
		{"*.go", "main.go", true},
		{"cmd/?ain.go", "cmd/main.go", true},
		{"cmd/?ain.go", "cmd/rain.go", true},
		{"cmd/?ain.go", "cmd/two.go", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},

		// '**' matches zero or more whole segments.
		{"**/*.gen.go", "a/b/c.gen.go", true},
		{"**/*.gen.go", "c.gen.go", true},
		{"**/*.gen.go", "a/b/c.gen", false},
		{"**", "anything/at/all.go", true},
		{"**", "", true},
		{"src/**", "src/a/b.go", true},
		{"src/**", "srcx/a/b.go", false},
		{"src/**/*.ts", "src/a/b.ts", true},
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "lib/a.ts", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},

		// Consecutive '**' segments collapse.
		{"**/**/*.go", "a/b.go", true},
		{"a/**/**/**/b", "a/b", true},
		{"a/**/**/**/c", "a/b", false},

		// Normalisation: a leading slash, a './' prefix and repeated
		// separators are cosmetic and must not change the result.
		{"/docs/**", "docs/a/b.md", true},
		{"/docs/*.md", "docs/a.md", true},
		{"./docs/*.md", "docs/a.md", true},
		{"./docs/**", "docs/a/b.md", true},
		{"a//b/*.go", "a/b/c.go", true},
		{"docs//*.md", "docs/a.md", true},
		{"/**", "a/b.md", true},
		{"./", "README.md", false},
		{"/", "README.md", false},
		{"//", "README.md", false},
		{".", "README.md", false},
		{"/", "", false},
		{"//", "", false},
		{"./", "", false},
		{".", "", false},
		{"/docs/**", "", false},

		// Character classes are supported inside a single segment.
		{"[abc].md", "a.md", true},
		{"[abc].md", "z.md", false},
		{"docs/[a-c]*.md", "docs/b2.md", true},
		{"docs/[a-c]*.md", "docs/z2.md", false},

		// Brace expansion is explicitly not part of the dialect, so braces
		// are matched literally.
		{"{a,b}.md", "a.md", false},
		{"{a,b}.md", "{a,b}.md", true},

		// An empty pattern matches nothing, and so does every spelling that
		// normalises to the repository root.
		{"", "a.md", false},
		{"", "", false},

		// A pattern that names at least one segment still matches the
		// repository root name, which is what an empty name normalises to.
		{"**", "", true},
		{"**/*.gen.go", "", false},
		{"docs/**", "", false},

		// A malformed class matches nothing rather than panicking.
		{"[.md", "a.md", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestMatchNeverCrossesASegment documents the '**' boundary that the skip
// list depends on: a bare '*' never crosses a '/', only '**' does.
func TestMatchNeverCrossesASegment(t *testing.T) {
	if Match("docs/*", "docs/a/b.md") {
		t.Error(`Match("docs/*", "docs/a/b.md") = true, want false`)
	}
	if !Match("docs/**", "docs/a/b.md") {
		t.Error(`Match("docs/**", "docs/a/b.md") = false, want true`)
	}
}

// TestMatchRootPatternMatchesNothing pins the empty-pattern rule for every
// spelling that normalises to the repository root, not just for the empty
// string. The guard used to test the raw pattern, so it caught "" and then
// normalisation turned "/", "//" and "./" back into the root pattern, which
// matched the root name — Match("/", "") was true while the documentation
// said an empty pattern matches nothing.
func TestMatchRootPatternMatchesNothing(t *testing.T) {
	for _, pattern := range []string{"", "/", "//", "///", "./", ".", ".//", "/./"} {
		for _, name := range []string{"", ".", "/", "README.md", "docs/a.md"} {
			if Match(pattern, name) {
				t.Errorf("Match(%q, %q) = true, want false: a pattern that normalises to the repository root matches nothing", pattern, name)
			}
		}
	}
}

// TestMatchRootPatternDoesNotSwallowTheDialect is the guard against
// over-correcting the root case. Deciding emptiness after normalisation must
// not cost the dialect any pattern that genuinely matches, in particular
// '**', which still matches every name including the root, and 'docs/**',
// which still matches the directory itself rather than only its contents.
func TestMatchRootPatternDoesNotSwallowTheDialect(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**", "", true},
		{"**", ".", true},
		{"**", "/", true},
		{"**", "README.md", true},
		{"**", "a/b/c.go", true},
		{"**/*.gen.go", "c.gen.go", true},
		{"**/*.gen.go", "a/b/c.gen.go", true},
		{"docs/**", "docs", true},
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "docsy/a.md", false},
		{"/**", "a/b.md", true},
		{"/**", "", true},
		{"src/**", "src/a/b.go", true},
		{"a/**/b", "a/b", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
