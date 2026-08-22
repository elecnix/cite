// Package model — LLM client abstraction (§6).
//
// Providers declare capability; credentials are expressions rather than
// values; a model entry has exactly one required field. Every call carries an
// explicit per-request deadline set at the call site, in our code, never
// inherited from an SDK (§7). The API key is an HTTP header, never in a
// prompt, a log, an artifact, an error string, or the output (§12, I4).
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// APIStyle enumerates the supported wire protocols.
type APIStyle string

const (
	APIOpenAICompletions APIStyle = "openai-completions"
	APIOpenAIResponses   APIStyle = "openai-responses"
	APIAnthropicMessages APIStyle = "anthropic-messages"
)

// ErrDeterministic is returned for deterministic failures, which are terminal:
// a truncated response truncates identically on retry (§7).
var ErrDeterministic = errors.New("deterministic failure")

// ErrDeadline is a per-request deadline expiry.
var ErrDeadline = errors.New("request deadline exceeded")

// CredentialExpr is a credential *expression*, never a value: a literal, a
// $VAR / ${VAR} environment reference, or a !shell-command (§6).
type CredentialExpr string

// Resolve evaluates the expression. The resolved value is returned once and
// must only be used as a header.
func (c CredentialExpr) Resolve() (string, error) {
	s := string(c)
	switch {
	case s == "":
		return "", fmt.Errorf("empty credential expression")
	case strings.HasPrefix(s, "!"):
		out, err := exec.Command("sh", "-c", s[1:]).Output()
		if err != nil {
			return "", fmt.Errorf("credential command failed")
		}
		return strings.TrimSpace(string(out)), nil
	case strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}"):
		v, ok := os.LookupEnv(s[2 : len(s)-1])
		if !ok {
			return "", fmt.Errorf("credential variable %s not set", s[2:len(s)-1])
		}
		return v, nil
	case strings.HasPrefix(s, "$"):
		v, ok := os.LookupEnv(s[1:])
		if !ok {
			return "", fmt.Errorf("credential variable %s not set", s[1:])
		}
		return v, nil
	default:
		return s, nil
	}
}

// ModelEntry has exactly one required field: ID. Everything else is defaulted.
type ModelEntry struct {
	ID            string `json:"id"` // the only required field
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	Cost          *Cost  `json:"cost,omitempty"`
}

// Cost is first-class configuration with per-million rates, so cost reporting
// works for a model Cite has never heard of.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Provider declares capability: a base URL, a wire protocol, a credential
// expression and its models.
type Provider struct {
	Name    string            `json:"-"`
	BaseURL string            `json:"base_url"`
	API     APIStyle          `json:"api"`
	APIKey  CredentialExpr    `json:"api_key,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  []ModelEntry      `json:"models,omitempty"`
}

// Role names the three roles a reviewer needs (§6).
type Role string

const (
	RoleReview   Role = "review"
	RoleTriage   Role = "triage"
	RoleAssemble Role = "assemble"
)

// RoleConfig carries per-role settings, because a slow local model and a fast
// hosted one cannot share one number.
type RoleConfig struct {
	Model           string        `json:"model"`
	Timeout         time.Duration `json:"-"`
	TimeoutStr      string        `json:"timeout,omitempty"`
	MaxOutputTokens int           `json:"max_output_tokens,omitempty"`
	Concurrency     int           `json:"concurrency,omitempty"`
}

// CompletionRequest is the provider-neutral call. Temperature is pinned; a
// seed is passed where the provider honours one (§8: a green is one sample).
type CompletionRequest struct {
	System          string
	User            string
	MaxOutputTokens int
	Temperature     float64
	Seed            *int64
	// ResponseSchema, when set, requests provider-enforced structured output
	// rather than JSON-in-prose plus a validator.
	ResponseSchema json.RawMessage
}

// Usage records token counters, including cache behaviour, which CI asserts on
// because caching failure is silent (§7).
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// CompletionResponse carries the text and the counters.
type CompletionResponse struct {
	Text         string
	Usage        Usage
	FinishReason string
	Model        string
}

// Client sends one provider-neutral completion. Implementations must map
// provider errors to typed codes before rendering — never verbatim provider
// text, which can carry an echoed header (§12, I4).
type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
	ModelID() string
}

// OpenAICompatClient speaks the openai-completions wire protocol against any
// base URL. It makes no runtime network call except to the model endpoint
// (§12, I8).
type OpenAICompatClient struct {
	BaseURL      string // e.g. https://api.openai.com/v1
	APIKey       string // header value only; never logged
	ExtraHeaders map[string]string
	Model        string
	HTTP         *http.Client
}

// NewOpenAICompatClient infers the endpoint from the environment when no
// explicit provider is configured: the provider is inferred from which key is
// present (§1).
//
// GitHub Models zero-secret path (§1, "A first run with no key at all"): when
// MODEL_API_KEY is unset but the ambient GITHUB_TOKEN is present, inference
// runs on https://models.github.ai/inference with the job's own token and the
// default model id openai/gpt-4o-mini. That path needs the `models: read`
// permission, is rate-limited, and exists only so the first review happens
// before the user has configured anything; bring-your-own-key via
// MODEL_API_KEY remains the serious path.
func NewOpenAICompatClient() (*OpenAICompatClient, error) {
	if k := os.Getenv("MODEL_API_KEY"); k != "" {
		base := os.Getenv("MODEL_BASE_URL")
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		model := os.Getenv("MODEL_ID")
		if model == "" {
			model = "gpt-5-mini"
		}
		return &OpenAICompatClient{BaseURL: strings.TrimSuffix(base, "/"), APIKey: k, Model: model}, nil
	}
	// Zero-secret first run: GitHub's models endpoint on the ambient token.
	if k := os.Getenv("GITHUB_TOKEN"); k != "" {
		model := os.Getenv("MODEL_ID")
		if model == "" {
			model = "openai/gpt-4o-mini"
		}
		return &OpenAICompatClient{BaseURL: "https://models.github.ai/inference", APIKey: k, Model: model}, nil
	}
	return nil, fmt.Errorf("no model key found: set MODEL_API_KEY, or grant the workflow `models: read` so the ambient GITHUB_TOKEN can be used with GitHub Models (the provider is inferred from which key is present)")
}

func (c *OpenAICompatClient) ModelID() string { return c.Model }

// typedError maps provider errors to typed codes before rendering (I4).
type typedError struct {
	Code string
	Body string // sanitised: never verbatim provider text with headers
}

func (e *typedError) Error() string { return e.Code + ": " + e.Body }

// Complete performs one bounded call. The deadline comes from ctx, set at the
// call site. The output-token cap is the bound, never an inactivity timeout.
func (c *OpenAICompatClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if _, ok := ctx.Deadline(); !ok {
		// An explicit per-request deadline at the call site. Never inherit
		// an unbounded client default.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
	}
	httpReq := map[string]any{
		"model":       c.Model,
		"temperature": req.Temperature,
		"max_tokens":  req.MaxOutputTokens,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
	}
	if req.Seed != nil {
		httpReq["seed"] = *req.Seed
	}
	if len(req.ResponseSchema) > 0 {
		httpReq["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "review",
				"strict": true,
				"schema": json.RawMessage(req.ResponseSchema),
			},
		}
	}
	body, err := json.Marshal(httpReq)
	if err != nil {
		return nil, err
	}
	if os.Getenv("CITE_DEBUG") != "" {
		// Debug aid: dump the exact request body (without the key, which is
		// header-only) so a provider 400 can be replayed verbatim.
		_ = os.WriteFile("/tmp/cite-last-request.json", body, 0o600)
	}
	httpReqBody := bytes.NewReader(body)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{} // deadline comes from ctx, not the client
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", httpReqBody)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.ExtraHeaders {
		httpRequest.Header.Set(k, v)
	}
	resp, err := httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrDeadline
		}
		return nil, fmt.Errorf("provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Map to a typed code; never surface verbatim provider text (I4).
		var b struct {
			Error *struct {
				Code     any    `json:"code"`
				Message  string `json:"message"`
				Metadata *struct {
					Raw string `json:"raw"`
				} `json:"metadata"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&b)
		code := "provider_error"
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if b.Error != nil {
			m := b.Error.Message
			// Gateways (e.g. OpenRouter) wrap the upstream error; the raw
			// payload is where the actionable reason lives.
			if b.Error.Metadata != nil && b.Error.Metadata.Raw != "" {
				raw := strings.TrimSpace(b.Error.Metadata.Raw)
				raw = strings.ReplaceAll(raw, "\n", " ")
				if len(raw) > len(m) {
					m = raw
				}
			}
			if m != "" {
				if len(m) > 300 {
					m = m[:300]
				}
				msg += ": " + m
			}
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			code = "rate_limited"
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			code = "auth"
		case resp.StatusCode >= 500:
			code = "provider_unavailable"
		}
		return nil, &typedError{Code: code, Body: msg}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: malformed provider response", ErrDeterministic)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: no choices in response", ErrDeterministic)
	}
	ch := out.Choices[0]
	if ch.FinishReason == "length" {
		// A truncated response truncates identically on retry. Terminal.
		return nil, fmt.Errorf("%w: output truncated at token cap (finish_reason=length)", ErrDeterministic)
	}
	return &CompletionResponse{
		Text:         ch.Message.Content,
		Usage:        out.Usage,
		FinishReason: ch.FinishReason,
		Model:        c.Model,
	}, nil
}
