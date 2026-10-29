package prcontext

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/model"
)

type fakeClient struct {
	text string
	err  error
	reqs []model.CompletionRequest
}

func (f *fakeClient) Complete(_ context.Context, req model.CompletionRequest) (*model.CompletionResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	return &model.CompletionResponse{Text: f.text}, nil
}

func (f *fakeClient) ModelID() string { return "fake" }

func TestParseComplete(t *testing.T) {
	res, err := Parse("VERDICT: COMPLETE\nRATIONALE: all stated.\nCLARIFYING_QUESTIONS: none\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != Complete || len(res.Questions) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestParseMissingContextCollectsBulletsAndContinuations(t *testing.T) {
	text := "VERDICT: MISSING_CONTEXT\nRATIONALE: gaps.\nCLARIFYING_QUESTIONS:\n" +
		"- Why does the body say \"only\" for retries?\n" +
		"* How long was the crash live?\n" +
		"  and which callers hit it?\n\n" +
		"- What stays untouched?\n"
	res, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Why does the body say "only" for retries?`,
		"How long was the crash live?",
		"and which callers hit it?",
		"What stays untouched?",
	}
	if res.Verdict != MissingContext || strings.Join(res.Questions, "|") != strings.Join(want, "|") {
		t.Fatalf("res = %+v", res)
	}
}

// The classifier reads a reply with no verdict as COMPLETE. Cite cannot: a
// silent COMPLETE would minimize the author's open questions on a reply that
// said nothing, so the reply is rejected and the run records context_error.
func TestParseRejectsWhatItCannotRead(t *testing.T) {
	for name, text := range map[string]string{
		"empty":                         "",
		"prose only":                    "The body looks fine to me.",
		"verdict without a label":       "VERDICT: maybe\nCLARIFYING_QUESTIONS: none",
		"missing context, no questions": "VERDICT: MISSING_CONTEXT\nRATIONALE: gaps.\nCLARIFYING_QUESTIONS: none\n",
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s: want a parse error", name)
		}
	}
}

// The request reproduces the classifier's: its system prompt, its user
// message, temperature 0, and no other field. No max_tokens (MaxOutputTokens
// 0), no reasoning_effort, no response schema, no session header.
func TestClassifySendsTheClassifiersRequest(t *testing.T) {
	fc := &fakeClient{text: "VERDICT: COMPLETE\nRATIONALE: ok\nCLARIFYING_QUESTIONS: none"}
	res, err := Classify(context.Background(), fc, "  Fixes the crash.\n\nThe cause is X.  \n", Options{Timeout: time.Second})
	if err != nil || res.Verdict != Complete {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(fc.reqs) != 1 {
		t.Fatalf("calls = %d", len(fc.reqs))
	}
	req := fc.reqs[0]
	wantUser := UserQuestion + "\n\nPR BODY:\nFixes the crash.\n\nThe cause is X."
	if req.User != wantUser {
		t.Errorf("user message = %q", req.User)
	}
	if req.System != SystemPrompt() || req.System == "" {
		t.Errorf("system prompt is not the vendored classifier prompt")
	}
	if req.Temperature != 0 || req.MaxOutputTokens != 0 || req.ReasoningEffort != "" ||
		len(req.ResponseSchema) != 0 || len(req.Tools) != 0 || req.Seed != nil ||
		req.SessionID != "" || req.RequireParameters {
		t.Errorf("request carries a field the classifier does not send: %+v", req)
	}
}

func TestClassifyPassesRequireParametersThrough(t *testing.T) {
	fc := &fakeClient{text: "VERDICT: COMPLETE\nRATIONALE: ok\nCLARIFYING_QUESTIONS: none"}
	if _, err := Classify(context.Background(), fc, "body", Options{RequireParameters: true}); err != nil {
		t.Fatal(err)
	}
	if !fc.reqs[0].RequireParameters {
		t.Error("the repository's require_parameters must reach the context call")
	}
}

func TestClassifyEmptyBodyAsksOneFixedQuestionWithoutACall(t *testing.T) {
	for _, body := range []string{"", "  \n\t\n"} {
		fc := &fakeClient{err: errors.New("must not be called")}
		res, err := Classify(context.Background(), fc, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdict != MissingContext || len(res.Questions) != 1 || res.Questions[0] != EmptyBodyQuestion {
			t.Fatalf("res = %+v", res)
		}
		if len(fc.reqs) != 0 {
			t.Fatalf("an empty body must not reach the model, got %d call(s)", len(fc.reqs))
		}
	}
}

func TestClassifyReportsProviderAndParseFailures(t *testing.T) {
	if _, err := Classify(context.Background(), &fakeClient{err: errors.New("boom")}, "body", Options{}); err == nil {
		t.Error("a provider error must surface")
	}
	if _, err := Classify(context.Background(), &fakeClient{text: "no verdict here"}, "body", Options{}); err == nil {
		t.Error("an unreadable reply must surface")
	}
}

// The vendored prompt is the one the classifier shipped after its calibration
// block came out. A copy that still carries the block restores an
// eval-specific rule the classifier removed.
func TestVendoredPromptIsTheCalibrationFreeClassifierPrompt(t *testing.T) {
	p := SystemPrompt()
	if strings.Contains(p, "Special calibration") {
		t.Error("the vendored prompt still carries the special-calibration block")
	}
	for _, want := range []string{"strict pull-request body context reviewer", "VERDICT: COMPLETE | MISSING_CONTEXT", "CLARIFYING_QUESTIONS:", "Question construction rule"} {
		if !strings.Contains(p, want) {
			t.Errorf("vendored prompt lacks %q", want)
		}
	}
	if !strings.HasPrefix(UserQuestion, "Does this pull-request body give a reader complete context") {
		t.Errorf("UserQuestion = %q", UserQuestion)
	}
}
