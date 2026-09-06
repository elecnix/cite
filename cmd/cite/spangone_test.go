package main

// Tests for spanGoneFor: the predicate that decides whether a live thread's
// quoted evidence is verified gone from the new file content (§10). A false
// "gone" clears a real finding; a false "present" leaves an outdated thread
// open forever. The original implementation had the second failure mode: it
// treated any file line that was a SUBSTRING of the quote as evidence still
// present, so short lines ("fi", "it", "cmd") matched nearly every quote and
// no thread was ever resolved in practice.

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

// spanGoneFor ignores its thread argument (the evidence data carries
// everything), so a zero thread is a valid stub.
var liveStub = publisher.LiveThread{}

func spanGone(t *testing.T, evidence []model.Evidence, content string) bool {
	t.Helper()
	data := &threadFinding{Path: "f.txt", Evidence: evidence}
	return spanGoneFor(data, map[string][]byte{"f.txt": []byte(content)})(liveStub)
}

func TestSpanGoneQuoteRemovedResolves(t *testing.T) {
	content := "package main\n\nfunc main() {\n\twriteQueue(allPending)\n}\n"
	gone := spanGone(t, []model.Evidence{{Line: 3, Quote: "write_queue(read_queue())"}}, content)
	if !gone {
		t.Fatal("the quoted line is gone from the file; the span must count as gone")
	}
}

func TestSpanGoneQuoteMovedStillPresent(t *testing.T) {
	// The quote survived, just at a different line: the span is present.
	content := "x\nwrite_queue(read_queue())\ny\n"
	gone := spanGone(t, []model.Evidence{{Line: 3, Quote: "write_queue(read_queue())"}}, content)
	if gone {
		t.Fatal("a quote that moved lines is still present; must not count as gone")
	}
}

func TestSpanGoneShortFileLineIsNotEvidence(t *testing.T) {
	// The old bug: a short file line ("fi", "it", "cmd") that happens to be
	// a substring of the quote marked the span present. It says nothing
	// about the span; only forward containment counts.
	content := "fi\nit\ncmd\ndone\nesac\n"
	gone := spanGone(t, []model.Evidence{{Line: 1, Quote: "setsid sleep infinity < /dev/null > \"$d/in.fifo\" 2>> \"$d/err.log\" &"}}, content)
	if !gone {
		t.Fatal("short lines inside the quote must not keep the thread open")
	}
}

func TestSpanGoneChurnResistant(t *testing.T) {
	// Reformat churn: punctuation, case and whitespace changes must not
	// evade the check — the normalized quote still appears.
	content := "SET -EUO PIPEFAIL;\nother()\n"
	gone := spanGone(t, []model.Evidence{{Line: 1, Quote: "set -euo pipefail"}}, content)
	if gone {
		t.Fatal("punctuation/case churn must not hide a still-present span")
	}
}

func TestSpanGoneMultipleEvidenceAnyPresentKeepsOpen(t *testing.T) {
	// One of two quotes still in the file: fail toward keeping the thread.
	content := "first gone now\nbut b is here\n"
	gone := spanGone(t, []model.Evidence{
		{Line: 1, Quote: "aaaa bbbb cccc"},
		{Line: 2, Quote: "b is here"},
	}, content)
	if gone {
		t.Fatal("one surviving quote must keep the thread open")
	}
}

func TestSpanGoneFileDeleted(t *testing.T) {
	data := &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "something"}}}
	gone := spanGoneFor(data, map[string][]byte{})(liveStub)
	if !gone {
		t.Fatal("the whole file is gone; the span is gone")
	}
}

func TestSpanGoneUnverifiableData(t *testing.T) {
	// No parsed evidence: never resolve (fail toward keeping the thread).
	data := &threadFinding{Path: "f.txt"}
	gone := spanGoneFor(data, map[string][]byte{"f.txt": []byte("x")})(liveStub)
	if gone {
		t.Fatal("no evidence, no verification, no resolution")
	}
	// Empty normalized quote: never resolve.
	gone = spanGone(t, []model.Evidence{{Line: 1, Quote: "***"}}, "anything")
	if gone {
		t.Fatal("an empty normalized quote is unverifiable, never gone")
	}
}

func TestResolutionReplyStatesItsBasis(t *testing.T) {
	// Span verified gone: the reply says so.
	data := map[int64]*threadFinding{1: {Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "old code"}}}}
	post := map[string][]byte{"f.txt": []byte("new code entirely\n")}
	if r := resolutionReply(data, 1, post, "abcdef1234567"); !strings.Contains(r, "verified gone") {
		t.Fatalf("span-gone resolution must state the verified basis, got %q", r)
	}
	// Span still present: the reply names the re-review that adjudicated it.
	post = map[string][]byte{"f.txt": []byte("still has old code here\n")}
	if r := resolutionReply(data, 1, post, "abcdef1234567"); !strings.Contains(r, "re-reviewed at abcdef1") {
		t.Fatalf("re-reviewed resolution must name the head SHA, got %q", r)
	}
}
