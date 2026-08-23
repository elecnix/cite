package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Models emit raw control characters inside JSON string literals — most
// often a literal tab, because the review envelope line-numbers code with
// "NNNN +|<TAB>code" and the evidence quote copies it. Strict encoding/json
// rejects any raw control char in a string, so one unescaped tab costs the
// whole finding (and, per §7's deterministic-failure rule, every retry
// identically). The parse path escapes them before decoding; the semantic
// content is unchanged.

func TestEscapeRawControlCharsInStrings(t *testing.T) {
	in := []byte(`{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[` +
		`{"id":"f1","category":"crash","anchor":{"start_line":2,"end_line":2},` +
		`"title":"t","body":"b","impact":"i",` +
		`"evidence":[{"line":2,"quote":"var debug\t= true"}],` +
		`"external_claims":[],"introduced_by":"added_line",` +
		`"confidence":"certain","fix":null}]}`)

	got, err := ParseFileReview(in)
	if err != nil {
		t.Fatalf("raw tab in evidence quote must parse: %v", err)
	}
	want := "var debug\t= true"
	if got.Findings[0].Evidence[0].Quote != want {
		t.Errorf("quote = %q, want %q", got.Findings[0].Evidence[0].Quote, want)
	}
}

func TestEscapeRawControlCharsPreservesValidEscapes(t *testing.T) {
	// An already-escaped \t must survive exactly as-is — no double-escaping.
	in := []byte(`{"quote": "a\tb", "plain": "c\\d"})
	}`)
	out := escapeRawControlCharsInStrings(in)
	if !strings.Contains(string(out), `a\tb`) {
		t.Errorf("valid escape rewritten: %q", out)
	}
	if !strings.Contains(string(out), `c\\d`) {
		t.Errorf("escaped backslash altered: %q", out)
	}
}

func TestEscapeRawControlCharsOutsideStringsUntouched(t *testing.T) {
	// Whitespace between tokens is structural JSON and must be preserved.
	in := []byte("{\n\t\"a\": 1,\n\t\"b\": [1, 2]\n}")
	out := string(escapeRawControlCharsInStrings(in))
	if !strings.Contains(out, "\n\t\"a\"") {
		t.Errorf("structural whitespace mangled: %q", out)
	}
}

func TestEscapeRawControlCharsNewlineAndCarriageReturn(t *testing.T) {
	in := []byte("{\"s\": \"line1\nline2\rx\"}")
	var probe map[string]any
	if err := jsonUnmarshalForTest(escapeRawControlCharsInStrings(in), &probe); err != nil {
		t.Fatalf("escaped newline/CR must decode: %v", err)
	}
}

func jsonUnmarshalForTest(data []byte, v any) error {
	return jsonDecode(data, v)
}

func jsonDecode(data []byte, v any) error { return json.Unmarshal(data, v) }
