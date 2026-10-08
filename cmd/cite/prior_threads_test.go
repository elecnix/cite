package main

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/githubclient"
)

// Issue #168: Cite threads become per-file prior threads for the next review
// call, with the human replies under them.
func TestPriorThreadsFromGitHub(t *testing.T) {
	fp := strings.Repeat("ab", 16)
	threads := []githubclient.Thread{
		{IsResolved: true, Comments: []githubclient.ThreadComment{
			{ID: 11, Path: "a.go", StartLine: 3, Line: 5, AuthorLogin: "github-actions[bot]", Body: citeBody(fp, "a.go", "x := 1")},
			{ID: 12, Path: "a.go", AuthorLogin: "maintainer", Body: "False positive, see TestX."},
			{ID: 13, Path: "a.go", AuthorLogin: "github-actions[bot]", Body: "bot chatter"},
		}},
		{Comments: []githubclient.ThreadComment{
			{ID: 21, Path: "b.go", Line: 9, AuthorLogin: "github-actions[bot]", Body: citeBody(fp, "b.go", "y")},
		}},
		{Comments: []githubclient.ThreadComment{
			{ID: 31, Path: "a.go", Line: 1, AuthorLogin: "human", Body: "a human review comment, not Cite's"},
		}},
	}

	got := priorThreadsFrom(threads)

	a := got["a.go"]
	if len(a) != 1 {
		t.Fatalf("a.go prior threads = %+v, want only the Cite thread", a)
	}
	p := a[0]
	if p.ID != 11 || p.Category != "crash" || p.Title != "t" || p.StartLine != 3 || p.EndLine != 5 || !p.Resolved {
		t.Fatalf("a.go thread = %+v", p)
	}
	if len(p.Replies) != 1 || p.Replies[0].Author != "maintainer" || p.Replies[0].Body != "False positive, see TestX." {
		t.Fatalf("replies = %+v, want the one human reply", p.Replies)
	}
	b := got["b.go"]
	if len(b) != 1 || b[0].StartLine != 9 || b[0].EndLine != 9 || len(b[0].Replies) != 0 {
		t.Fatalf("b.go = %+v, want one single-line thread with no replies", b)
	}
}

// A reply is untrusted text in every review call of the file; one long reply
// must not take over the prompt.
func TestPriorThreadsFromCapsReplyLength(t *testing.T) {
	fp := strings.Repeat("cd", 16)
	long := strings.Repeat("é", priorReplyMaxRunes+50)
	got := priorThreadsFrom([]githubclient.Thread{{Comments: []githubclient.ThreadComment{
		{ID: 1, Path: "a.go", Line: 1, AuthorLogin: "github-actions[bot]", Body: citeBody(fp, "a.go", "q")},
		{ID: 2, Path: "a.go", AuthorLogin: "maintainer", Body: long},
	}}})
	body := got["a.go"][0].Replies[0].Body
	if n := len([]rune(body)); n > priorReplyMaxRunes+len([]rune(priorReplyTruncated)) {
		t.Fatalf("reply kept %d runes, cap is %d", n, priorReplyMaxRunes)
	}
	if !strings.HasSuffix(body, priorReplyTruncated) {
		t.Fatalf("truncated reply does not say so: ...%q", body[len(body)-20:])
	}
}
