package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// runawayHandler serves a single chat/completions response. The caller
// records what the request asked for so the test can assert on the budget.
type runawayHandler struct {
	body      string
	lastMaxTk int
	calls     int
}

func (h *runawayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls++
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		MaxTokens int `json:"max_tokens"`
	}
	_ = json.Unmarshal(raw, &req)
	h.lastMaxTk = req.MaxTokens
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(h.body))
}

// runawayBody is the measured shape of a runaway generation: the provider
// spent the ENTIRE requested output budget and returned no answer at all.
// Reproduced against deepseek/deepseek-v4-flash-0731 at cap 12288 and 24576 —
// reasoning_tokens == completion_tokens == max_tokens, content null,
// finish_reason=length.
func runawayBody(maxTokens int) string {
	return `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"` +
		strings.Repeat("thinking ", 40) + `"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":` + itoa(maxTokens) + `,` +
		`"completion_tokens_details":{"reasoning_tokens":` + itoa(maxTokens) + `}}}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// A response that spent the whole budget and produced no answer is a runaway
// generation, not a capacity overflow. It must not be reported as the terminal
// deterministic failure, because the premise behind that class -- "a truncated
// response truncates identically on retry" -- is false for this shape.
func TestCompleteRunawayGenerationIsNotTerminalDeterministic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	h := &runawayHandler{body: runawayBody(4096)}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	if err == nil {
		t.Fatal("a runaway generation must be an error, got nil")
	}
	if errors.Is(err, ErrDeterministic) {
		t.Fatalf("runaway generation misclassified as terminal deterministic: %v", err)
	}
	if !errors.Is(err, ErrRunaway) {
		t.Fatalf("err = %v, want ErrRunaway", err)
	}
}

// The operator-facing message must not send the operator down the one path
// that provably does not help. Measured: raising the cap 12288 -> 24576 grew
// the runaway from 96 KB to 186 KB, both with reasoning_tokens == cap and no
// answer. The message must also say the capture is a reasoning trace, because
// the captured bytes are what made this look like runaway *output*.
func TestRunawayMessageSteersOperatorAwayFromTheCap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	h := &runawayHandler{body: runawayBody(4096)}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	os.Stderr = old
	w.Close()
	logged, _ := io.ReadAll(r)

	if err == nil {
		t.Fatal("expected an error")
	}
	blob := err.Error() + "\n" + string(logged)
	// The runaway message names the word exactly once, to tell the operator
	// what not to do. Strip that corrective clause, then require that no
	// mention survives anywhere else. A bare search for the stem would flag
	// the very sentence that replaces the misleading advice, so the check has
	// to be about what is left after the correction.
	stem := regexp.MustCompile(`(?i)rais(?:e|es|ed|ing)`)
	corrective := regexp.MustCompile(`(?i)[^.\n]*rais(?:e|es|ed|ing)[^.\n]*larger[^.\n]*`)
	if !corrective.MatchString(blob) {
		t.Fatalf("runaway message must carry the corrective that a bigger cap enlarges the runaway, got: %s", blob)
	}
	residue := corrective.ReplaceAllString(blob, "")
	if stem.MatchString(residue) {
		t.Fatalf("runaway message steers the operator toward the cap outside the correction: %s", blob)
	}
	if !strings.Contains(strings.ToLower(blob), "reasoning") && !strings.Contains(blob, "output cap") {
		t.Fatalf("runaway message must say what consumed the budget: %s", blob)
	}
	raw, rerr := os.ReadFile(dir + "/captured.json")
	if rerr != nil {
		t.Fatalf("runaway capture must still be written: %v", rerr)
	}
	if !strings.Contains(string(raw), "thinking") {
		t.Fatalf("capture must hold the raw response, got %q", string(raw))
	}
}

// A genuine overflow -- content came back and was cut off mid-answer -- keeps
// its terminal class. The runaway test above must not have widened it.
func TestCompleteGenuineOverflowStaysDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"content present", `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}],"usage":{"completion_tokens":4096}}`},
		{"content present, partial budget", `{"choices":[{"message":{"content":"{\"findings\":"},"finish_reason":"length"}],"usage":{"completion_tokens":100,"completion_tokens_details":{"reasoning_tokens":10}}}`},
		{"no usage reported", `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")
			h := &runawayHandler{body: tc.body}
			ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
			defer ts.Close()
			c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

			_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
			if !errors.Is(err, ErrDeterministic) {
				t.Fatalf("genuine overflow must stay terminal deterministic, got %v", err)
			}
			if errors.Is(err, ErrRunaway) {
				t.Fatalf("genuine overflow misclassified as a runaway: %v", err)
			}
		})
	}
}

// reasoning_tokens is part of the evidence Cite needs to explain the failure,
// so it must survive decoding.
func TestUsageCarriesReasoningTokens(t *testing.T) {
	h := &runawayHandler{body: `{"choices":[{"message":{"content":"ok","reasoning":"thought"},` +
		`"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":900,` +
		`"completion_tokens_details":{"reasoning_tokens":850}}}`}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	resp, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatalf("non-truncated call: %v", err)
	}
	if resp.Usage.ReasoningTokens != 850 {
		t.Fatalf("ReasoningTokens = %d, want 850", resp.Usage.ReasoningTokens)
	}
}

// A provider that itemises no reasoning_tokens still puts the reasoning in the
// message body, and Cite decodes that body. The captured runaway that settled
// whether this failure is a loop arrived exactly that way: 176812 bytes of
// response, content "", and no completion_tokens_details at all. Everything in
// it was reasoning, and the message the operator read named only the cap.
//
// So the message has to be able to say how much text actually came back when
// the provider reports no split of its own. Without that, the one number
// available is the budget, which is precisely the number the finding says was
// never the constraint.
func TestRunawayMessageNamesTheObservedReasoningTrace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

	const trace = "Let me review this file. Hmm. A broken anchor is plausible. Hmm. OK, I'll report it."
	body := `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"` +
		trace + `"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":12361,"completion_tokens":4096,"total_tokens":16457}}`
	h := &runawayHandler{body: body}
	ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
	if !errors.Is(err, ErrRunaway) {
		t.Fatalf("err = %v, want ErrRunaway", err)
	}
	if !strings.Contains(err.Error(), itoa(len(trace))) {
		t.Fatalf("runaway message must name the %d characters of reasoning the provider actually returned, got: %v",
			len(trace), err)
	}
	// The bare number is not enough: the fixture reports completion_tokens at
	// the cap, so a trace of exactly that length would satisfy the check above
	// against a message naming only the budget. Require the unit too.
	if !strings.Contains(err.Error(), "characters") {
		t.Fatalf("runaway message must report the trace in characters rather than as the budget, got: %v", err)
	}
}

// The reverse guard on the same change: a response with no reasoning trace
// must not have one invented for it. The captured fixture had 173631
// characters of reasoning and zero of content, and a caller that reports no
// reasoning at all is a different shape with a different remedy.
func TestRunawayMessageInventsNoReasoningTrace(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no reasoning field", `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":4096}}`},
		{"empty reasoning field", `{"choices":[{"message":{"role":"assistant","content":"","reasoning":""},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":4096}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CITE_TRUNCATED_OUT", dir+"/captured.json")

			h := &runawayHandler{body: tc.body}
			ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
			defer ts.Close()
			c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

			_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
			if !errors.Is(err, ErrRunaway) {
				t.Fatalf("err = %v, want ErrRunaway", err)
			}
			if strings.Contains(err.Error(), "characters") {
				t.Fatalf("runaway message reported a reasoning trace the provider never sent: %v", err)
			}
		})
	}
}

// A provider that sends message.reasoning as anything other than a string must
// not cost a valid review. Decoding the field into a plain Go string makes
// json.Unmarshal reject the entire response, and a well-formed review goes
// with it as a deterministic malformed provider response. The sibling
// diagnostic field Provider is raw for the same reason.
func TestCompleteSurvivesANonStringReasoningField(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"array", `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}","reasoning":["a","b"]},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":200}}`},
		{"object", `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}","reasoning":{"steps":[]}},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":200}}`},
		{"number", `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}","reasoning":42},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":200}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &runawayHandler{body: tc.body}
			ts := newTestServer(http.HandlerFunc(h.ServeHTTP))
			defer ts.Close()
			c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}

			resp, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 4096})
			if err != nil {
				t.Fatalf("a non-string reasoning field must not reject a valid review: %v", err)
			}
			if resp.Text != `{"ok":true}` {
				t.Fatalf("Text = %q, want the provider's answer", resp.Text)
			}
		})
	}
}
