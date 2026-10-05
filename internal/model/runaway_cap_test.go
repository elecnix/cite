package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// measuredRunawayBody is the shape of a real captured truncated response
// (run 37239996067, archived at /tmp/trunc/capture): the provider stopped on
// its OWN ceiling at 65536 output tokens while Cite had asked for 131072,
// reported no completion_tokens_details at all, returned an empty content
// string, and spent 173631 characters of reasoning getting there.
//
// It is the case that made ErrRunaway unreachable: the old predicate was
// OutputTokens >= MaxOutputTokens, and 65536 >= 131072 is false, so the
// genuine runaway was filed as a terminal capacity overflow and the file was
// lost.
func measuredRunawayBody() string {
	return `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"` +
		strings.Repeat("Hmm. ", 2000) + `"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":12361,"completion_tokens":65536,"total_tokens":77897,` +
		`"prompt_tokens_details":{"cached_tokens":6272}}}`
}

// The runaway must be recognised from what the response contains, not from the
// provider having honoured a cap Cite asked for. This is the measured case:
// reasoning is present, the answer is empty, and the provider stopped below
// the requested cap, so no number Cite chose can be the test.
func TestCompleteRunawayUnderProviderCapIsNotTerminal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	h := &runawayHandler{body: measuredRunawayBody()}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	// The gap the provider created: it answered at 65536, Cite asked for more.
	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 131072})
	if err == nil {
		t.Fatal("an empty answer with a reasoning trace and finish_reason=length must be an error, got nil")
	}
	if errors.Is(err, ErrDeterministic) {
		t.Fatalf("runaway under the provider's own cap misclassified as terminal deterministic: %v", err)
	}
	if !errors.Is(err, ErrRunaway) {
		t.Fatalf("err = %v, want ErrRunaway", err)
	}
}

// A response that says nothing about reasoning and spent less than the cap is
// not evidence of anything: the old full-budget test was the only thing that
// classified it, and that test cannot be trusted against a provider whose
// ceiling Cite does not know. It stays a capacity overflow -- the same
// terminal outcome as before, never a runaway.
func TestCompleteEmptyAnswerWithoutEvidenceStaysOverflow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	body := `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":64}}`
	h := &runawayHandler{body: body}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 131072})
	if !errors.Is(err, ErrDeterministic) {
		t.Fatalf("no reasoning trace and a budget below the cap must stay a capacity overflow, got %v", err)
	}
	if errors.Is(err, ErrRunaway) {
		t.Fatalf("absence of reasoning must not be read as a runaway: %v", err)
	}
}

// A tool call, even one whose arguments the cap cut in half, is the model
// having stopped thinking and started emitting its answer. That is where the
// cap bit; calling it a runaway would discard a real (truncated) answer as if
// no answer existed at all.
func TestCompleteTruncatedToolCallStaysOverflow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	body := `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"` +
		strings.Repeat("thinking ", 40) + `","tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"submit_review","arguments":""}}]},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":65536}}`
	h := &runawayHandler{body: body}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 131072})
	if !errors.Is(err, ErrDeterministic) {
		t.Fatalf("a truncated tool call must stay a capacity overflow, got %v", err)
	}
	if errors.Is(err, ErrRunaway) {
		t.Fatalf("a tool call the cap cut mid-arguments is an answer, not a runaway: %v", err)
	}
}

// The reasoning bytes are in the response and the code must be able to see
// them. Message had no reasoning field, so 173631 characters were invisible
// to everything except the capture file, and the absent
// completion_tokens_details left Usage.ReasoningTokens at 0 -- indistinguishable
// from a response that never thought at all.
//
// The error Cite hands upward is what the reviewer turns into a reason code
// and an operator remedy, so it has to name the reasoning rather than the
// budget: on this response "the whole 131072-token output cap was spent" is
// false, the provider stopped at 65536 and never got there.
func TestRunawayMessageNamesTheReasoningItActuallyHas(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	h := &runawayHandler{body: measuredRunawayBody()}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 131072})
	os.Stderr = old
	w.Close()
	logged, _ := io.ReadAll(r)

	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "reasoning") {
		t.Fatalf("error must name what consumed the generation, got: %q", msg)
	}
	// The provider reported no token split, so the message must not invent
	// one. Saying zero reasoning tokens where the trace is 173 KB is the same
	// lie in the other direction.
	if strings.Contains(msg, "0 ") && strings.Contains(strings.ToLower(msg), "reasoning token") {
		t.Fatalf("message claims a reasoning token count the provider never gave: %q", msg)
	}
	if strings.Contains(msg, "131072-token output cap was spent") {
		t.Fatalf("message blames a cap the provider never reached: %q", msg)
	}
	_ = logged
}

// The stronger form of the same shape: the provider reports no reasoning at
// all AND reports the whole requested budget spent. Withholding only the
// reasoning from the runaway predicate would not help here -- the
// full-budget fallback would still fire -- so the guard has to be on the
// predicate itself. This is the case Cite's reviewer caught on PR #166.
func TestCompleteTruncatedToolCallWithFullBudgetStaysOverflow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	body := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1",` +
		`"type":"function","function":{"name":"submit_review","arguments":""}}]},` +
		`"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":4096}}`
	h := &runawayHandler{body: body}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	if errors.Is(err, ErrRunaway) {
		t.Fatalf("a tool call the cap cut mid-arguments is an answer, not a runaway: %v", err)
	}
	if !errors.Is(err, ErrDeterministic) {
		t.Fatalf("truncated tool call must stay a capacity overflow, got %v", err)
	}
}

// The reasoning has to survive decoding, or the predicate above cannot see it.
func TestCompletionResponseCarriesReasoning(t *testing.T) {
	h := &runawayHandler{body: `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning":"thought about it"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":900}}`}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	resp, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("non-truncated call: %v", err)
	}
	if resp.Reasoning != "thought about it" {
		t.Fatalf("Reasoning = %q, want %q", resp.Reasoning, "thought about it")
	}
}

// The measured provider was deepseek-v4.1-flash, and DeepSeek's own surface
// puts the trace on reasoning_content rather than reasoning. Only the
// OpenAI-compatible name was exercised above, so without this the second wire
// name is decoded code no test ever reaches: drop it and every other test here
// still passes.
//
// The response is also finish_reason=length with the provider stopping well
// below the requested cap, so it proves the trace reaches the predicate from
// this name and not only into the response struct.
func TestCompleteRunawayUnderReasoningContentWireName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	body := `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"` +
		strings.Repeat("Hmm. ", 2000) + `"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":12361,"completion_tokens":65536}}`
	h := &runawayHandler{body: body}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 131072})
	if !errors.Is(err, ErrRunaway) {
		t.Fatalf("reasoning_content must reach the runaway predicate the same way reasoning does, got %v", err)
	}
	if strings.Contains(err.Error(), "131072-token output cap was spent") {
		t.Fatalf("message blames a cap the provider never reached: %v", err)
	}
}

// Both names can arrive on one response. Neither wins by priority: the first
// non-empty does, and a provider that sends an empty reasoning alongside a
// real reasoning_content is not thereby reporting no trace.
func TestCompleteReasoningContentSurvivesAnEmptyReasoningField(t *testing.T) {
	h := &runawayHandler{body: `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning":"","reasoning_content":"deep thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":900}}`}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	resp, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("non-truncated call: %v", err)
	}
	if resp.Reasoning != "deep thought" {
		t.Fatalf("Reasoning = %q, want %q", resp.Reasoning, "deep thought")
	}
}
