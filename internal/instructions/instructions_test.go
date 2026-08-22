package instructions

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// fakeTree is a Tree backed by a map, per the fixture requirement.
type fakeTree struct {
	files map[string][]byte
}

func (f fakeTree) List(dir string) ([]string, error) {
	var out []string
	for p := range f.files {
		if dir == "" || strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f fakeTree) Read(path string) ([]byte, bool, error) {
	b, ok := f.files[path]
	return b, ok, nil
}

func ft(files map[string]string) fakeTree {
	m := make(map[string][]byte, len(files))
	for k, v := range files {
		m[k] = []byte(v)
	}
	return fakeTree{files: m}
}

// stubClassifier marks sections containing "CHECKABLE" reviewable and
// everything else authoring; it counts calls to verify hash caching.
type stubClassifier struct{ calls int }

func (s *stubClassifier) Classify(file string, sections []Section) ([]ClassifiedSection, error) {
	s.calls++
	out := make([]ClassifiedSection, len(sections))
	for i, sec := range sections {
		k := KindAuthoring
		if strings.Contains(sec.Text, "CHECKABLE") {
			k = KindReviewable
		}
		out[i] = ClassifiedSection{Section: sec, Kind: k}
	}
	return out, nil
}

func headings(r *ResolvedInstructions, path string) []string {
	var out []string
	for _, s := range r.For(path) {
		out = append(out, s.SourceFile+"::"+s.Heading)
	}
	return out
}

func TestGlobDialect(t *testing.T) {
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

func TestPrecedenceGlobSpecificity(t *testing.T) {
	tree := ft(map[string]string{
		".github/instructions/a.instructions.md": "---\napplyTo: \"**/*.go\"\n---\n## Broad\nbroad CHECKABLE rule\n",
		".github/instructions/z.instructions.md": "---\napplyTo: \"internal/**/*.go\"\n---\n## Narrow\nnarrow CHECKABLE rule\n",
	})
	got := headings(mustResolve(t, tree, "internal/auth/login.go"), "internal/auth/login.go")
	if len(got) != 2 || !strings.HasPrefix(got[0], ".github/instructions/z.instructions.md") {
		t.Fatalf("most specific glob should come first, got %v", got)
	}

	// Equal wildcards: longer pattern first.
	tree = ft(map[string]string{
		".github/instructions/short.instructions.md": "---\napplyTo: \"src/*.go\"\n---\n## Short\nx CHECKABLE\n",
		".github/instructions/longg.instructions.md": "---\napplyTo: \"src/ma*.go\"\n---\n## Long\ny CHECKABLE\n",
	})
	got = headings(mustResolve(t, tree, "src/main.go"), "src/main.go")
	if len(got) != 2 || !strings.HasPrefix(got[0], ".github/instructions/longg") {
		t.Fatalf("longer pattern should win on equal wildcards, got %v", got)
	}
}

func TestApplyToArrayFormAlias(t *testing.T) {
	tree := ft(map[string]string{
		".github/instructions/list.instructions.md": "---\napplyTo:\n  - \"**/*.rs\"\n  - \"**/*.go\"\n---\n## Rust and Go\nrule CHECKABLE\n",
	})
	r := mustResolve(t, tree, "crates/x/src/lib.rs")
	if n := len(r.For("crates/x/src/lib.rs")); n != 1 {
		t.Fatalf("YAML array applyTo should match, got %d sections", n)
	}
	if len(r.For("readme.txt")) != 0 {
		t.Fatal("array applyTo must not match unrelated paths")
	}
}

func TestExcludeAgentSkipsFile(t *testing.T) {
	tree := ft(map[string]string{
		".github/instructions/optout.instructions.md": "---\napplyTo: \"**/*.go\"\nexcludeAgent: code-review\n---\n## Hidden\nnot for cite CHECKABLE\n",
		".github/instructions/stay.instructions.md":   "---\napplyTo: \"**/*.go\"\n---\n## Kept\nstays CHECKABLE\n",
	})
	r := mustResolve(t, tree, "a/b.go")
	for _, s := range r.For("a/b.go") {
		if strings.Contains(s.Text, "not for cite") {
			t.Fatal("excludeAgent: code-review file must be skipped")
		}
	}
	if len(r.For("a/b.go")) != 1 {
		t.Fatalf("expected only the non-excluded file's section, got %v", headings(r, "a/b.go"))
	}
}

func TestNearestAgentsMdWins(t *testing.T) {
	tree := ft(map[string]string{
		"AGENTS.md":         "## Root rules\nroot guidance CHECKABLE\n",
		"cmd/AGENTS.md":     "## Cmd rules\ncmd guidance CHECKABLE\n",
		".github/AGENTS.md": "## GH rules\ngh guidance CHECKABLE\n",
		".github/ci.yml":    "irrelevant\n",
	})
	r := mustResolve(t, tree, "cmd/build.go", "README.md", ".github/workflows/ci.yml")

	// cmd/build.go: nearest is cmd/AGENTS.md only.
	if got := headings(r, "cmd/build.go"); len(got) != 1 || !strings.Contains(got[0], "cmd/AGENTS.md") {
		t.Fatalf("nearest AGENTS.md wins: got %v", got)
	}
	// README.md: root AGENTS.md applies repository-wide.
	if got := headings(r, "README.md"); len(got) != 1 || !strings.Contains(got[0], "::Root rules") {
		t.Fatalf("root AGENTS.md fallback: got %v", got)
	}
	// .github/workflows/ci.yml: .github/AGENTS.md applies under .github/ only,
	// and beats root as the nearest file.
	if got := headings(r, ".github/workflows/ci.yml"); len(got) != 1 || !strings.Contains(got[0], ".github/AGENTS.md") {
		t.Fatalf(".github/AGENTS.md nearest for subtree: got %v", got)
	}
}

func TestRepoWideRanksOrdering(t *testing.T) {
	tree := ft(map[string]string{
		".github/copilot-instructions.md": "## Copilot\ncopilot rule CHECKABLE\n",
		"AGENTS.md":                       "## Agents\nagents rule CHECKABLE\n",
		"CLAUDE.md":                       "## Claude\nclaude rule CHECKABLE\n",
		"GEMINI.md":                       "## Gemini\ngemini rule CHECKABLE\n",
		"REVIEW.md":                       "## Review doc\nreview rule CHECKABLE\n",
	})
	got := headings(mustResolve(t, tree, "any/file.rb"), "any/file.rb")
	want := []string{
		".github/copilot-instructions.md::Copilot",
		"AGENTS.md::Agents",
		"CLAUDE.md::Claude",
		"GEMINI.md::Gemini",
		"REVIEW.md::Review doc",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rank order:\n got %v\nwant %v", got, want)
	}
}

func TestClaudeRulesPathsFrontmatter(t *testing.T) {
	tree := ft(map[string]string{
		".claude/rules/go.md":  "---\npaths:\n  - \"**/*.go\"\n---\n## Go style\nidiomatic Go CHECKABLE\n",
		".claude/rules/all.md": "---\npaths: \"**/*.md\"\n---\n## Docs\nprose rule CHECKABLE\n",
	})
	r := mustResolve(t, tree, "x/y.go", "docs/note.md")
	if got := headings(r, "x/y.go"); len(got) != 1 || !strings.Contains(got[0], "go.md") {
		t.Fatalf("rules paths glob: got %v", got)
	}
	if got := headings(r, "docs/note.md"); len(got) != 1 || !strings.Contains(got[0], "all.md") {
		t.Fatalf("rules paths single-string form: got %v", got)
	}
}

func TestSkillsSelectedByDescription(t *testing.T) {
	tree := ft(map[string]string{
		".github/skills/reviewer/SKILL.md": "---\nname: reviewer\ndescription: Runs a careful code review pass\n---\n## Skill body\nbe thorough CHECKABLE\n",
		".claude/skills/lint/SKILL.md":     "---\ndescription: Formats CSS files\n---\n## Not relevant\nignore me CHECKABLE\n",
	})
	r := mustResolve(t, tree, "src/app.ts")
	got := headings(r, "src/app.ts")
	if len(got) != 1 || !strings.Contains(got[0], ".github/skills/reviewer/SKILL.md") {
		t.Fatalf("description keyword heuristic (mentions \"review\") should select the skill: got %v", got)
	}
}

func TestReviewHeadingOverridesTriage(t *testing.T) {
	tree := ft(map[string]string{
		"AGENTS.md": "## Workflow\nalways rebase, never force-push\n\n## Review\ncheck every error path CHECKABLE\n\n### Sub\nsub detail CHECKABLE\n\n## After\nafter text\n",
	})
	cls := &stubClassifier{}
	r := mustResolveWithClassifier(t, cls, tree, "x.go")
	got := headings(r, "x.go")
	foundReview, foundSub := false, false
	for _, g := range got {
		if strings.Contains(g, "::Review") {
			foundReview = true
		}
		if strings.Contains(g, "::Sub") {
			foundSub = true
		}
		if strings.Contains(g, "Workflow") || strings.Contains(g, "After") {
			t.Fatalf("non-Review sections are subject to triage and were authoring here: %v", got)
		}
	}
	if !foundReview || !foundSub {
		t.Fatalf("## Review span (with subsections) must win wholesale: %v", got)
	}
}

func TestUsageCountingAndCacheByHash(t *testing.T) {
	content := "## A\nauthoring workflow\n\n## B\nCHECKABLE rule\n"
	tree := ft(map[string]string{
		"AGENTS.md":                       content,
		".github/copilot-instructions.md": content, // same bytes: one classify call by hash
	})
	cls := &stubClassifier{}
	r := mustResolveWithClassifier(t, cls, tree, "a.go", "b.go")

	usage := r.Usage()
	if len(usage) != 2 {
		t.Fatalf("expected usage for 2 source files, got %d", len(usage))
	}
	for _, u := range usage {
		if u.TotalSections != 2 || u.UsedSections != 1 || u.AuthoringSections != 1 {
			t.Fatalf("usage N of M with K authoring: got %+v", u)
		}
	}
	if cls.calls != 1 {
		t.Fatalf("classification must be cached by sha256 of content: %d calls for identical files", cls.calls)
	}
}

func TestNoopClassifierMarksAllReviewable(t *testing.T) {
	tree := ft(map[string]string{"AGENTS.md": "## X\nwhatever\n"})
	r := mustResolveWithClassifier(t, nil, tree, "f.go") // nil = NoopClassifier
	secs := r.For("f.go")
	if len(secs) != 1 || secs[0].Kind != KindReviewable {
		t.Fatalf("noop classifier: all sections reviewable, got %+v", secs)
	}
}

func TestSettingsDisableLocations(t *testing.T) {
	base := map[string]string{
		".github/copilot-instructions.md": "## Copilot\ncopilot rule CHECKABLE\n",
		"CLAUDE.md":                       "## Claude\nclaude rule CHECKABLE\n",
	}
	tree := ft(base)
	tree.files[".vscode/settings.json"] = []byte(`{
	  "chat.instructionsFilesLocations": {
	    ".github/copilot-instructions.md": false,
	    "CLAUDE.md": true
	  }
	}`)
	r := mustResolve(t, tree, "a.go")
	if got := headings(r, "a.go"); len(got) != 1 || !strings.Contains(got[0], "CLAUDE.md") {
		t.Fatalf("boolean-false disables the location, boolean-true keeps it: got %v", got)
	}
}

func TestSettingsReviewSelectionInstructions(t *testing.T) {
	t2 := ft(map[string]string{"guidelines/review.md": "referenced guideline CHECKABLE\n"})
	t2.files[".vscode/settings.json"] = []byte(`{
	  "github.copilot.chat.reviewSelection.instructions": [
	    {"text": "Always check nil derefs CHECKABLE"},
	    {"file": "guidelines/review.md"}
	  ]
	}`)
	r := mustResolve(t, t2, "svc.go")
	got := headings(r, "svc.go")
	if len(got) != 2 {
		t.Fatalf("text and file entries both become sections: got %v", got)
	}
	if !strings.Contains(got[0], ".vscode/settings.json") {
		t.Fatalf("settings entries rank 7 source: got %v", got)
	}
}

func TestTruncationWarningDisclosed(t *testing.T) {
	long := "## Big\n" + strings.Repeat("x", 5000) + "\n"
	tree := ft(map[string]string{"AGENTS.md": long})
	r := mustResolveWithClassifier(t, nil, tree, "a.go")

	// Whole file read: the section text carries every character.
	secs := r.For("a.go")
	if len(secs) != 1 || len(secs[0].Text) < 5000 {
		t.Fatalf("never truncate silently: expected full text, got %d chars", len(secs[0].Text))
	}
	found := false
	for _, w := range r.Warnings() {
		if w.File == "AGENTS.md" && strings.Contains(w.Message, "first 4000 characters") {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation must be disclosed with the cap note: %+v", r.Warnings())
	}

	// Under the limit: no warning.
	small := mustResolve(t, ft(map[string]string{"AGENTS.md": "## S\nshort\n"}), "a.go")
	if len(small.Warnings()) != 0 {
		t.Fatalf("no warning below the cap: %+v", small.Warnings())
	}
}

func TestDoctorReportContent(t *testing.T) {
	tree := ft(map[string]string{
		".github/instructions/go.instructions.md": "---\napplyTo: \"**/*.go\"\n---\n## Keep\nCHECKABLE keep this\n",
		"AGENTS.md": "## Process\nauthoring process talk\n",
	})
	r := mustResolve(t, tree, "main.go")
	report := DoctorReport(r)
	for _, want := range []string{
		"cite doctor",
		"main.go",
		".github/instructions/go.instructions.md",
		"applyTo globs `**/*.go` matched",
		"[reviewable] Keep",
		"[authoring] Process",
		"Using 1 of 1 sections from .github/instructions/go.instructions.md",
		"Using 0 of 1 sections from AGENTS.md. 1 were authoring.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("doctor report missing %q:\n%s", want, report)
		}
	}
}

func TestUsageTypeFromModelPackage(t *testing.T) {
	var u model.InstructionUsage
	u.File = "AGENTS.md"
	u.TotalSections, u.UsedSections, u.AuthoringSections = 41, 6, 35
	if u.UsedSections != 6 {
		t.Fatal("sanity")
	}
}

func mustResolve(t *testing.T, tree Tree, changed ...string) *ResolvedInstructions {
	t.Helper()
	return mustResolveWithClassifier(t, &stubClassifier{}, tree, changed...)
}

func mustResolveWithClassifier(t *testing.T, cls Classifier, tree Tree, changed ...string) *ResolvedInstructions {
	t.Helper()
	r, warns, err := Resolve(tree, changed, cls)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_ = warns
	return r
}
