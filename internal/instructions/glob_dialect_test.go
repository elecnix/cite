package instructions

import (
	"testing"

	"github.com/elecnix/cite/internal/scope"
)

// TestMatchUsesTheDocumentedDialect is the regression test for the two-dialect
// defect. CONFORMANCE.md documents one glob dialect for applyTo and
// paths_ignore alike, so every row here has to agree with what
// internal/scope.Match answers for the same pair. Before this package shared
// scope's matcher it split the pattern on '/' with no normalisation at all,
// so an applyTo such as "./docs/**" or "/docs/**" matched nothing at all and
// the instruction file silently never loaded.
func TestMatchUsesTheDocumentedDialect(t *testing.T) {
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
		// that normalises to the repository root.
		{"", "a.md", false},
		{"", "", false},
		{"/", "", false},
		{"//", "", false},
		{"./", "", false},
		{".", "", false},
		{"/", "README.md", false},
		{"//", "README.md", false},
		{"./", "README.md", false},

		// '**' still matches the root name, so it is not one of the
		// spellings above.
		{"**", "", true},
		{"docs/**", "docs", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestApplyToWithNormalisedGlobLoads is the user-visible consequence of the
// two-dialect defect. An instruction file whose applyTo carries a './' prefix
// or a leading slash used to match no file at all: its instructions never
// loaded, for any changed path, and nothing reported the mismatch. The same
// pattern in paths_ignore worked, so the two features disagreed in a way no
// user could predict.
func TestApplyToWithNormalisedGlobLoads(t *testing.T) {
	const changed = "docs/guide/page.md"
	for _, applyTo := range []string{"./docs/**", "/docs/**", "docs/**", "docs//**"} {
		tree := ft(map[string]string{
			".github/instructions/docs.instructions.md": "---\napplyTo: \"" + applyTo + "\"\n---\n## Docs\ndocs CHECKABLE rule\n",
		})
		got := headings(mustResolve(t, tree, changed), changed)
		if len(got) != 1 {
			t.Errorf("applyTo %q matched %d instruction files for %q, want 1", applyTo, len(got), changed)
		}
	}
	// A pattern that genuinely does not cover the changed file must still not
	// match, so the normalisation does not over-match.
	tree := ft(map[string]string{
		".github/instructions/other.instructions.md": "---\napplyTo: \"src/**\"\n---\n## Other\nother CHECKABLE rule\n",
	})
	if got := headings(mustResolve(t, tree, changed), changed); len(got) != 0 {
		t.Errorf(`applyTo "src/**" matched %v for %q, want no match`, got, changed)
	}
}

// TestMatchAgreesWithScope is the guard against the next divergence. Both
// Match functions are exercised over the same corpus and must answer
// identically for every pair, whether or not the value is the "right" one.
func TestMatchAgreesWithScope(t *testing.T) {
	patterns := []string{
		"", "*", "*.go", "**", "**/*", "**/*.go", "docs/*.md", "docs/**",
		"src/**", "src/**/*.ts", "cmd/?ain.go", "a/**/b", "**/**/*.go",
		"/docs/**", "./docs/*.md", "a//b/*.go", "/**", "[abc].md",
		"docs/[a-c]*.md", "{a,b}.md", "[.md", "./", "/", "//", ".", "/docs",
	}
	names := []string{
		"", ".", "/", "docs", "a.go", "main.go", "README.md", "docs/a.md",
		"docs/a/b.md", "src/a/b.go", "src/a.ts", "cmd/main.go", "a/b",
		"a/x/y/b", "[abc].md", "{a,b}.md", "z.md",
	}
	for _, p := range patterns {
		for _, n := range names {
			if got, want := Match(p, n), scope.Match(p, n); got != want {
				t.Errorf("instructions.Match(%q, %q) = %v but scope.Match = %v", p, n, got, want)
			}
		}
	}
}
