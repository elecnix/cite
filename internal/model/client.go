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

// ErrDeadline is a per-request deadline expiry. Errors wrapping it name the
// per-call deadline VALUE, so a failure log says what timeout actually
// applied — a bare "deadline exceeded" gives an operator nothing to compare
// against roles.<role>.timeout (issue #28).
var ErrDeadline = errors.New("request deadline exceeded")

// ProviderDescriber is an optional Client capability: it names the provider
// endpoint the client talks to, so a run can log which provider and model it
// used — including runs that die before the record is printed.
type ProviderDescriber interface {
	DescribeProvider() string
}

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

// StructuredOutputMode selects how Cite asks a provider for schema-shaped
// JSON. It is wired from the GitHub Action's `structured_output` input.
type StructuredOutputMode string

const (
	// StructuredOutputResponseFormat is the default: the OpenAI-compatible
	// response_format: {type: json_schema} field, which the provider must
	// enforce. OpenAI and OpenRouter honour it.
	StructuredOutputResponseFormat StructuredOutputMode = "response_format"
	// StructuredOutputTools offers one function tool whose parameters are the
	// response schema and forces the model to call it. Providers that ignore
	// response_format — Ollama Cloud does so silently — still return the
	// arguments as a tool call, which Cite decodes as the response.
	StructuredOutputTools StructuredOutputMode = "tools"
)

// ParseStructuredOutputMode validates a configured mode. Empty means the
// default, so an unset action input keeps the historical behaviour.
func ParseStructuredOutputMode(s string) (StructuredOutputMode, error) {
	switch s {
	case "", string(StructuredOutputResponseFormat):
		return StructuredOutputResponseFormat, nil
	case string(StructuredOutputTools):
		return StructuredOutputTools, nil
	default:
		return "", fmt.Errorf("structured output mode %q: want %q or %q",
			s, StructuredOutputResponseFormat, StructuredOutputTools)
	}
}

// ToolCall is one function call a model made.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Message is one turn of a follow-up conversation the caller supplies after a
// failed structured-output attempt. Role is "assistant", "tool" or "user".
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolDef is one function tool offered to the model.
type ToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// NewToolDef builds a function tool whose parameters are the response schema.
func NewToolDef(name, description string, schema json.RawMessage) ToolDef {
	t := ToolDef{Type: "function"}
	t.Function.Name = name
	t.Function.Description = description
	t.Function.Parameters = schema
	return t
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

	// Tools, when set, offers function tools, and ToolChoice forces one of
	// them. The tools mode uses these instead of ResponseSchema; a provider
	// that ignores response_format still returns the arguments as a tool call.
	Tools      []ToolDef
	ToolChoice any

	// History, when set, appends turns after the user message. The reviewer
	// uses it to follow up a rejected tool call with the conversation so far
	// (the assistant's call, a tool result naming the error, and a user turn
	// asking for a proper call) instead of repeating the prompt blind.
	History []Message

	// RequireParameters adds a top-level "provider": {"require_parameters":
	// true} to the chat completion request. Routers such as OpenRouter then
	// only pick endpoints that support every request parameter (structured
	// outputs here); when none does, the call fails with a routing error
	// instead of the endpoint silently dropping response_format and returning
	// an empty body.
	RequireParameters bool

	// SessionID, when set, is sent as the x-session-id header. OpenRouter
	// uses it as the sticky-routing key, so every call of one run reaches
	// the same upstream provider and reads the same prompt cache.
	SessionID string
}

// Usage records token counters, including cache behaviour, which CI asserts on
// because caching failure is silent (§7). CostUSD is what the provider billed
// for the call, when the provider reports it (OpenRouter's usage.cost).
// CostReported tells a billed $0, such as a free model, apart from a provider
// that reports no cost at all.
type Usage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostReported     bool    `json:"cost_reported,omitempty"`
}

// chatCompletionsUsage is the usage object of a /chat/completions response.
// Its key names differ from Usage's: decoding the wire object straight into
// Usage silently read 0 tokens on every call (issue #103). Cost is the USD
// amount a billing gateway such as OpenRouter charged for the call.
type chatCompletionsUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	Cost *float64 `json:"cost"`
}

// toUsage maps the chat-completions key names onto the provider-neutral
// counters.
func (w chatCompletionsUsage) toUsage() Usage {
	u := Usage{
		InputTokens:  w.PromptTokens,
		OutputTokens: w.CompletionTokens,
	}
	if w.Cost != nil {
		u.CostUSD = *w.Cost
		u.CostReported = true
	}
	if d := w.PromptTokensDetails; d != nil {
		u.CacheReadTokens = d.CachedTokens
		u.CacheWriteTokens = d.CacheWriteTokens
	}
	return u
}

// CacheHitRate is the fraction of prompt tokens served from provider cache.
// Cached tokens are reported as part of prompt tokens, so the denominator is
// InputTokens. Zero input means nothing was measured and the rate is zero —
// never NaN.
func (u Usage) CacheHitRate() float64 {
	if u.InputTokens <= 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(u.InputTokens)
}

// CompletionResponse carries the text and the counters.
type CompletionResponse struct {
	Text         string
	Usage        Usage
	FinishReason string
	Model        string
	// ToolCalls are the function calls the model made, when it made any. In
	// tools mode Text carries the arguments of the matching call, so callers
	// that only decode Text need no change; the raw calls let a caller quote
	// the rejected one back in a follow-up.
	ToolCalls []ToolCall
	// Provider is the upstream provider a router reported serving the call
	// (OpenRouter's top-level "provider" field); empty when not reported.
	Provider string
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
// GitHub Models zero-secret path (§1, "A first run without a key"): when
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

// DescribeProvider names the endpoint this client targets (ProviderDescriber).
func (c *OpenAICompatClient) DescribeProvider() string { return c.BaseURL }

// typedError maps provider errors to typed codes before rendering (I4).
type typedError struct {
	Code string
	Body string // sanitised: never verbatim provider text with headers
}

func (e *typedError) Error() string { return e.Code + ": " + e.Body }

// Complete performs one bounded call. The deadline comes from ctx, set at the
// call site. The output-token cap is the bound, never an inactivity timeout.
func (c *OpenAICompatClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	start := time.Now()
	if _, ok := ctx.Deadline(); !ok {
		// An explicit per-request deadline at the call site. Never inherit
		// an unbounded client default. The fallback mirrors the triage role
		// default (15 minutes): a wall-clock cap is a safety net for a hung
		// call, not a tuning knob — provider queueing alone can eat minutes
		// before the first byte.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
	}
	// deadlineBudget renders the per-call deadline actually in force, for
	// error messages: the value that was exceeded must appear in the error
	// (issue #28).
	deadlineBudget := func() string {
		dl, ok := ctx.Deadline()
		if !ok {
			return "none"
		}
		return dl.Sub(start).Round(time.Millisecond).String()
	}
	// messages is []any rather than []map[string]string: a follow-up turn
	// carries tool_calls (an array) and tool_call_id, which a string map
	// cannot express.
	messages := make([]any, 0, 2+len(req.History))
	messages = append(messages,
		map[string]any{"role": "system", "content": req.System},
		map[string]any{"role": "user", "content": req.User},
	)
	for _, m := range req.History {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			msg["tool_calls"] = m.ToolCalls
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		messages = append(messages, msg)
	}
	httpReq := map[string]any{
		"model":       c.Model,
		"temperature": req.Temperature,
		"max_tokens":  req.MaxOutputTokens,
		"messages":    messages,
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
	if len(req.Tools) > 0 {
		httpReq["tools"] = req.Tools
		if req.ToolChoice != nil {
			httpReq["tool_choice"] = req.ToolChoice
		}
	}
	if req.RequireParameters {
		httpReq["provider"] = map[string]any{"require_parameters": true}
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
	if req.SessionID != "" {
		// A header, not the top-level session_id body field OpenRouter also
		// accepts: OpenAI's API rejects unknown request arguments with a 400,
		// while every endpoint ignores a header it does not know.
		httpRequest.Header.Set("X-Session-Id", req.SessionID)
	}
	resp, err := httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w (per-call deadline was %s)", ErrDeadline, deadlineBudget())
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
		// Provider stays raw: it is a diagnostic label, and a field of
		// an unexpected type must not fail a call whose review is fine.
		Provider json.RawMessage `json:"provider"`
		Choices  []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage chatCompletionsUsage `json:"usage"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			// A stall during the body read is a deadline expiry too: type it
			// as ErrDeadline with the value, or it reaches the operator as a
			// bare "reading response: context deadline exceeded" with no
			// hint of which knob to turn (issue #28).
			return nil, fmt.Errorf("%w while reading the response body (per-call deadline was %s)", ErrDeadline, deadlineBudget())
		}
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if os.Getenv("CITE_DEBUG") != "" {
		// Debug aid: dump the exact response body so a truncation or a
		// decode failure can be replayed and inspected. Complements the
		// request dump above. The body contains no credentials.
		_ = os.WriteFile("/tmp/cite-last-response.json", raw, 0o600)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: malformed provider response", ErrDeterministic)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: no choices in response", ErrDeterministic)
	}
	ch := out.Choices[0]
	// In tools mode the answer is the forced call's argument. A call to a
	// function Cite did not offer is not that answer, so Text then stays the
	// content and the caller rejects it and follows up.
	text := ch.Message.Content
	if len(req.Tools) > 0 {
		if args, ok := toolArguments(ch.Message.ToolCalls, req.Tools[0].Function.Name); ok {
			text = args
		}
	}
	if ch.FinishReason == "length" {
		// A truncated response truncates identically on retry. Terminal.
		// Always capture the partial content to a file first: the error
		// discards it, and without this there is nothing to inspect after
		// the fact. The path is overridable so a caller (the GitHub
		// Action) can point it at a directory it archives for download.
		capturePath := os.Getenv("CITE_TRUNCATED_OUT")
		if capturePath == "" {
			capturePath = "/tmp/cite-truncated-response.json"
		}
		_ = os.WriteFile(capturePath, raw, 0o600)
		// The partial content is what the operator needs to see, so it
		// always goes to stderr: a CI run that captures stderr keeps it in
		// the job log, where it can be downloaded later.
		fmt.Fprintf(os.Stderr, "cite: partial output before the token cap captured to %s (%d bytes):\n%s\n", capturePath, len(raw), text)
		return nil, fmt.Errorf("%w: output truncated at token cap (finish_reason=length)", ErrDeterministic)
	}
	return &CompletionResponse{
		Text:         text,
		ToolCalls:    ch.Message.ToolCalls,
		Usage:        out.Usage.toUsage(),
		FinishReason: ch.FinishReason,
		Model:        c.Model,
		Provider:     upstreamProvider(out.Provider),
	}, nil
}

// toolArguments returns the arguments of the first tool call naming want. A
// call to another function is ignored, and empty arguments are not an answer.
func toolArguments(calls []ToolCall, want string) (string, bool) {
	for _, c := range calls {
		if want != "" && c.Function.Name != want {
			continue
		}
		if strings.TrimSpace(c.Function.Arguments) == "" {
			return "", false
		}
		return c.Function.Arguments, true
	}
	return "", false
}

// upstreamProvider reads a router's upstream provider label, which
// OpenRouter sends as a string. Any other type, or none, gives "".
func upstreamProvider(raw json.RawMessage) string {
	var name string
	if len(raw) == 0 || json.Unmarshal(raw, &name) != nil {
		return ""
	}
	return name
}
