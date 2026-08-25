package model

import (
	"errors"
	"testing"
)

// A response that fails strict JSON decoding is a mechanical formatting
// artifact (single-quoted keys, truncated output), not a confident wrong
// answer. It must be classified as a syntax error so the reviewer can retry
// it from the run-global bucket, the same way it retries blank bodies.
func TestParseFileReviewClassifiesSyntaxErrors(t *testing.T) {
	for name, in := range map[string]string{
		"single-quoted keys": `{'path':'a.go','outcome':'reviewed'}`,
		"truncated JSON":     `{"schema_version":1,"path":"a.go","outcome":"rev`,
		"backticked garbage": "```json\n{'path': 'a.go'}\n```", // fence + invalid body
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFileReview([]byte(in))
			if err == nil {
				t.Fatalf("ParseFileReview(%q): expected error", in)
			}
			if !errors.Is(err, ErrSyntax) {
				t.Fatalf("ParseFileReview(%q) = %v, want ErrSyntax", in, err)
			}
		})
	}
}

// A well-formed JSON document that violates the schema semantically (unknown
// outcome, banned claim type, …) must NOT be classified as a syntax error:
// re-asking would invite a matching quote for the same wrong claim (§8).
func TestParseFileReviewSemanticViolationIsNotSyntax(t *testing.T) {
	in := `{"schema_version":1,"path":"a.go","outcome":"nope","findings":[]}`
	_, err := ParseFileReview([]byte(in))
	if err == nil {
		t.Fatal("expected error for unknown outcome")
	}
	if errors.Is(err, ErrSyntax) {
		t.Fatalf("semantic violation classified as ErrSyntax: %v", err)
	}
}
