package reviewer

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// Shapes posted across the user's repositories as comments: a finding that
// says the code is fine reaches a reader at any confidence, so it is dropped
// at any confidence. The impact and title must open (or the title close)
// with the disclaimer; a real impact that happens to start with "None of"
// stays.
func TestSelfNegationAtEveryConfidence(t *testing.T) {
	cases := []struct {
		name, title, impact string
		drop                bool
	}{
		{"impact None at question", "Thread header fields are not sanitised", "None.", true},
		{"impact None em dash", "Order of checks", "None — both orders reach the same result.", true},
		{"impact no runtime", "Comment overstates the guard", "No runtime impact; the comment is stale.", true},
		{"bold impact", "Guard ordering", "**No impact.** The check runs first either way.", true},
		{"title no defect", "No defect in new-line rule", "The rule fires once per line.", true},
		{"title is correct", "Loopback rewrite is correct", "Every occurrence is rewritten.", true},
		{"none of is an impact", "Retries never run", "None of the three retries runs after the first 429.", false},
		{"correct mid-title is a claim", "Correct token dropped from the child env", "The child loses its credential and every call fails.", false},
		{"ordinary impact", "Index panic on short input", "An input shorter than four bytes panics the handler.", false},
	}
	for _, conf := range []string{"question", "likely"} {
		for _, tc := range cases {
			t.Run(conf+"/"+tc.name, func(t *testing.T) {
				f := &model.Finding{Title: tc.title, Impact: tc.impact, Confidence: model.Confidence(conf)}
				got := selfNegation(f) != ""
				if got != tc.drop {
					t.Fatalf("selfNegation(%q, %q) dropped=%v, want %v", tc.title, tc.impact, got, tc.drop)
				}
			})
		}
	}
}
