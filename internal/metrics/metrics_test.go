package metrics

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

func ev(line int, quote string) model.Evidence {
	return model.Evidence{Line: line, Quote: quote}
}

// SpanChanged: a finding whose quoted span survives verbatim (after the one
// documented normaliser) into the head content was NOT actioned.
func TestSpanChangedQuoteIntact(t *testing.T) {
	head := []byte("package main\n\nfunc f() {\n\tel.innerHTML = user.displayName\n}\n")
	fs := []model.Evidence{ev(3, "el.innerHTML = user.displayName")}
	if SpanChanged(fs, head) {
		t.Fatal("quote still present at head: span must count as unchanged")
	}
}

// Whitespace-only differences do not count as actioned — the normaliser is
// applied to both sides (§8's cascade normaliser).
func TestSpanChangedWhitespaceOnly(t *testing.T) {
	head := []byte("func f() {\n\tel.innerHTML   =   user.displayName\n}\n")
	fs := []model.Evidence{ev(2, "el.innerHTML = user.displayName")}
	if SpanChanged(fs, head) {
		t.Fatal("whitespace drift is not an actioned change")
	}
}

// Any evidence quote disappearing from the head content means the anchored
// span changed — that is exactly what "fixed" looks like.
func TestSpanChangedQuoteGone(t *testing.T) {
	head := []byte("func f() {\n\tel.textContent = user.displayName\n}\n")
	fs := []model.Evidence{ev(2, "el.innerHTML = user.displayName")}
	if !SpanChanged(fs, head) {
		t.Fatal("quoted line rewritten: span must count as changed")
	}
}

// A deleted file is the strongest form of "the span is gone".
func TestSpanChangedFileGone(t *testing.T) {
	if !SpanChanged([]model.Evidence{ev(1, "anything")}, nil) {
		t.Fatal("missing head content must report changed")
	}
}

// Multiple evidence quotes: every one must survive for the finding to count
// as unactioned. One surviving quote is not enough.
func TestSpanChangedAllQuotesMustSurvive(t *testing.T) {
	head := []byte("a := req.body.displayName\nb := el.textContent\n")
	fs := []model.Evidence{ev(1, "a := req.body.displayName"), ev(2, "GONE LINE")}
	if !SpanChanged(fs, head) {
		t.Fatal("one vanished quote must mark the whole span changed")
	}
}

// Empty evidence cannot be verified either way; it must never claim action.
func TestSpanChangedEmptyEvidence(t *testing.T) {
	if SpanChanged(nil, []byte("code")) {
		t.Fatal("no evidence ⇒ no actioned verdict")
	}
}

func fi(fp, path string, evidence ...model.Evidence) Finding {
	return Finding{Fingerprint: fp, Path: path, Evidence: evidence}
}

// Evaluate: fixed when the span changed; argued on a >40-char human reply;
// the two overlap in Actioned; Published counts every finding once.
func TestEvaluateDispositions(t *testing.T) {
	findings := []Finding{
		fi("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "a.go", ev(1, "old line")),
		fi("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "b.go", ev(1, "stable line")),
		fi("cccccccccccccccccccccccccccccccc", "c.go", ev(1, "also stable")),
		fi("dddddddddddddddddddddddddddddddd", "d.go", ev(1, "untouched")),
	}
	heads := map[string][]byte{
		"a.go": []byte("new line"),    // fixed
		"b.go": []byte("stable line"), // argued below
		"c.go": []byte("also stable"), // both
		"d.go": []byte("untouched"),
	}
	replies := map[string][]Reply{
		"bbbb…no": nil,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": {
			{Author: "human1", Body: "This is not actually reachable because the caller validates the length before invoking this helper."},
		},
		"cccccccccccccccccccccccccccccccc": {
			{Author: "human2", Body: "Agreed, refactoring this in the next push along with the error path cleanup."},
			{Author: "cite[bot]", Body: "short"},
		},
	}
	rep := Evaluate(findings, heads, replies)

	if rep.Published != 4 {
		t.Fatalf("Published = %d, want 4 (two denominators: published_findings)", rep.Published)
	}
	if rep.Fixed != 1 {
		t.Fatalf("Fixed = %d, want 1", rep.Fixed)
	}
	if rep.Argued != 2 {
		t.Fatalf("Argued = %d, want 2 (>40-char non-bot replies)", rep.Argued)
	}
	if rep.Actioned != 3 {
		t.Fatalf("Actioned = %d, want 3 (fixed ∪ argued)", rep.Actioned)
	}
	want := 3.0 / 4.0
	if rep.Rate() < want-1e-9 || rep.Rate() > want+1e-9 {
		t.Fatalf("Rate = %v, want %v", rep.Rate(), want)
	}
}

// Replies at or under the threshold never count as argument — "ok", "done",
// "+1" cost nothing and must stay outside the metric.
func TestEvaluateShortRepliesIgnored(t *testing.T) {
	findings := []Finding{fi("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "e.go", ev(1, "line"))}
	heads := map[string][]byte{"e.go": []byte("line")}
	replies := map[string][]Reply{
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee": {
			{Author: "h", Body: "ok done thanks"},
		},
	}
	rep := Evaluate(findings, heads, replies)
	if rep.Argued != 0 || rep.Actioned != 0 {
		t.Fatalf("short reply must not action: %+v", rep)
	}
}

// Bot-authored replies are never argument, whatever their length.
func TestEvaluateBotRepliesIgnored(t *testing.T) {
	findings := []Finding{fi("ffffffffffffffffffffffffffffffff", "f.go", ev(1, "line"))}
	heads := map[string][]byte{"f.go": []byte("line")}
	long := "I am the bot and this reply is deliberately longer than forty characters to pass the gate."
	replies := map[string][]Reply{
		"ffffffffffffffffffffffffffffffff": {{Author: "cite-app[bot]", Body: long}},
	}
	rep := Evaluate(findings, heads, replies)
	if rep.Argued != 0 {
		t.Fatalf("bot reply must not count as argued: %+v", rep)
	}
}

// Rate with an empty denominator is zero, never NaN.
func TestRateEmpty(t *testing.T) {
	var r Report
	if r.Rate() != 0 {
		t.Fatal("empty report rate must be 0")
	}
}
