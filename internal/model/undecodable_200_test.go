package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A 200 whose body is not an OpenAI completion is a blip, not a verdict.
//
// It used to be typed ErrDeterministic ("malformed provider response"), which
// made it terminal: completeWithRetry returned at once, FailoverClient had
// nothing to fail over to, the file was never reviewed and the gate concluded
// COULD_NOT_EVALUATE. elecnix/email4ynab#107 stayed red for a month on one
// such body: the same commit, model and endpoint then produced three clean
// runs in a row, so the condition was transient and the classification had
// frozen it into a permanent failure.
//
// Every shape below is the same class -- HTTP 200, no completion in it -- so
// every one of them must be retryable: not ErrDeterministic, eligible for
// failover, and carrying a positive backoff.
func TestUndecodable200IsRetryable(t *testing.T) {
	cases := []struct {
		name string
		body string
		// shape is what bodyShape says about the body, which the diagnostic
		// must name.
		shape string
		// echoed is provider text the error must not repeat.
		echoed []string
	}{
		{
			name:   "html interstitial",
			body:   "<html>\n<body>upstream timeout for sk-abc123XYZ</body>\n</html>",
			shape:  "an HTML page rather than an OpenAI completion",
			echoed: []string{"sk-abc123XYZ", "upstream timeout"},
		},
		{
			name:   "empty body",
			body:   "",
			shape:  "an empty body",
			echoed: nil,
		},
		{
			name:   "truncated json",
			body:   `{"choices":[{"message":{"content":"partial review`,
			shape:  "a JSON document that does not decode as an OpenAI completion",
			echoed: []string{"partial review"},
		},
		{
			name:   "not json at all",
			body:   "upstream connect timeout\n",
			shape:  "a body that is not JSON",
			echoed: []string{"upstream connect timeout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()
			c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m", APIKey: "k"}

			_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 8})
			if err == nil {
				t.Fatalf("a 200 carrying %s must be an error", tc.shape)
			}
			// The classification is the bug: ErrDeterministic means no retry,
			// no failover, no reviewed file.
			if errors.Is(err, ErrDeterministic) {
				t.Fatalf("%s must not be terminal (ErrDeterministic): %v", tc.shape, err)
			}
			if !FailsOver(err) {
				t.Fatalf("%s must be a failure another model may not share, so failover can act on it: %v", tc.shape, err)
			}
			// Bounded, and not the zero wait that would spend the whole
			// run-global bucket inside one provider stall.
			if d := RetryDelay(err, 0); d <= 0 || d > maxRetryDelay {
				t.Fatalf("RetryDelay = %s, want a positive wait within %s", d, maxRetryDelay)
			}
			// The diagnostic is the only thing an operator can act on, so
			// all of it survives the reclassification.
			for _, want := range []string{
				ts.URL,
				"HTTP 200",
				fmt.Sprintf("%d bytes", len(tc.body)),
				tc.shape,
				"CITE_DEBUG=1",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q must name %q", err.Error(), want)
				}
			}
			if strings.Contains(err.Error(), "\n") {
				t.Fatalf("error must stay single-line, got %q", err.Error())
			}
			for _, forbidden := range tc.echoed {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("the error must not echo provider text (%q): %q", forbidden, err.Error())
				}
			}
		})
	}
}

// The other half of the distinction: a 200 that carries a provider error
// envelope is a refusal, not a blip. The same request meets the same refusal,
// so this one stays terminal -- and says what the provider refused with,
// which is the only actionable half of the body.
func TestProviderErrorObjectAt200IsDeterministic(t *testing.T) {
	const body = `{"error":{"code":404,"message":"model qwen3.5:397b was retired\nuse qwen3.5:397b-instruct"}}`
	ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "qwen3.5:397b", APIKey: "k"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 8})
	if err == nil {
		t.Fatal("a 200 carrying only an error envelope must be an error")
	}
	if !errors.Is(err, ErrDeterministic) {
		t.Fatalf("a provider error object at 200 is a refusal and must stay terminal, got %v", err)
	}
	// The provider's own sentence is what names the fix; it is bounded and
	// flattened exactly as on a non-200 (I4: never verbatim, never multi-line).
	for _, want := range []string{ts.URL, "HTTP 200", "was retired"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must name %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error must stay single-line, got %q", err.Error())
	}
}

// A 200 that decodes as JSON but yields no choices is the same class as an
// undecodable one: no completion arrived. It is retryable, and its message
// names the endpoint and the shape so the operator is not left with "no
// choices in response".
func TestEmptyChoicesAt200IsRetryable(t *testing.T) {
	const body = `{"choices":[]}`
	ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m", APIKey: "k"}

	_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 8})
	if err == nil {
		t.Fatal("a 200 with no choices must be an error")
	}
	if errors.Is(err, ErrDeterministic) {
		t.Fatalf("a 200 with no choices must not be terminal: %v", err)
	}
	if !FailsOver(err) {
		t.Fatalf("a 200 with no choices must be eligible for failover: %v", err)
	}
	for _, want := range []string{ts.URL, "HTTP 200", "no choices"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must name %q", err.Error(), want)
		}
	}
}

// End to end through the failover client: a leg that answers 200 with an HTML
// page hands the call to the next model instead of ending the run. The
// sentinel branch of FailsOver is not what carries this -- the new class is a
// typedError, so it only moves on if its code is listed as retryable, which is
// what this pins.
func TestUndecodable200FailsOverToTheNextModel(t *testing.T) {
	ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer ts.Close()
	bad := &OpenAICompatClient{BaseURL: ts.URL, Model: "primary", APIKey: "k"}
	good := &legStub{id: "fallback"}

	resp, err := NewFailoverClient(bad, []Client{good}, nil).Complete(context.Background(), CompletionRequest{MaxOutputTokens: 8})
	if err != nil {
		t.Fatalf("the fallback must serve the call, got %v", err)
	}
	if resp.Model != "fallback" || good.calls != 1 {
		t.Fatalf("served by %q, fallback calls = %d", resp.Model, good.calls)
	}
}

// An error field beside a usable completion must not cost the review: the
// envelope is a refusal only when no completion came with it. This is the
// guard on the branch above, in the direction where being strict would throw
// away work.
func TestErrorFieldBesideACompletionIsNotARefusal(t *testing.T) {
	const body = `{"error":{"message":"rate limited upstream for a moment"},` +
		`"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`
	ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m", APIKey: "k"}

	resp, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 8})
	if err != nil {
		t.Fatalf("a completion that arrived with an incidental error field must be used: %v", err)
	}
	if resp.Text != `{"ok":true}` {
		t.Fatalf("Text = %q, want the completion", resp.Text)
	}
}
