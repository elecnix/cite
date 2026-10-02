// Package glob_test holds the cross-caller table: the one place that proves
// every exported entry point into the shared dialect answers identically.
// It lives outside package glob so it can import both owners (which import
// glob) without an import cycle.
package glob_test

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/glob"
	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/scope"
)

// table is the union of every (pattern, name) pair the two former dialects
// answered for themselves, plus the normalisation rows they disagreed on.
var table = []struct {
	pattern, name string
	want          bool
}{
	// paths_ignore rows (internal/scope, formerly untested-in-isolation)
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
	{"*.go", "dir/a.go", false},
	{"a?.go", "ab.go", true},
	{"a?.go", "a.go", false},
	{"[abc].go", "b.go", true},
	{"[abc].go", "d.go", false},
	{"[^abc].go", "d.go", true},

	// applyTo rows (internal/instructions)
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

	// the disagreements: normalisation applied to both sides
	{"/docs/**", "docs/a/b.md", true},
	{"./docs/*.md", "docs/a.md", true},
	{"a//b/*.go", "a/b/c.go", true},
	{"/docs/**", "docs/a.md", true},
	{"./docs/*.md", "docs/a/b.md", false},
	{"a//b/*.go", "a/b/", false},
	{"docs/", "docs/a.md", false},
	{"/internal/", "internal", true},
	{"//internal//**", "internal/a/b.go", true},
	{"docs/../docs/*.md", "docs/a.md", true},
	{"/docs/*.md", "./docs/a.md", true},
	{"**/", "a.go", true},
	{"[abc].go", "d.go", false},
	{"a\\*b.go", "a*b.go", true},
	{"", "a.go", false},
	{".", "a.go", false},
	{"/", "a.go", false},
}

// TestEveryCallerAgrees is the test that would have caught the fork: the
// same pattern against the same path must give the same answer whether it
// came from paths_ignore, applyTo, paths, or the dialect's own entry point.
func TestEveryCallerAgrees(t *testing.T) {
	for _, c := range table {
		want := glob.Match(c.pattern, c.name)
		if want != c.want {
			t.Errorf("glob.Match(%q, %q) = %v, want %v", c.pattern, c.name, want, c.want)
		}
		if got := scope.Match(c.pattern, c.name); got != want {
			t.Errorf("scope.Match(%q, %q) = %v, want %v (paths_ignore disagrees with applyTo)",
				c.pattern, c.name, got, want)
		}
		if got := instructions.Match(c.pattern, c.name); got != want {
			t.Errorf("instructions.Match(%q, %q) = %v, want %v (applyTo disagrees with paths_ignore)",
				c.pattern, c.name, got, want)
		}
	}
}

// TestPreviouslyInertApplyTo is the end-to-end proof that the pattern an
// author would reasonably write now reaches the files it names. Before the
// dialects were unified, "applyTo: \"./docs/**\"" matched nothing at all and
// the file was silently inert.
func TestPreviouslyInertApplyTo(t *testing.T) {
	cases := []struct {
		applyTo, hit, miss string
	}{
		{"./docs/**", "docs/a/b.md", "a/b/c.go"},
		{"/docs/**", "docs/a/b.md", "a/b/c.go"},
		{"docs//**", "docs/a/b.md", "a/b/c.go"},
		{"docs/", "docs", "docs/a/b.md"}, // trailing slash names the directory itself
		{"a//b/*.go", "a/b/c.go", "docs/a/b.md"},
	}
	for _, c := range cases {
		tree := fakeTree{files: map[string][]byte{
			".github/instructions/docs.instructions.md": []byte(
				"---\napplyTo: \"" + c.applyTo + "\"\n---\n## Docs\n" + c.applyTo + " CHECKABLE rule\n"),
		}}
		resolved, warns, err := instructions.Resolve(tree, []string{c.hit, c.miss}, nil)
		if err != nil {
			t.Fatalf("applyTo %q: Resolve: %v", c.applyTo, err)
		}
		if len(warns) != 0 {
			t.Errorf("applyTo %q: unexpected warnings %v", c.applyTo, warns)
		}
		if got := resolved.For(c.hit); len(got) != 1 {
			t.Errorf("applyTo %q matched %d sections for %s, want 1", c.applyTo, len(got), c.hit)
		}
		if n := len(resolved.For(c.miss)); n != 0 {
			t.Errorf("applyTo %q matched %d sections for %s, want 0", c.applyTo, n, c.miss)
		}
	}
}

// TestInertApplyToIsReportable pairs the fix with the diagnosis: the resolver
// is the only holder of the changed paths, so glob.Unmatched is what turns a
// match-nothing applyTo from an invisible no-op into a nameable observation.
func TestInertApplyToIsReportable(t *testing.T) {
	patterns := []string{"docs/**", "/docs/**", "./docs/**", "a//b/*.go", "cmd/**"}
	changed := []string{"docs/a/b.md", "a/b/c.go"}
	unmatched := glob.Unmatched(patterns, changed)
	if len(unmatched) != 1 || unmatched[0] != "cmd/**" {
		t.Fatalf("Unmatched = %q, want [\"cmd/**\"]", unmatched)
	}
}

// TestFrontmatterCanonicalisation proves the frontmatter layer stores the
// canonical spelling, so what `cite doctor` prints is what paths_ignore would
// have understood, and so two spellings of one pattern cannot rank apart.
func TestFrontmatterCanonicalisation(t *testing.T) {
	tree := fakeTree{files: map[string][]byte{
		".github/instructions/a.instructions.md": []byte("---\napplyTo: \"./docs/**\"\n---\n## A\nA CHECKABLE\n"),
		".github/instructions/b.instructions.md": []byte("---\napplyTo: \"docs/**\"\n---\n## B\nB CHECKABLE\n"),
	}}
	resolved, _, err := instructions.Resolve(tree, []string{"docs/a/b.md"}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := resolved.For("docs/a/b.md")
	if len(got) != 2 {
		t.Fatalf("both spellings name the same files, got %d sections: %v", len(got), headings(got))
	}
	// Specificity ordering runs on the canonical form, so the two spellings
	// are ordered by their source path, not by a stray leading "./".
	if !strings.HasSuffix(got[0].SourceFile, "/a.instructions.md") {
		t.Errorf("first section came from %q, want the lexically first source", got[0].SourceFile)
	}
}

func headings(sections []instructions.ResolvedSection) string {
	var out []string
	for _, s := range sections {
		out = append(out, s.SourceFile+"::"+s.Heading)
	}
	return strings.Join(out, " | ")
}

// fakeTree is a minimal instructions.Tree backed by a map.
type fakeTree struct{ files map[string][]byte }

func (f fakeTree) List(dir string) ([]string, error) {
	var out []string
	for p := range f.files {
		if dir == "" || strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	sortStrings(out)
	return out, nil
}

func (f fakeTree) Read(path string) ([]byte, bool, error) {
	b, ok := f.files[path]
	return b, ok, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
