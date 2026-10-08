package model

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A status the same request meets again is terminal: it unwraps to
// ErrDeterministic, so the reviewer spends no retry on it. Ollama Cloud
// answers 410 for a retired model, and three retries of that were three
// certain failures.
func TestTerminalStatusesUnwrapToDeterministic(t *testing.T) {
	cases := map[int]struct {
		code     string
		terminal bool
	}{
		http.StatusGone:                {"model_unavailable", true},
		http.StatusNotFound:            {"model_unavailable", true},
		http.StatusBadRequest:          {"bad_request", true},
		http.StatusUnprocessableEntity: {"bad_request", true},
		http.StatusUnauthorized:        {"auth", true},
		http.StatusTooManyRequests:     {"rate_limited", false},
		http.StatusBadGateway:          {"provider_unavailable", false},
	}
	for status, want := range cases {
		ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"qwen3.5:397b was retired"}}`))
		}))
		c := &OpenAICompatClient{BaseURL: ts.URL, Model: "qwen3.5:397b", APIKey: "k"}
		_, err := c.Complete(context.Background(), CompletionRequest{MaxOutputTokens: 16})
		ts.Close()
		if err == nil {
			t.Fatalf("HTTP %d: want an error", status)
		}
		if !strings.HasPrefix(err.Error(), want.code+":") {
			t.Errorf("HTTP %d: error %q, want code %s", status, err, want.code)
		}
		if got := errors.Is(err, ErrDeterministic); got != want.terminal {
			t.Errorf("HTTP %d: terminal=%v, want %v", status, got, want.terminal)
		}
		if status == http.StatusGone && !strings.Contains(err.Error(), `"qwen3.5:397b"`) {
			t.Errorf("HTTP 410 should name the model it could not serve: %q", err)
		}
	}
}
