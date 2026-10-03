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

// A thread that carries a fingerprint but no evidence quotes has nothing to
// verify. Whatever the post-image looks like — file present or file absent —
// the predicate must answer false. Reading the file-absence rule first makes
// an empty finding resolve itself on the strength of nothing at all, which is
// the one direction this predicate is not allowed to fail: it clears a human's
// thread.
func TestSpanGoneNoEvidenceKeepsThreadWhateverTheFileDoes(t *testing.T) {
	for name, post := range map[string]map[string][]byte{
		"file absent":      {},
		"file present":     {"f.txt": []byte("anything at all\n")},
		"other files only": {"other.txt": []byte("x\n")},
	} {
		for _, evidence := range [][]model.Evidence{nil, {}} {
			data := &threadFinding{Path: "f.txt", Evidence: evidence}
			if spanGoneFor(data, post)(liveStub) {
				t.Fatalf("%s: a finding with no evidence must never resolve, got gone", name)
			}
		}
	}
	// The same shape reached through the live predicate over a nil finding.
	if spanGoneFor(nil, map[string][]byte{})(liveStub) {
		t.Fatal("no parsed data at all must never resolve")
	}
}

// One test per branch of the predicate, with the answer main gave for each.
// The refactor that introduced internal/scope moved the empty-evidence guard
// behind the file-absence lookup; this table is the shape that bug hid in,
// so every edge is written out rather than left implicit.
func TestSpanGoneBranchTable(t *testing.T) {
	gone, present := []model.Evidence{{Line: 1, Quote: "old line"}}, []model.Evidence{{Line: 1, Quote: "still line"}}
	for _, c := range []struct {
		name     string
		data     *threadFinding
		post     map[string][]byte
		wantGone bool
	}{
		{"no data at all", nil, map[string][]byte{}, false},
		{"no evidence, file absent", &threadFinding{Path: "f.txt"}, map[string][]byte{}, false},
		{"no evidence, file present", &threadFinding{Path: "f.txt"}, map[string][]byte{"f.txt": []byte("x")}, false},
		{"empty evidence slice, file absent", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{}}, map[string][]byte{}, false},
		{"evidence, file absent", &threadFinding{Path: "f.txt", Evidence: gone}, map[string][]byte{}, true},
		{"evidence, file present, no quote survives", &threadFinding{Path: "f.txt", Evidence: gone}, map[string][]byte{"f.txt": []byte("new line\n")}, true},
		{"evidence, file present, quote survives", &threadFinding{Path: "f.txt", Evidence: present}, map[string][]byte{"f.txt": []byte("still line\n")}, false},
		{"unverifiable quote, file present", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "***"}}}, map[string][]byte{"f.txt": []byte("new line\n")}, false},
		{"unverifiable quote, other quote survives", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "***"}, {Line: 2, Quote: "still line"}}}, map[string][]byte{"f.txt": []byte("still line\n")}, false},
		{"two quotes, one survives", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "old line"}, {Line: 2, Quote: "still line"}}}, map[string][]byte{"f.txt": []byte("still line\n")}, false},
		{"two quotes, none survives", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "old line"}, {Line: 2, Quote: "older line"}}}, map[string][]byte{"f.txt": []byte("new line\n")}, true},
		{"empty file present", &threadFinding{Path: "f.txt", Evidence: gone}, map[string][]byte{"f.txt": {}}, true},
		{"multi-line quote survives", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "if !escaped {\n\tel.innerHTML"}}}, map[string][]byte{"f.txt": []byte("if !escaped {\n\tel.innerHTML = user.displayName\n")}, false},
		{"multi-line quote gone", &threadFinding{Path: "f.txt", Evidence: []model.Evidence{{Line: 1, Quote: "if !escaped {\n\tel.innerHTML"}}}, map[string][]byte{"f.txt": []byte("el.textContent = user.displayName\n")}, true},
	} {
		if got := spanGoneFor(c.data, c.post)(liveStub); got != c.wantGone {
			t.Errorf("%s: got gone=%v, want gone=%v", c.name, got, c.wantGone)
		}
	}
}

func TestSpanGoneMultiLineQuoteStillPresent(t *testing.T) {
	// A multi-line quote survives: the check compares the quote against the
	// whole normalised content, so the newlines inside the quote collapse to
	// spaces exactly as the newlines inside the file do. The fix_or_argue
	// instrument reads the same owner (scope.EvidenceGone's sibling), so the
	// predicate that resolves a thread and the predicate behind the metric
	// cannot disagree about whether a multi-line span survived.
	content := "package main\n\nfunc f() {\n\tif !escaped {\n\t\tel.innerHTML = user.displayName\n\t}\n}\n"
	gone := spanGone(t, []model.Evidence{{Line: 4, Quote: "if !escaped {\n\t\tel.innerHTML = user.displayName"}}, content)
	if gone {
		t.Fatal("a two-line quote present verbatim is still present")
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

func TestResolutionReplyMissingEvidenceDoesNotPanic(t *testing.T) {
	// A thread in ThreadsToResolve without a parsed evidence entry (missing
	// key, nil entry, nil map) must fall back to the re-review basis, not
	// dereference nil.
	post := map[string][]byte{"f.txt": []byte("anything\n")}
	for name, td := range map[string]map[int64]*threadFinding{
		"nil map":      nil,
		"missing key":  {},
		"nil entry":    {1: nil},
		"no evidence":  {1: {Path: "f.txt"}},
		"empty quotes": {1: {Path: "f.txt", Evidence: []model.Evidence{}}},
	} {
		r := resolutionReply(td, 1, post, "abcdef1234567")
		if !strings.Contains(r, "re-reviewed at abcdef1") {
			t.Fatalf("%s: expected re-review basis fallback, got %q", name, r)
		}
	}
}
