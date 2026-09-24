package reviewer

import (
	"os"
	"strings"
	"testing"
)

// The reviewer's instructions live in three places at once, and only one of
// them is the file a reader edits:
//
//   - prompts/review.system.md is the versioned source of truth;
//   - internal/reviewer/prompts/review.system.md is the copy go:embed can
//     reach, and the one the reviewer actually sends;
//   - PLAN.md Appendix A carries the same text inside a ```“text fence, so
//     the prompt can be read next to the design it implements.
//
// Nothing but these tests keeps the three together, and a drift between them
// changes what the model reads while every other check stays green: the
// embedded copy is the behaviour, the root copy is the diff, and the appendix
// is the documentation. A prose rewrite that lands in one and misses another
// is exactly the failure this guards.
func TestPromptCopiesStayInSync(t *testing.T) {
	root := readFile(t, "../../prompts/review.system.md")
	embedded := readFile(t, "prompts/review.system.md")

	if root != embedded {
		t.Fatalf(
			"prompts/review.system.md (%d bytes) and internal/reviewer/prompts/review.system.md (%d bytes) differ: the reviewer embeds the second, so the first is what a reader edits",
			len(root), len(embedded))
	}
	if root != embeddedSystemPrompt {
		t.Fatal("the embedded prompt does not match the file on disk")
	}
}

func TestPromptMatchesPlanAppendixA(t *testing.T) {
	appendix := appendixA(t, readFile(t, "../../PLAN.md"))
	root := readFile(t, "../../prompts/review.system.md")

	if appendix != root {
		t.Fatalf(
			"PLAN.md Appendix A (%d bytes) and prompts/review.system.md (%d bytes) differ: re-sync the appendix so the copy in the design document matches the prompt",
			len(appendix), len(root))
	}
}

// appendixA returns the body of the four-backtick `text` fence that holds the
// prompt. Four backticks, not three, so the prompt's own fences survive inside
// it.
func appendixA(t *testing.T, plan string) string {
	t.Helper()

	const open = "````text\n"
	start := strings.Index(plan, open)
	if start < 0 {
		t.Fatal("PLAN.md carries no ````text fence for Appendix A")
	}
	start += len(open)

	end := strings.Index(plan[start:], "\n````\n")
	if end < 0 {
		t.Fatal("PLAN.md's ````text fence is never closed")
	}
	return plan[start : start+end+1]
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
