package main

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// The footer says what each confidence means. "likely" used to read "this is
// a question, not an assertion", which contradicted every likely finding's
// own assertive wording.
func TestRenderCommentConfidenceFooter(t *testing.T) {
	for conf, want := range map[model.Confidence]string{
		model.ConfidenceLikely:   "Confidence: likely — one step rests on code outside this file",
		model.ConfidenceQuestion: "Confidence: question — this is a question, not an assertion.",
	} {
		body := renderComment(model.ValidatedFinding{Finding: model.Finding{Title: "t", Confidence: conf}, Path: "a.go"})
		if !strings.Contains(body, want) {
			t.Errorf("%s footer missing %q:\n%s", conf, want, body)
		}
		if conf == model.ConfidenceLikely && strings.Contains(body, "this is a question") {
			t.Errorf("likely finding still calls itself a question:\n%s", body)
		}
	}
	certain := renderComment(model.ValidatedFinding{Finding: model.Finding{Title: "t", Confidence: model.ConfidenceCertain}, Path: "a.go"})
	if strings.Contains(certain, "Confidence:") {
		t.Errorf("certain finding carries a confidence footer:\n%s", certain)
	}
}
