package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestCredentialExpr(t *testing.T) {
	t.Setenv("CITE_TEST_KEY", "abc123")
	v, err := CredentialExpr("$CITE_TEST_KEY").Resolve()
	if err != nil || v != "abc123" {
		t.Fatalf("$VAR resolve: %q %v", v, err)
	}
	v, err = CredentialExpr("${CITE_TEST_KEY}").Resolve()
	if err != nil || v != "abc123" {
		t.Fatalf("${VAR} resolve: %q %v", v, err)
	}
	v, err = CredentialExpr("plain-literal").Resolve()
	if err != nil || v != "plain-literal" {
		t.Fatalf("literal resolve: %q %v", v, err)
	}
	if _, err := CredentialExpr("$CITE_TEST_MISSING").Resolve(); err == nil {
		t.Fatal("missing variable must error")
	}
}

func TestCompleteDeadlineEnforced(t *testing.T) {
	// The deadline is set at the call site; a hanging endpoint must return
	// ErrDeadline, not hang on an SDK default.
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, APIKey: "k", Model: "m"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8})
	if !errors.Is(err, ErrDeadline) {
		t.Fatalf("want ErrDeadline, got %v", err)
	}
}

func TestCompleteTruncatedIsDeterministic(t *testing.T) {
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": "partial"},
				"finish_reason": "length",
			}},
		})
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	_, err := c.Complete(context.Background(), CompletionRequest{})
	if !errors.Is(err, ErrDeterministic) {
		t.Fatalf("finish_reason=length must be a deterministic (terminal) failure, got %v", err)
	}
}

func TestProviderErrorIsTypedNotVerbatim(t *testing.T) {
	// I4: provider errors are mapped to typed codes before rendering.
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"key sk-abc123 is invalid and echoed Authorization: Bearer sk-abc123"}}`))
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	_, err := c.Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatal("want error")
	}
	te, ok := err.(*typedError)
	if !ok {
		t.Fatalf("want typedError, got %T: %v", err, err)
	}
	if te.Code != "auth" {
		t.Fatalf("want code auth, got %q", te.Code)
	}
	if len(te.Body) > 200 {
		t.Fatal("provider text must be truncated")
	}
}

func TestRequestCarriesStructuredOutputAndSeed(t *testing.T) {
	var gotBody map[string]any
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"input_tokens":10,"output_tokens":2}}`))
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	seed := int64(42)
	resp, err := c.Complete(context.Background(), CompletionRequest{
		Temperature: 0, Seed: &seed, MaxOutputTokens: 64,
		ResponseSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["seed"].(float64) != 42 {
		t.Fatal("seed not pinned")
	}
	if gotBody["temperature"].(float64) != 0 {
		t.Fatal("temperature not pinned")
	}
	if _, ok := gotBody["response_format"]; !ok {
		t.Fatal("structured output not requested")
	}
	if resp.Usage.InputTokens != 10 {
		t.Fatalf("usage not surfaced: %+v", resp.Usage)
	}
}

func TestNewClientPrefersModelAPIKey(t *testing.T) {
	// The bring-your-own-key path is unchanged: MODEL_API_KEY wins over the
	// ambient GITHUB_TOKEN.
	t.Setenv("MODEL_API_KEY", "sk-test")
	t.Setenv("GITHUB_TOKEN", "gh-token")
	c, err := NewOpenAICompatClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://api.openai.com/v1" || c.APIKey != "sk-test" || c.Model != "gpt-5-mini" {
		t.Fatalf("MODEL_API_KEY path changed: %+v", c)
	}
}

func TestNewClientFallsBackToGitHubModels(t *testing.T) {
	// Zero-secret first run (§1): no MODEL_API_KEY, ambient GITHUB_TOKEN
	// present → GitHub's models endpoint with the default model id. Pure
	// construction assertions; no network.
	t.Setenv("MODEL_API_KEY", "")
	t.Setenv("MODEL_BASE_URL", "")
	t.Setenv("MODEL_ID", "")
	t.Setenv("GITHUB_TOKEN", "gh-ambient")
	c, err := NewOpenAICompatClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://models.github.ai/inference" {
		t.Fatalf("base URL: %q", c.BaseURL)
	}
	if c.APIKey != "gh-ambient" {
		t.Fatalf("ambient token not used: %q", c.APIKey)
	}
	if c.Model != "openai/gpt-4o-mini" {
		t.Fatalf("default model: %q", c.Model)
	}
}

func TestNewClientGitHubModelsHonoursModelID(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "")
	t.Setenv("MODEL_ID", "openai/gpt-4.1-mini")
	t.Setenv("GITHUB_TOKEN", "gh-ambient")
	c, err := NewOpenAICompatClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "openai/gpt-4.1-mini" || c.BaseURL != "https://models.github.ai/inference" {
		t.Fatalf("MODEL_ID override: %+v", c)
	}
}

func TestNewClientErrorsWithoutAnyKey(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "")
	t.Setenv("MODEL_BASE_URL", "")
	t.Setenv("MODEL_ID", "")
	t.Setenv("GITHUB_TOKEN", "")
	c, err := NewOpenAICompatClient()
	if err == nil || c != nil {
		t.Fatalf("want error with no key at all, got %v %v", c, err)
	}
}
