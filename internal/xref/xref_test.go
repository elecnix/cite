package xref

import (
	"strings"
	"testing"
)

func lines(s string) []string { return splitLines([]byte(s)) }

func addedAll(post []string) map[int]bool {
	m := map[int]bool{}
	for i := range post {
		m[i+1] = true
	}
	return m
}

// The shape of a false positive posted on elecnix/cite#138: the changed line
// passes an environment variable to a parser defined in another package, and
// the reviewer, unable to see the parser, guessed that the empty string was
// rejected. The parser's first case accepts it.
func TestRelatedFindsTheDefinitionOfACalledFunction(t *testing.T) {
	snap := MapSnapshot{
		"internal/model/client.go": []byte(`package model

// ParseStructuredOutputMode maps the flag to a mode; empty is the default.
func ParseStructuredOutputMode(s string) (StructuredOutputMode, error) {
	switch s {
	case "", string(StructuredOutputResponseFormat):
		return StructuredOutputResponseFormat, nil
	case string(StructuredOutputTools):
		return StructuredOutputTools, nil
	default:
		return "", fmt.Errorf("structured output mode %q", s)
	}
}

func unrelated() {}
`),
		"cmd/cite/rereview.go": nil,
	}
	post := lines(`package main

func runReReview(args []string) error {
	mode, err := model.ParseStructuredOutputMode(*structuredOutput)
	if err != nil {
		return err
	}
	return nil
}
`)
	ix := NewIndex(snap, Limits{})
	got := ix.Related("cmd/cite/rereview.go", post, map[int]bool{4: true})
	if len(got) != 1 {
		t.Fatalf("want one snippet, got %d: %+v", len(got), got)
	}
	s := got[0]
	if s.Path != "internal/model/client.go" || s.Kind != KindDefinition || s.Symbol != "ParseStructuredOutputMode" {
		t.Fatalf("wrong snippet: %+v", s)
	}
	body := strings.Join(s.Lines, "\n")
	if !strings.Contains(body, `case "", string(StructuredOutputResponseFormat):`) {
		t.Fatalf("snippet lacks the case that settles the claim:\n%s", body)
	}
	if !strings.HasPrefix(s.Lines[0], "// ParseStructuredOutputMode") || s.StartLine != 3 {
		t.Fatalf("snippet should start at the doc comment on line 3, got line %d: %q", s.StartLine, s.Lines[0])
	}
	if strings.Contains(body, "unrelated") {
		t.Fatalf("snippet ran past the end of the function:\n%s", body)
	}
}

// A constant in a grouped const block is a definition too: the other false
// positive on #138 asked whether a default was still 131072.
func TestRelatedFindsAGroupedConstant(t *testing.T) {
	snap := MapSnapshot{
		"internal/config/config.go": []byte("package config\n\nconst (\n\tDefaultReviewMaxOutputTokens = 131072\n\tother = 1\n)\n"),
	}
	post := lines("package main\n\nvar cap = config.DefaultReviewMaxOutputTokens\n")
	got := NewIndex(snap, Limits{}).Related("cmd/x.go", post, map[int]bool{3: true})
	if len(got) != 1 || !strings.Contains(got[0].Lines[0], "131072") {
		t.Fatalf("want the constant's line, got %+v", got)
	}
}

func TestRelatedSkipsNamesTheFileDefines(t *testing.T) {
	snap := MapSnapshot{"b.go": []byte("package p\n\nfunc helper() int { return 2 }\n")}
	post := lines("package p\n\nfunc helper() int { return 1 }\n\nfunc use() int { return helper() }\n")
	got := NewIndex(snap, Limits{}).Related("a.go", post, map[int]bool{5: true})
	for _, s := range got {
		if s.Kind == KindDefinition {
			t.Fatalf("the file defines helper itself; got a definition from %s", s.Path)
		}
	}
}

func TestRelatedSkipsAmbiguousNames(t *testing.T) {
	snap := MapSnapshot{
		"a/x.go": []byte("package a\n\nfunc Parse() {}\n"),
		"b/x.go": []byte("package b\n\nfunc Parse() {}\n"),
		"c/x.go": []byte("package c\n\nfunc Parse() {}\n"),
	}
	post := lines("package d\n\nfunc f() { Parse() }\n")
	if got := NewIndex(snap, Limits{}).Related("d/y.go", post, map[int]bool{3: true}); len(got) != 0 {
		t.Fatalf("three definitions of Parse is ambiguous; want none, got %+v", got)
	}
}

func TestRelatedPrefersTheSamePackage(t *testing.T) {
	snap := MapSnapshot{
		"other/x.go": []byte("package other\n\nfunc Decode() {}\n"),
		"pkg/x.go":   []byte("package pkg\n\nfunc Decode() {}\n"),
	}
	post := lines("package pkg\n\nfunc f() { Decode() }\n")
	got := NewIndex(snap, Limits{MaxDefinitions: 2}).Related("pkg/y.go", post, map[int]bool{3: true})
	if len(got) != 2 || got[0].Path != "pkg/x.go" {
		t.Fatalf("want pkg/x.go first, got %+v", got)
	}
}

// A changed function's callers are what an api-contract-break claim is about.
func TestRelatedFindsCallersOfAChangedFunction(t *testing.T) {
	snap := MapSnapshot{
		"caller.go": []byte("package p\n\nfunc use() {\n\tv := Compute(1, 2)\n\t_ = v\n}\n"),
	}
	post := lines("package p\n\nfunc Compute(a, b, c int) int {\n\treturn a + b + c\n}\n")
	got := NewIndex(snap, Limits{}).Related("compute.go", post, map[int]bool{3: true})
	if len(got) != 1 || got[0].Kind != KindCaller || got[0].Path != "caller.go" {
		t.Fatalf("want the one call site, got %+v", got)
	}
	if !strings.Contains(strings.Join(got[0].Lines, "\n"), "Compute(1, 2)") {
		t.Fatalf("caller window lacks the call: %+v", got[0])
	}
}

func TestRelatedPythonUsesIndentation(t *testing.T) {
	snap := MapSnapshot{
		"lib/util.py": []byte("import os\n\n\ndef normalize(path):\n    if not path:\n        return '.'\n    return os.path.normpath(path)\n\n\ndef other():\n    pass\n"),
	}
	post := lines("from lib.util import normalize\n\nx = normalize(arg)\n")
	got := NewIndex(snap, Limits{}).Related("app.py", post, map[int]bool{3: true})
	if len(got) != 1 {
		t.Fatalf("want one snippet, got %+v", got)
	}
	body := strings.Join(got[0].Lines, "\n")
	if !strings.Contains(body, "return os.path.normpath(path)") || strings.Contains(body, "def other") {
		t.Fatalf("python block cut wrong:\n%s", body)
	}
}

func TestRelatedTypeScript(t *testing.T) {
	snap := MapSnapshot{
		"src/auth.ts": []byte("export async function verifyToken(token: string): Promise<boolean> {\n  if (!token) {\n    return false;\n  }\n  return check(token);\n}\n"),
	}
	post := lines("const ok = await verifyToken(req.headers.auth);\n")
	got := NewIndex(snap, Limits{}).Related("src/handler.ts", post, map[int]bool{1: true})
	if len(got) != 1 || len(got[0].Lines) != 6 {
		t.Fatalf("want the whole six-line function, got %+v", got)
	}
}

func TestRelatedRespectsTheLineBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for _, n := range []string{"Alpha", "Bravo", "Charlie", "Delta"} {
		b.WriteString("func " + n + "() {\n")
		for i := 0; i < 10; i++ {
			b.WriteString("\tx := 1\n")
		}
		b.WriteString("}\n\n")
	}
	snap := MapSnapshot{"lib.go": []byte(b.String())}
	post := lines("package p\n\nfunc f() { Alpha(); Bravo(); Charlie(); Delta() }\n")
	got := NewIndex(snap, Limits{MaxTotalLines: 30}).Related("f.go", post, map[int]bool{3: true})
	total := 0
	for _, s := range got {
		total += len(s.Lines)
	}
	if total > 30 || len(got) != 2 {
		t.Fatalf("want two twelve-line snippets inside a 30-line budget, got %d snippets, %d lines", len(got), total)
	}
}

func TestIndexLeavesOutVendoredAndGeneratedFiles(t *testing.T) {
	snap := MapSnapshot{
		"vendor/x/y.go":     []byte("package y\n\nfunc Target() {}\n"),
		"api/types.pb.go":   []byte("package api\n\nfunc Target() {}\n"),
		"node_modules/a.js": []byte("function Target() {}\n"),
	}
	post := lines("package p\n\nfunc f() { Target() }\n")
	if got := NewIndex(snap, Limits{}).Related("p.go", post, map[int]bool{3: true}); len(got) != 0 {
		t.Fatalf("vendored and generated definitions must not be quoted, got %+v", got)
	}
}

func TestRelatedIgnoresCommentedCalls(t *testing.T) {
	snap := MapSnapshot{"lib.go": []byte("package p\n\nfunc Hidden() {}\n")}
	post := lines("package p\n\n// Hidden() is not called here\nvar x = 1\n")
	if got := NewIndex(snap, Limits{}).Related("p.go", post, map[int]bool{3: true}); len(got) != 0 {
		t.Fatalf("a name in a comment is not a use, got %+v", got)
	}
}
