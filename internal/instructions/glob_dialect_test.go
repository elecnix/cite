package instructions

import (
	"reflect"
	"testing"

	"github.com/elecnix/cite/internal/glob"
)

// The dialect itself is pinned once, in internal/glob, and this package calls
// glob.Match directly. The tables that used to live here — a corpus
// cross-checked against internal/scope.Match, and a normalisation table —
// moved there; with one matcher they are not cross-checks but duplicates.
// What has to stay here is the narrower claim this package owns: that
// applyTo/paths frontmatter reaches that one matcher, and that the pattern it
// stores is the canonical one.

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

// TestApplyToUsesTheSharedDialect is the frontmatter-side claim that
// TestPathsIgnoreUsesTheSharedDialect makes on the cite.yml side: for the
// spellings the two surfaces once disagreed on, an applyTo and the same
// pattern in paths_ignore now agree, because both call glob.Match.
//
// It asserts through the resolver rather than through a matcher, so it fails
// if applyTo ever stops being the thing that decides applicability.
func TestApplyToUsesTheSharedDialect(t *testing.T) {
	const changed = "docs/guide/page.md"
	cases := []struct {
		pattern string
		want    bool
	}{
		{"docs/**", true},
		{"/docs/**", true},
		{"./docs/**", true},
		{"docs//**", true},
		{"docs/**/*.md", true},
		{"./docs/**/*.md", true},
		{"docs/guide/*.md", true},
		{"docs/[a-z]*/page.md", true},
		// 'docs/' names the directory itself, not its contents.
		{"docs/", false},
		// '*' never crosses a '/', so this is two segments against three.
		{"docs/*.md", false},
		{"docs/*.go", false},
		{"src/**", false},
		{"", false},
		{".", false},
		{"/", false},
	}
	for _, c := range cases {
		tree := ft(map[string]string{
			".github/instructions/docs.instructions.md": "---\napplyTo: \"" + c.pattern + "\"\n---\n## Docs\ndocs CHECKABLE rule\n",
		})
		loaded := len(headings(mustResolve(t, tree, changed), changed)) == 1
		if loaded != c.want {
			t.Errorf("applyTo %q against %q: loaded = %v, want %v", c.pattern, changed, loaded, c.want)
			continue
		}
		if want := glob.Match(c.pattern, changed); want != c.want {
			t.Errorf("applyTo %q and glob.Match disagree: loaded = %v, glob.Match = %v", c.pattern, loaded, want)
		}
	}
}

// TestFrontmatterStoresTheCanonicalPattern covers the change the two-dialect
// fix did not need but the single owner makes possible: applyTo and paths are
// canonicalised at parse time, so the pattern the resolver compares and
// `cite doctor` prints is the same spelling paths_ignore would have
// understood.
//
// A pattern with no canonical spelling is kept verbatim rather than dropped.
// Dropping it would be worse than leaving it: discover.go reads an empty paths
// list as "declared no paths" and widens the file to repository-wide, so
// dropping a match-nothing pattern would turn it into a match-everything one.
func TestFrontmatterStoresTheCanonicalPattern(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"docs/**", "docs/**"},
		{"/docs/**", "docs/**"},
		{"./docs/**", "docs/**"},
		{"docs//**", "docs/**"},
		{"docs/", "docs"},
		{"./docs/*.md", "docs/*.md"},
		// No canonical spelling: kept as written, still inert.
		{".", "."},
		{"/", "/"},
		{"//", "//"},
	}
	for _, c := range cases {
		if got := splitCSV(c.in); len(got) != 1 || got[0] != c.want {
			t.Errorf("splitCSV(%q) = %q, want [%q]", c.in, got, c.want)
		}
	}
}

// TestSplitCSVCanonicalisesEveryElement covers the multi-pattern form, where
// a stray non-canonical entry must not defeat the ones beside it.
func TestSplitCSVCanonicalisesEveryElement(t *testing.T) {
	got := splitCSV("/docs/**, ./src/**, docs/*.md")
	want := []string{"docs/**", "src/**", "docs/*.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitCSV = %q, want %q", got, want)
	}
}
