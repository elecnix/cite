package reviewer

import (
	"encoding/json"
	"strings"
	"testing"
)

// A model that runs without reasoning writes its JSON in the order the
// prompt's OUTPUT example shows, so a finding's title is its first commitment
// and the body then argues for it. Replayed on a real file at
// reasoning_effort=none, the reviewer read `v != "0" && v != "false" ...` as
// the reverse of what it returns in 4 of 10 runs. With an "analysis" string
// written before "findings", it wrote that claim in none of 20.
//
// These tests hold the field in place: required by the schema ahead of
// findings, and shown ahead of findings in the example the model copies.

func TestReviewSchemaRequiresAnalysisBeforeFindings(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(reviewResponseSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["analysis"].Type != "string" {
		t.Fatalf("schema properties lack an analysis string: %+v", schema.Properties["analysis"])
	}
	a, f := indexOf(schema.Required, "analysis"), indexOf(schema.Required, "findings")
	if a < 0 || f < 0 || a > f {
		t.Fatalf("required = %v, want analysis listed before findings", schema.Required)
	}
}

func TestOutputExampleShowsAnalysisBeforeFindings(t *testing.T) {
	i := strings.Index(embeddedSystemPrompt, "## OUTPUT")
	if i < 0 {
		t.Fatal("the prompt has no ## OUTPUT section")
	}
	out := embeddedSystemPrompt[i:]
	a, f := strings.Index(out, `"analysis":`), strings.Index(out, `"findings":`)
	if a < 0 || f < 0 || a > f {
		t.Fatalf("the OUTPUT example must show \"analysis\" before \"findings\":\n%s", out)
	}
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}
