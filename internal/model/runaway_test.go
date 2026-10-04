package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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
func TestRunawayMessageDoesNotAdviseRaisingTheCap(t *testing.T) {
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
	for _, bad := range []string{"raise", "raise the cap", "larger cap", "increase the cap"} {
		if strings.Contains(strings.ToLower(blob), bad) {
			t.Fatalf("runaway message advises %q, which cannot help: %s", bad, blob)
		}
	}
	if !strings.Contains(strings.ToLower(blob), "reasoning") {
		t.Fatalf("runaway message must name reasoning as what ate the budget: %s", blob)
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
