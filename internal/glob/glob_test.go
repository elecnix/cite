package glob

import "testing"

// This file is the dialect's only test. The two packages that use the dialect
// — internal/scope (paths_ignore) and internal/instructions (applyTo, paths) —
// call glob.Match directly and hold no matcher of their own, so every rule is
// pinned here once rather than twice with a cross-check that could only ever
// compare a function with itself.

// TestMatchDialect is the reference table for the whole repository: both
// surfaces reach this matcher, so every row is an answer paths_ignore and
// applyTo must both give. internal/scope/manifest_test.go covers the ordinary
// segment cases; the normalisation and '**' collapse rules that were
// previously pinned nowhere are here.
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

		// A root pattern names no segment, so there is nothing for it to
		// match. '**' is not a root pattern: it names zero or more
		// segments, so it still matches the zero-segment name.
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

// TestMatchAppliesToFrontmatterGlobs is the same corpus as it was answered
// through instruction frontmatter before the extraction, kept verbatim so the
// applyTo surface cannot drift from the paths_ignore one. It used to live in
// internal/instructions and compare its own answer against
// internal/scope.Match; with one matcher there is nothing left to compare
// against, so the rows are pinned as expectations instead.
func TestMatchAppliesToFrontmatterGlobs(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		// Cases that already agreed.
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/x/y.go", true},
		{"**/*.go", "main.got", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/x/a.md", false},
		{"**", "anything/at/all.go", true},
		{"src/**", "src/a/b.go", true},

		// Normalisation. A leading slash, a './' prefix and repeated
		// separators are cosmetic and must not change the result.
		{"/docs/**", "docs/a/b.md", true},
		{"/docs/*.md", "docs/a.md", true},
		{"./docs/*.md", "docs/a.md", true},
		{"./docs/**", "docs/a/b.md", true},
		{"a//b/*.go", "a/b/c.go", true},
		{"/**", "a/b.md", true},

		// Character classes inside a single segment.
		{"[abc].md", "a.md", true},
		{"[abc].md", "z.md", false},
		{"docs/[a-c]*.md", "docs/b2.md", true},
		{"docs/[a-c]*.md", "docs/z2.md", false},

		// Brace expansion is not part of the dialect.
		{"{a,b}.md", "a.md", false},

		// An empty pattern matches nothing, and so does every spelling
		// that normalises to the repository root: a pattern naming no
		// segment can never name a file.
		{"", "a.md", false},
		{"", "", false},
		{"/", "", false},
		{"//", "", false},
		{"./", "", false},
		{".", "", false},
		{"/", "README.md", false},
		{"//", "README.md", false},
		{"./", "README.md", false},

		// '**' names zero or more segments rather than naming none,
		// so it is not one of the spellings above and still matches
		// the empty name.
		{"**", "", true},
		{"docs/**", "docs", true},
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
//
// "matches nothing" here means the pattern names no segment at all, so it can
// never name a file. It is not a statement about '**', which also has no
// literal segment but explicitly stands for zero or more of them, and which
// still matches every name. The two spellings are different rules, not
// competing ones: see TestMatchRootPatternDoesNotSwallowTheDialect.
//
// Both callers pass a real repository-relative path, never an empty name, so
// this class is pinned as dialect rather than as production behaviour.
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
// '**', which stands for zero or more segments and so matches the empty name
// that a root pattern is rejected for, and 'docs/**', which still matches the
// directory itself rather than only its contents. Read together with
// TestMatchRootPatternMatchesNothing the pair states the whole rule: a pattern
// that names no segment matches nothing, and a pattern that names segments —
// however optionally — still matches.
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

// TestMatchOrdinarySegmentCases moved here verbatim from
// internal/scope/manifest_test.go, which used to call the scope matcher
// directly. The dialect now has one owner, so its ordinary cases are pinned
// with the rest of them; what internal/scope keeps is
// TestPathsIgnoreUsesTheSharedDialect, which proves the paths_ignore entry
// point still reaches this matcher.
func TestMatchOrdinarySegmentCases(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.gen.go", "a/b/c.gen.go", true},
		{"**/*.gen.go", "c.gen.go", true},
		{"**/*.gen.go", "a/b/c.go", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/x/a.md", false},
		{"docs/**/*.md", "docs/x/a.md", true},
		{"docs/**/*.md", "docs/a.md", true},
		{"**", "anything/at/all.go", true},
		{"internal/**", "internal/a/b.go", true},
		{"internal/**", "internalX/a.go", false},
		{"*.go", "a.go", true},
		{"*.go", "dir/a.go", false}, // '*' never crosses '/'
		{"a?.go", "ab.go", true},
		{"a?.go", "a.go", false},
		{"[abc].go", "b.go", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestMatchAppliesToInstructionGlobs moved here verbatim from
// internal/instructions/instructions_test.go, whose TestGlobDialect called
// the instructions matcher directly. The dialect has one owner now, so the
// rows are pinned with the rest of them; internal/instructions keeps
// TestApplyToUsesTheSharedDialect, which proves applyTo reaches this matcher.
func TestMatchAppliesToInstructionGlobs(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/x/y.go", true},
		{"**/*.go", "main.got", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"src/**/*.ts", "src/a/b.ts", true},
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "lib/a.ts", false},
		{"cmd/?ain.go", "cmd/main.go", true},
		{"cmd/?ain.go", "cmd/rain.go", true},
		{"cmd/?ain.go", "cmd/two.go", false},
		{"**", "anything/at/all.go", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestMatchWholeCorpus is the exhaustive cross-product that used to be
// TestMatchAgreesWithScope. That test cross-checked the two Match functions
// against each other and asserted nothing on its own; with one matcher the
// cross-check is a tautology, so the same 34 patterns and 19 names are now
// pinned as 646 expectations.
//
// Every expectation in this table was recorded from the matcher as it stood
// on arch/c6-glob-dialect BEFORE this package existed — that is, from the
// internal/scope implementation the extraction moved — and none of them was
// edited to fit the new one. The extraction is therefore a move, not a
// redefinition: a row that changed here would mean the two surfaces had
// disagreed all along, which is the defect this package exists to end.
func TestMatchWholeCorpus(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "", false},
		{"", ".", false},
		{"", "/", false},
		{"", "docs", false},
		{"", "a.go", false},
		{"", "main.go", false},
		{"", "README.md", false},
		{"", "docs/a.md", false},
		{"", "docs/a/b.md", false},
		{"", "src/a/b.go", false},
		{"", "src/a.ts", false},
		{"", "cmd/main.go", false},
		{"", "a/b", false},
		{"", "a/x/y/b", false},
		{"", "[abc].md", false},
		{"", "{a,b}.md", false},
		{"", "z.md", false},
		{"", "docs/..", false},
		{"", "//", false},
		{"*", "", false},
		{"*", ".", false},
		{"*", "/", false},
		{"*", "docs", true},
		{"*", "a.go", true},
		{"*", "main.go", true},
		{"*", "README.md", true},
		{"*", "docs/a.md", false},
		{"*", "docs/a/b.md", false},
		{"*", "src/a/b.go", false},
		{"*", "src/a.ts", false},
		{"*", "cmd/main.go", false},
		{"*", "a/b", false},
		{"*", "a/x/y/b", false},
		{"*", "[abc].md", true},
		{"*", "{a,b}.md", true},
		{"*", "z.md", true},
		{"*", "docs/..", false},
		{"*", "//", false},
		{"*.go", "", false},
		{"*.go", ".", false},
		{"*.go", "/", false},
		{"*.go", "docs", false},
		{"*.go", "a.go", true},
		{"*.go", "main.go", true},
		{"*.go", "README.md", false},
		{"*.go", "docs/a.md", false},
		{"*.go", "docs/a/b.md", false},
		{"*.go", "src/a/b.go", false},
		{"*.go", "src/a.ts", false},
		{"*.go", "cmd/main.go", false},
		{"*.go", "a/b", false},
		{"*.go", "a/x/y/b", false},
		{"*.go", "[abc].md", false},
		{"*.go", "{a,b}.md", false},
		{"*.go", "z.md", false},
		{"*.go", "docs/..", false},
		{"*.go", "//", false},
		{"**", "", true},
		{"**", ".", true},
		{"**", "/", true},
		{"**", "docs", true},
		{"**", "a.go", true},
		{"**", "main.go", true},
		{"**", "README.md", true},
		{"**", "docs/a.md", true},
		{"**", "docs/a/b.md", true},
		{"**", "src/a/b.go", true},
		{"**", "src/a.ts", true},
		{"**", "cmd/main.go", true},
		{"**", "a/b", true},
		{"**", "a/x/y/b", true},
		{"**", "[abc].md", true},
		{"**", "{a,b}.md", true},
		{"**", "z.md", true},
		{"**", "docs/..", true},
		{"**", "//", true},
		{"**/*", "", false},
		{"**/*", ".", false},
		{"**/*", "/", false},
		{"**/*", "docs", true},
		{"**/*", "a.go", true},
		{"**/*", "main.go", true},
		{"**/*", "README.md", true},
		{"**/*", "docs/a.md", true},
		{"**/*", "docs/a/b.md", true},
		{"**/*", "src/a/b.go", true},
		{"**/*", "src/a.ts", true},
		{"**/*", "cmd/main.go", true},
		{"**/*", "a/b", true},
		{"**/*", "a/x/y/b", true},
		{"**/*", "[abc].md", true},
		{"**/*", "{a,b}.md", true},
		{"**/*", "z.md", true},
		{"**/*", "docs/..", false},
		{"**/*", "//", false},
		{"**/*.go", "", false},
		{"**/*.go", ".", false},
		{"**/*.go", "/", false},
		{"**/*.go", "docs", false},
		{"**/*.go", "a.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "README.md", false},
		{"**/*.go", "docs/a.md", false},
		{"**/*.go", "docs/a/b.md", false},
		{"**/*.go", "src/a/b.go", true},
		{"**/*.go", "src/a.ts", false},
		{"**/*.go", "cmd/main.go", true},
		{"**/*.go", "a/b", false},
		{"**/*.go", "a/x/y/b", false},
		{"**/*.go", "[abc].md", false},
		{"**/*.go", "{a,b}.md", false},
		{"**/*.go", "z.md", false},
		{"**/*.go", "docs/..", false},
		{"**/*.go", "//", false},
		{"docs/*.md", "", false},
		{"docs/*.md", ".", false},
		{"docs/*.md", "/", false},
		{"docs/*.md", "docs", false},
		{"docs/*.md", "a.go", false},
		{"docs/*.md", "main.go", false},
		{"docs/*.md", "README.md", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/a/b.md", false},
		{"docs/*.md", "src/a/b.go", false},
		{"docs/*.md", "src/a.ts", false},
		{"docs/*.md", "cmd/main.go", false},
		{"docs/*.md", "a/b", false},
		{"docs/*.md", "a/x/y/b", false},
		{"docs/*.md", "[abc].md", false},
		{"docs/*.md", "{a,b}.md", false},
		{"docs/*.md", "z.md", false},
		{"docs/*.md", "docs/..", false},
		{"docs/*.md", "//", false},
		{"docs/**", "", false},
		{"docs/**", ".", false},
		{"docs/**", "/", false},
		{"docs/**", "docs", true},
		{"docs/**", "a.go", false},
		{"docs/**", "main.go", false},
		{"docs/**", "README.md", false},
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "src/a/b.go", false},
		{"docs/**", "src/a.ts", false},
		{"docs/**", "cmd/main.go", false},
		{"docs/**", "a/b", false},
		{"docs/**", "a/x/y/b", false},
		{"docs/**", "[abc].md", false},
		{"docs/**", "{a,b}.md", false},
		{"docs/**", "z.md", false},
		{"docs/**", "docs/..", false},
		{"docs/**", "//", false},
		{"src/**", "", false},
		{"src/**", ".", false},
		{"src/**", "/", false},
		{"src/**", "docs", false},
		{"src/**", "a.go", false},
		{"src/**", "main.go", false},
		{"src/**", "README.md", false},
		{"src/**", "docs/a.md", false},
		{"src/**", "docs/a/b.md", false},
		{"src/**", "src/a/b.go", true},
		{"src/**", "src/a.ts", true},
		{"src/**", "cmd/main.go", false},
		{"src/**", "a/b", false},
		{"src/**", "a/x/y/b", false},
		{"src/**", "[abc].md", false},
		{"src/**", "{a,b}.md", false},
		{"src/**", "z.md", false},
		{"src/**", "docs/..", false},
		{"src/**", "//", false},
		{"src/**/*.ts", "", false},
		{"src/**/*.ts", ".", false},
		{"src/**/*.ts", "/", false},
		{"src/**/*.ts", "docs", false},
		{"src/**/*.ts", "a.go", false},
		{"src/**/*.ts", "main.go", false},
		{"src/**/*.ts", "README.md", false},
		{"src/**/*.ts", "docs/a.md", false},
		{"src/**/*.ts", "docs/a/b.md", false},
		{"src/**/*.ts", "src/a/b.go", false},
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "cmd/main.go", false},
		{"src/**/*.ts", "a/b", false},
		{"src/**/*.ts", "a/x/y/b", false},
		{"src/**/*.ts", "[abc].md", false},
		{"src/**/*.ts", "{a,b}.md", false},
		{"src/**/*.ts", "z.md", false},
		{"src/**/*.ts", "docs/..", false},
		{"src/**/*.ts", "//", false},
		{"cmd/?ain.go", "", false},
		{"cmd/?ain.go", ".", false},
		{"cmd/?ain.go", "/", false},
		{"cmd/?ain.go", "docs", false},
		{"cmd/?ain.go", "a.go", false},
		{"cmd/?ain.go", "main.go", false},
		{"cmd/?ain.go", "README.md", false},
		{"cmd/?ain.go", "docs/a.md", false},
		{"cmd/?ain.go", "docs/a/b.md", false},
		{"cmd/?ain.go", "src/a/b.go", false},
		{"cmd/?ain.go", "src/a.ts", false},
		{"cmd/?ain.go", "cmd/main.go", true},
		{"cmd/?ain.go", "a/b", false},
		{"cmd/?ain.go", "a/x/y/b", false},
		{"cmd/?ain.go", "[abc].md", false},
		{"cmd/?ain.go", "{a,b}.md", false},
		{"cmd/?ain.go", "z.md", false},
		{"cmd/?ain.go", "docs/..", false},
		{"cmd/?ain.go", "//", false},
		{"a/**/b", "", false},
		{"a/**/b", ".", false},
		{"a/**/b", "/", false},
		{"a/**/b", "docs", false},
		{"a/**/b", "a.go", false},
		{"a/**/b", "main.go", false},
		{"a/**/b", "README.md", false},
		{"a/**/b", "docs/a.md", false},
		{"a/**/b", "docs/a/b.md", false},
		{"a/**/b", "src/a/b.go", false},
		{"a/**/b", "src/a.ts", false},
		{"a/**/b", "cmd/main.go", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "[abc].md", false},
		{"a/**/b", "{a,b}.md", false},
		{"a/**/b", "z.md", false},
		{"a/**/b", "docs/..", false},
		{"a/**/b", "//", false},
		{"**/**/*.go", "", false},
		{"**/**/*.go", ".", false},
		{"**/**/*.go", "/", false},
		{"**/**/*.go", "docs", false},
		{"**/**/*.go", "a.go", true},
		{"**/**/*.go", "main.go", true},
		{"**/**/*.go", "README.md", false},
		{"**/**/*.go", "docs/a.md", false},
		{"**/**/*.go", "docs/a/b.md", false},
		{"**/**/*.go", "src/a/b.go", true},
		{"**/**/*.go", "src/a.ts", false},
		{"**/**/*.go", "cmd/main.go", true},
		{"**/**/*.go", "a/b", false},
		{"**/**/*.go", "a/x/y/b", false},
		{"**/**/*.go", "[abc].md", false},
		{"**/**/*.go", "{a,b}.md", false},
		{"**/**/*.go", "z.md", false},
		{"**/**/*.go", "docs/..", false},
		{"**/**/*.go", "//", false},
		{"/docs/**", "", false},
		{"/docs/**", ".", false},
		{"/docs/**", "/", false},
		{"/docs/**", "docs", true},
		{"/docs/**", "a.go", false},
		{"/docs/**", "main.go", false},
		{"/docs/**", "README.md", false},
		{"/docs/**", "docs/a.md", true},
		{"/docs/**", "docs/a/b.md", true},
		{"/docs/**", "src/a/b.go", false},
		{"/docs/**", "src/a.ts", false},
		{"/docs/**", "cmd/main.go", false},
		{"/docs/**", "a/b", false},
		{"/docs/**", "a/x/y/b", false},
		{"/docs/**", "[abc].md", false},
		{"/docs/**", "{a,b}.md", false},
		{"/docs/**", "z.md", false},
		{"/docs/**", "docs/..", false},
		{"/docs/**", "//", false},
		{"./docs/*.md", "", false},
		{"./docs/*.md", ".", false},
		{"./docs/*.md", "/", false},
		{"./docs/*.md", "docs", false},
		{"./docs/*.md", "a.go", false},
		{"./docs/*.md", "main.go", false},
		{"./docs/*.md", "README.md", false},
		{"./docs/*.md", "docs/a.md", true},
		{"./docs/*.md", "docs/a/b.md", false},
		{"./docs/*.md", "src/a/b.go", false},
		{"./docs/*.md", "src/a.ts", false},
		{"./docs/*.md", "cmd/main.go", false},
		{"./docs/*.md", "a/b", false},
		{"./docs/*.md", "a/x/y/b", false},
		{"./docs/*.md", "[abc].md", false},
		{"./docs/*.md", "{a,b}.md", false},
		{"./docs/*.md", "z.md", false},
		{"./docs/*.md", "docs/..", false},
		{"./docs/*.md", "//", false},
		{"a//b/*.go", "", false},
		{"a//b/*.go", ".", false},
		{"a//b/*.go", "/", false},
		{"a//b/*.go", "docs", false},
		{"a//b/*.go", "a.go", false},
		{"a//b/*.go", "main.go", false},
		{"a//b/*.go", "README.md", false},
		{"a//b/*.go", "docs/a.md", false},
		{"a//b/*.go", "docs/a/b.md", false},
		{"a//b/*.go", "src/a/b.go", false},
		{"a//b/*.go", "src/a.ts", false},
		{"a//b/*.go", "cmd/main.go", false},
		{"a//b/*.go", "a/b", false},
		{"a//b/*.go", "a/x/y/b", false},
		{"a//b/*.go", "[abc].md", false},
		{"a//b/*.go", "{a,b}.md", false},
		{"a//b/*.go", "z.md", false},
		{"a//b/*.go", "docs/..", false},
		{"a//b/*.go", "//", false},
		{"/**", "", true},
		{"/**", ".", true},
		{"/**", "/", true},
		{"/**", "docs", true},
		{"/**", "a.go", true},
		{"/**", "main.go", true},
		{"/**", "README.md", true},
		{"/**", "docs/a.md", true},
		{"/**", "docs/a/b.md", true},
		{"/**", "src/a/b.go", true},
		{"/**", "src/a.ts", true},
		{"/**", "cmd/main.go", true},
		{"/**", "a/b", true},
		{"/**", "a/x/y/b", true},
		{"/**", "[abc].md", true},
		{"/**", "{a,b}.md", true},
		{"/**", "z.md", true},
		{"/**", "docs/..", true},
		{"/**", "//", true},
		{"[abc].md", "", false},
		{"[abc].md", ".", false},
		{"[abc].md", "/", false},
		{"[abc].md", "docs", false},
		{"[abc].md", "a.go", false},
		{"[abc].md", "main.go", false},
		{"[abc].md", "README.md", false},
		{"[abc].md", "docs/a.md", false},
		{"[abc].md", "docs/a/b.md", false},
		{"[abc].md", "src/a/b.go", false},
		{"[abc].md", "src/a.ts", false},
		{"[abc].md", "cmd/main.go", false},
		{"[abc].md", "a/b", false},
		{"[abc].md", "a/x/y/b", false},
		{"[abc].md", "[abc].md", false},
		{"[abc].md", "{a,b}.md", false},
		{"[abc].md", "z.md", false},
		{"[abc].md", "docs/..", false},
		{"[abc].md", "//", false},
		{"docs/[a-c]*.md", "", false},
		{"docs/[a-c]*.md", ".", false},
		{"docs/[a-c]*.md", "/", false},
		{"docs/[a-c]*.md", "docs", false},
		{"docs/[a-c]*.md", "a.go", false},
		{"docs/[a-c]*.md", "main.go", false},
		{"docs/[a-c]*.md", "README.md", false},
		{"docs/[a-c]*.md", "docs/a.md", true},
		{"docs/[a-c]*.md", "docs/a/b.md", false},
		{"docs/[a-c]*.md", "src/a/b.go", false},
		{"docs/[a-c]*.md", "src/a.ts", false},
		{"docs/[a-c]*.md", "cmd/main.go", false},
		{"docs/[a-c]*.md", "a/b", false},
		{"docs/[a-c]*.md", "a/x/y/b", false},
		{"docs/[a-c]*.md", "[abc].md", false},
		{"docs/[a-c]*.md", "{a,b}.md", false},
		{"docs/[a-c]*.md", "z.md", false},
		{"docs/[a-c]*.md", "docs/..", false},
		{"docs/[a-c]*.md", "//", false},
		{"{a,b}.md", "", false},
		{"{a,b}.md", ".", false},
		{"{a,b}.md", "/", false},
		{"{a,b}.md", "docs", false},
		{"{a,b}.md", "a.go", false},
		{"{a,b}.md", "main.go", false},
		{"{a,b}.md", "README.md", false},
		{"{a,b}.md", "docs/a.md", false},
		{"{a,b}.md", "docs/a/b.md", false},
		{"{a,b}.md", "src/a/b.go", false},
		{"{a,b}.md", "src/a.ts", false},
		{"{a,b}.md", "cmd/main.go", false},
		{"{a,b}.md", "a/b", false},
		{"{a,b}.md", "a/x/y/b", false},
		{"{a,b}.md", "[abc].md", false},
		{"{a,b}.md", "{a,b}.md", true},
		{"{a,b}.md", "z.md", false},
		{"{a,b}.md", "docs/..", false},
		{"{a,b}.md", "//", false},
		{"[.md", "", false},
		{"[.md", ".", false},
		{"[.md", "/", false},
		{"[.md", "docs", false},
		{"[.md", "a.go", false},
		{"[.md", "main.go", false},
		{"[.md", "README.md", false},
		{"[.md", "docs/a.md", false},
		{"[.md", "docs/a/b.md", false},
		{"[.md", "src/a/b.go", false},
		{"[.md", "src/a.ts", false},
		{"[.md", "cmd/main.go", false},
		{"[.md", "a/b", false},
		{"[.md", "a/x/y/b", false},
		{"[.md", "[abc].md", false},
		{"[.md", "{a,b}.md", false},
		{"[.md", "z.md", false},
		{"[.md", "docs/..", false},
		{"[.md", "//", false},
		{"./", "", false},
		{"./", ".", false},
		{"./", "/", false},
		{"./", "docs", false},
		{"./", "a.go", false},
		{"./", "main.go", false},
		{"./", "README.md", false},
		{"./", "docs/a.md", false},
		{"./", "docs/a/b.md", false},
		{"./", "src/a/b.go", false},
		{"./", "src/a.ts", false},
		{"./", "cmd/main.go", false},
		{"./", "a/b", false},
		{"./", "a/x/y/b", false},
		{"./", "[abc].md", false},
		{"./", "{a,b}.md", false},
		{"./", "z.md", false},
		{"./", "docs/..", false},
		{"./", "//", false},
		{"/", "", false},
		{"/", ".", false},
		{"/", "/", false},
		{"/", "docs", false},
		{"/", "a.go", false},
		{"/", "main.go", false},
		{"/", "README.md", false},
		{"/", "docs/a.md", false},
		{"/", "docs/a/b.md", false},
		{"/", "src/a/b.go", false},
		{"/", "src/a.ts", false},
		{"/", "cmd/main.go", false},
		{"/", "a/b", false},
		{"/", "a/x/y/b", false},
		{"/", "[abc].md", false},
		{"/", "{a,b}.md", false},
		{"/", "z.md", false},
		{"/", "docs/..", false},
		{"/", "//", false},
		{"//", "", false},
		{"//", ".", false},
		{"//", "/", false},
		{"//", "docs", false},
		{"//", "a.go", false},
		{"//", "main.go", false},
		{"//", "README.md", false},
		{"//", "docs/a.md", false},
		{"//", "docs/a/b.md", false},
		{"//", "src/a/b.go", false},
		{"//", "src/a.ts", false},
		{"//", "cmd/main.go", false},
		{"//", "a/b", false},
		{"//", "a/x/y/b", false},
		{"//", "[abc].md", false},
		{"//", "{a,b}.md", false},
		{"//", "z.md", false},
		{"//", "docs/..", false},
		{"//", "//", false},
		{".", "", false},
		{".", ".", false},
		{".", "/", false},
		{".", "docs", false},
		{".", "a.go", false},
		{".", "main.go", false},
		{".", "README.md", false},
		{".", "docs/a.md", false},
		{".", "docs/a/b.md", false},
		{".", "src/a/b.go", false},
		{".", "src/a.ts", false},
		{".", "cmd/main.go", false},
		{".", "a/b", false},
		{".", "a/x/y/b", false},
		{".", "[abc].md", false},
		{".", "{a,b}.md", false},
		{".", "z.md", false},
		{".", "docs/..", false},
		{".", "//", false},
		{"/docs", "", false},
		{"/docs", ".", false},
		{"/docs", "/", false},
		{"/docs", "docs", true},
		{"/docs", "a.go", false},
		{"/docs", "main.go", false},
		{"/docs", "README.md", false},
		{"/docs", "docs/a.md", false},
		{"/docs", "docs/a/b.md", false},
		{"/docs", "src/a/b.go", false},
		{"/docs", "src/a.ts", false},
		{"/docs", "cmd/main.go", false},
		{"/docs", "a/b", false},
		{"/docs", "a/x/y/b", false},
		{"/docs", "[abc].md", false},
		{"/docs", "{a,b}.md", false},
		{"/docs", "z.md", false},
		{"/docs", "docs/..", false},
		{"/docs", "//", false},
		{"///", "", false},
		{"///", ".", false},
		{"///", "/", false},
		{"///", "docs", false},
		{"///", "a.go", false},
		{"///", "main.go", false},
		{"///", "README.md", false},
		{"///", "docs/a.md", false},
		{"///", "docs/a/b.md", false},
		{"///", "src/a/b.go", false},
		{"///", "src/a.ts", false},
		{"///", "cmd/main.go", false},
		{"///", "a/b", false},
		{"///", "a/x/y/b", false},
		{"///", "[abc].md", false},
		{"///", "{a,b}.md", false},
		{"///", "z.md", false},
		{"///", "docs/..", false},
		{"///", "//", false},
		{".//", "", false},
		{".//", ".", false},
		{".//", "/", false},
		{".//", "docs", false},
		{".//", "a.go", false},
		{".//", "main.go", false},
		{".//", "README.md", false},
		{".//", "docs/a.md", false},
		{".//", "docs/a/b.md", false},
		{".//", "src/a/b.go", false},
		{".//", "src/a.ts", false},
		{".//", "cmd/main.go", false},
		{".//", "a/b", false},
		{".//", "a/x/y/b", false},
		{".//", "[abc].md", false},
		{".//", "{a,b}.md", false},
		{".//", "z.md", false},
		{".//", "docs/..", false},
		{".//", "//", false},
		{"/./", "", false},
		{"/./", ".", false},
		{"/./", "/", false},
		{"/./", "docs", false},
		{"/./", "a.go", false},
		{"/./", "main.go", false},
		{"/./", "README.md", false},
		{"/./", "docs/a.md", false},
		{"/./", "docs/a/b.md", false},
		{"/./", "src/a/b.go", false},
		{"/./", "src/a.ts", false},
		{"/./", "cmd/main.go", false},
		{"/./", "a/b", false},
		{"/./", "a/x/y/b", false},
		{"/./", "[abc].md", false},
		{"/./", "{a,b}.md", false},
		{"/./", "z.md", false},
		{"/./", "docs/..", false},
		{"/./", "//", false},
		{"docs/..", "", false},
		{"docs/..", ".", false},
		{"docs/..", "/", false},
		{"docs/..", "docs", false},
		{"docs/..", "a.go", false},
		{"docs/..", "main.go", false},
		{"docs/..", "README.md", false},
		{"docs/..", "docs/a.md", false},
		{"docs/..", "docs/a/b.md", false},
		{"docs/..", "src/a/b.go", false},
		{"docs/..", "src/a.ts", false},
		{"docs/..", "cmd/main.go", false},
		{"docs/..", "a/b", false},
		{"docs/..", "a/x/y/b", false},
		{"docs/..", "[abc].md", false},
		{"docs/..", "{a,b}.md", false},
		{"docs/..", "z.md", false},
		{"docs/..", "docs/..", false},
		{"docs/..", "//", false},
		{"", "", false},
		{"", ".", false},
		{"", "/", false},
		{"", "docs", false},
		{"", "a.go", false},
		{"", "main.go", false},
		{"", "README.md", false},
		{"", "docs/a.md", false},
		{"", "docs/a/b.md", false},
		{"", "src/a/b.go", false},
		{"", "src/a.ts", false},
		{"", "cmd/main.go", false},
		{"", "a/b", false},
		{"", "a/x/y/b", false},
		{"", "[abc].md", false},
		{"", "{a,b}.md", false},
		{"", "z.md", false},
		{"", "docs/..", false},
		{"", "//", false},
		{"docs//**", "", false},
		{"docs//**", ".", false},
		{"docs//**", "/", false},
		{"docs//**", "docs", true},
		{"docs//**", "a.go", false},
		{"docs//**", "main.go", false},
		{"docs//**", "README.md", false},
		{"docs//**", "docs/a.md", true},
		{"docs//**", "docs/a/b.md", true},
		{"docs//**", "src/a/b.go", false},
		{"docs//**", "src/a.ts", false},
		{"docs//**", "cmd/main.go", false},
		{"docs//**", "a/b", false},
		{"docs//**", "a/x/y/b", false},
		{"docs//**", "[abc].md", false},
		{"docs//**", "{a,b}.md", false},
		{"docs//**", "z.md", false},
		{"docs//**", "docs/..", false},
		{"docs//**", "//", false},
		{"\\*.go", "", false},
		{"\\*.go", ".", false},
		{"\\*.go", "/", false},
		{"\\*.go", "docs", false},
		{"\\*.go", "a.go", false},
		{"\\*.go", "main.go", false},
		{"\\*.go", "README.md", false},
		{"\\*.go", "docs/a.md", false},
		{"\\*.go", "docs/a/b.md", false},
		{"\\*.go", "src/a/b.go", false},
		{"\\*.go", "src/a.ts", false},
		{"\\*.go", "cmd/main.go", false},
		{"\\*.go", "a/b", false},
		{"\\*.go", "a/x/y/b", false},
		{"\\*.go", "[abc].md", false},
		{"\\*.go", "{a,b}.md", false},
		{"\\*.go", "z.md", false},
		{"\\*.go", "docs/..", false},
		{"\\*.go", "//", false},
		{"a/**/**/**/b", "", false},
		{"a/**/**/**/b", ".", false},
		{"a/**/**/**/b", "/", false},
		{"a/**/**/**/b", "docs", false},
		{"a/**/**/**/b", "a.go", false},
		{"a/**/**/**/b", "main.go", false},
		{"a/**/**/**/b", "README.md", false},
		{"a/**/**/**/b", "docs/a.md", false},
		{"a/**/**/**/b", "docs/a/b.md", false},
		{"a/**/**/**/b", "src/a/b.go", false},
		{"a/**/**/**/b", "src/a.ts", false},
		{"a/**/**/**/b", "cmd/main.go", false},
		{"a/**/**/**/b", "a/b", true},
		{"a/**/**/**/b", "a/x/y/b", true},
		{"a/**/**/**/b", "[abc].md", false},
		{"a/**/**/**/b", "{a,b}.md", false},
		{"a/**/**/**/b", "z.md", false},
		{"a/**/**/**/b", "docs/..", false},
		{"a/**/**/**/b", "//", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestNormalize pins the canonical spelling that instruction frontmatter now
// stores its applyTo and paths patterns in, so that the pattern `cite doctor`
// reports is the one paths_ignore would also have understood.
//
// A pattern with no canonical spelling at all normalises to "". That is not
// "match everything": frontmatter keeps such a pattern verbatim rather than
// dropping it, because dropping it would turn a match-nothing pattern into a
// match-everything one (discover.go reads an empty paths list as "declared no
// paths" and widens the file to repository-wide).
func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"docs/**", "docs/**"},
		{"/docs/**", "docs/**"},
		{"//docs/**", "docs/**"},
		{"./docs/**", "docs/**"},
		{"docs//**", "docs/**"},
		{"docs/", "docs"},
		{"./docs/*.md", "docs/*.md"},
		{"a//b/*.go", "a/b/*.go"},
		{"docs/./a.md", "docs/a.md"},
		{"docs/x/../a.md", "docs/a.md"},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"/", ""},
		{"//", ""},
		{"///", ""},
		{"./", ""},
		{".//", ""},
		{"/./", ""},
		{"docs/..", ""},
		{"/docs/..", ""},
		{"a/../..", ""},
		{"../docs", "docs"},
		{".././docs", "docs"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeAndMatchAgree pins the invariant the Normalize doc states.
// It is one-directional: a pattern that normalises to the empty string
// specifies no segment, and so it cannot match any name.
//
//	Normalize(pattern) == ""  =>  Match(pattern, name) == false, for any name
//
// The converse is deliberately not asserted and must not be. A pattern that
// keeps a segment can still match no name, because that name may simply be
// somewhere else: "docs" is not inert and does not match "a.go". Reading the
// implication as an equivalence would demand that every non-inert pattern
// match every name, which is not a property any matcher has.
//
// The leading-".." cases are here because a reader can reasonably expect ".."
// to be able to escape upward, and it cannot.
func TestNormalizeAndMatchAgree(t *testing.T) {
	patterns := []string{"", ".", "..", "/", "//", "./", "docs/..", "../docs", "docs", "docs/**", "**", "../**"}
	names := []string{"", "a.go", "docs", "docs/a.md", "../docs", "..", "/docs/a.md"}
	for _, p := range patterns {
		for _, n := range names {
			inert, matched := Normalize(p) == "", Match(p, n)
			if inert && matched {
				t.Errorf("Match(%q, %q) = true, but Normalize(%q) = %q, so the pattern specifies no segment and cannot match", p, n, p, Normalize(p))
			}
		}
	}
	// The two named contrasts the package doc relies on.
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"docs/**", "docs", true},
		{"docs/**", "", false},
		{"docs/**", "docs/..", false},
		// A leading ".." is resolved away, so "../docs" is "docs" and
		// nothing more: it matches the directory itself, not its contents.
		{"../docs", "docs", true},
		{"../docs", "docs/a.md", false},
		{"docs", "docs", true},
		{"..", "a.go", false},
	} {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
