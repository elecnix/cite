package glob

import "testing"

// dialectCase is one (pattern, name) pair. The table is the union of what
// paths_ignore (internal/scope.Match) and instruction frontmatter
// (internal/instructions.Match) used to answer for themselves — every row
// below was a case one of the two former dialects got right and the other
// got wrong, or a row that pins the shared grammar where they agreed.
type dialectCase struct {
	pattern, name string
	want          bool
	why           string
}

var dialectCases = []dialectCase{
	// --- the two former dialects already agreed -------------------------
	{"**/*.gen.go", "a/b/c.gen.go", true, "** spans zero or more segments"},
	{"**/*.gen.go", "c.gen.go", true, "** spans zero segments too"},
	{"**/*.gen.go", "a/b/c.go", false, "literal suffix must match"},
	{"docs/*.md", "docs/a.md", true, "single segment"},
	{"docs/*.md", "docs/x/a.md", false, "* never crosses a slash"},
	{"docs/**/*.md", "docs/x/a.md", true, "** after a literal segment"},
	{"docs/**/*.md", "docs/a.md", true, "** may span zero segments"},
	{"**", "anything/at/all.go", true, "bare ** matches every path"},
	{"internal/**", "internal/a/b.go", true, "trailing ** swallows the rest"},
	{"internal/**", "internalX/a.go", false, "segment boundary is exact"},
	{"*.go", "a.go", true, ""},
	{"*.go", "dir/a.go", false, ""},
	{"a?.go", "ab.go", true, "? is exactly one character"},
	{"a?.go", "a.go", false, ""},
	{"cmd/?ain.go", "cmd/rain.go", true, ""},
	{"cmd/?ain.go", "cmd/two.go", false, ""},
	{"**/*.go", "main.got", false, ""},
	{"**/*.go", "main.go", true, ""},
	{"*.md", "README.md", true, ""},
	{"*.md", "docs/README.md", false, ""},
	{"src/**/*.ts", "src/a/b.ts", true, ""},
	{"src/**/*.ts", "src/a.ts", true, ""},
	{"src/**/*.ts", "lib/a.ts", false, ""},
	{"internal/**/*.go", "internal/a/b.go", true, ""},

	// --- normalisation: the rows the review measured ---------------------
	{"/docs/**", "docs/a/b.md", true, "leading slash is dropped"},
	{"./docs/*.md", "docs/a.md", true, "leading ./ is dropped"},
	{"a//b/*.go", "a/b/c.go", true, "duplicate slash collapses"},

	// --- the same normalisation, further out -----------------------------
	{"/**", "a.go", true, "leading slash on a bare **"},
	{"./**", "deep/a/b.go", true, "leading ./ on a bare **"},
	{"**/", "a.go", true, "trailing slash is dropped"},
	{"/internal/", "internal", true, "trailing slash names the directory itself"},
	{"//internal//**", "internal/a/b.go", true, "leading and inner duplicate slashes"},
	{"docs/../docs/*.md", "docs/a.md", true, ".. is resolved"},
	{"/docs/*.md", "./docs/a.md", true, "the name is normalised too"},
	{"docs/*.md", "docs//a.md", true, "duplicate slash in the name"},

	// --- character classes and escapes (paths_ignore only, before) --------
	{"[abc].go", "b.go", true, "[...] is a character class"},
	{"[abc].go", "d.go", false, ""},
	{"[a-c].go", "c.go", true, "ranges inside a class"},
	{"[^abc].go", "d.go", true, "^ negates a class (path.Match has no !)"},
	{"[*].go", "*.go", true, "a class can name a literal star"},
	{"a\\*b.go", "a*b.go", true, "backslash escapes the next character"},
	{"a\\*b.go", "axb.go", false, ""},

	// --- inert patterns match nothing, not everything --------------------
	{"", "a.go", false, "an empty pattern matches nothing"},
	{".", "a.go", false, "'.' has no segments"},
	{".", "", false, "not even the empty path"},
	{"/", "a.go", false, "the root is not a pattern"},
	{"./", "docs/a.md", false, ""},
	{"docs/..", "a.go", false, "resolves to the root"},
	{"a//b/*.go", "a/b/", false, "a pattern cannot match a prefix"},

	// --- the empty pattern must not become a match-everything ----------
	{"**", "", true, "bare ** still matches the (degenerate) empty path"},
}

func TestDialect(t *testing.T) {
	for _, c := range dialectCases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v (%s)", c.pattern, c.name, got, c.want, c.why)
		}
	}
}

// TestNormalisationIsIdempotent pins the property the two former dialects
// disagreed about: every spelling of a pattern collapses to one canonical
// form, so which spelling an author typed cannot change what it matches.
func TestNormalisationIsIdempotent(t *testing.T) {
	groups := [][]string{
		{"docs/**/*.md", "/docs/**/*.md", "./docs/**/*.md", "//docs//**//*.md", "docs/./**/*.md", "docs/x/../**/*.md"},
		{"**", "/**", "./**", "**/", "/**/"},
		{"a/b/*.go", "./a/b/*.go", "/a//b/*.go"},
	}
	for _, g := range groups {
		want := Normalize(g[0])
		for _, p := range g {
			if got := Normalize(p); got != want {
				t.Errorf("Normalize(%q) = %q, want %q (same as Normalize(%q))", p, got, want, g[0])
			}
			if Normalize(Normalize(p)) != want {
				t.Errorf("Normalize is not idempotent for %q", p)
			}
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{".", ""},
		{"/", ""},
		{"./", ""},
		{"//", ""},
		{"docs/..", ""},
		{"/docs/**", "docs/**"},
		{"./docs/*.md", "docs/*.md"},
		{"a//b/*.go", "a/b/*.go"},
		{"docs/", "docs"},
		{"**/", "**"},
		{"docs/../docs/*.md", "docs/*.md"},
		{"[a-c].go", "[a-c].go"},
		{"a\\*b.go", "a\\*b.go"},
		{"a..b/c.go", "a..b/c.go"}, // only a whole ".." segment is a parent
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInert(t *testing.T) {
	inert := []string{"", ".", "/", "./", "//", "docs/..", "///docs/../.."}
	for _, p := range inert {
		if !Inert(p) {
			t.Errorf("Inert(%q) = false, want true", p)
		}
	}
	live := []string{"**", "docs", "docs/**", ".github/instructions/*.md"}
	for _, p := range live {
		if Inert(p) {
			t.Errorf("Inert(%q) = true, want false", p)
		}
	}
	// An inert pattern is exactly a pattern that matches no path at all.
	probe := []string{"a.go", "docs/a.md", "x", "", "a/b/c.go"}
	for _, p := range inert {
		for _, n := range probe {
			if Match(p, n) {
				t.Errorf("Match(%q, %q) = true, but Inert(%q) = true", p, n, p)
			}
		}
	}
}

func TestUnmatched(t *testing.T) {
	paths := []string{"docs/a.md", "internal/scope/glob.go"}
	got := Unmatched([]string{"docs/**", "./docs/**", "internal/**/*.go", "cmd/**", "cmd/**", "./nope"}, paths)
	// "docs/**" and "./docs/**" are the same pattern and both hit; "cmd/**"
	// appears once in the answer; the ./ spelling of an inert pattern is kept.
	want := []string{"cmd/**", "./nope"}
	if len(got) != len(want) {
		t.Fatalf("Unmatched = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Unmatched = %q, want %q", got, want)
		}
	}
	if n := len(Unmatched([]string{"docs/**", "internal/**/*.go"}, paths)); n != 0 {
		t.Errorf("Unmatched reported %d patterns that do match, want 0", n)
	}
}

func TestUnmatchedFindsThePreviouslyInertApplyTo(t *testing.T) {
	// The whole point: an applyTo that matches nothing has to be nameable.
	paths := []string{"docs/a/b.md", "README.md"}
	got := Unmatched([]string{"/docs/**", "README.md"}, paths)
	if len(got) != 0 {
		t.Fatalf("Unmatched = %q, want none once normalisation is shared", got)
	}
	old := Unmatched([]string{"docs/**/*.md", "*.md"}, []string{"README.md"})
	if len(old) != 1 || old[0] != "docs/**/*.md" {
		t.Fatalf("Unmatched = %q, want the one pattern that missed", old)
	}
}

func BenchmarkMatch(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Match("internal/**/*.go", "internal/scope/glob.go")
	}
}
