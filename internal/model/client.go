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

// ErrRunaway is returned when the provider spent the entire output budget and
// produced NO answer: reasoning tokens consumed the whole max_tokens and the
// content came back empty (RunawayGeneration is the test).
//
// It is deliberately NOT ErrDeterministic. The terminal class rests on "a
// truncated response truncates identically on retry", and measurement refutes
// that premise for this shape. Against deepseek/deepseek-v4-flash-0731, one
// identical request at one identical 12288 cap and temperature 0 exhausted the
// whole budget in reasoning on two attempts out of four and terminated cleanly
// on the other two. Nothing about the request made the failure repeat, so
// treating it as deterministic turned a recoverable miss into a
// COULD_NOT_EVALUATE for the entire run.
var ErrRunaway = errors.New("runaway generation")

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

	// ReasoningEffort, when non-empty, is sent as the request's
	// reasoning_effort field. Ollama counts reasoning tokens against
	// max_tokens, so a heavy reasoner can spend the entire output budget
	// thinking and never answer; "none" suppresses that. Providers that do
	// not know the field see it only when an operator sets it.
	ReasoningEffort string

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
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	// ReasoningTokens are the part of OutputTokens the provider spent
	// thinking rather than answering. A provider that bills reasoning
	// against max_tokens spends this out of the SAME budget Cite sizes from
	// the answer's worst case, so a large ReasoningTokens alongside an empty
	// answer is a runaway generation, not a too-small cap.
	//
	// Zero here means UNREPORTED as often as it means none. A provider that
	// omits completion_tokens_details leaves this at 0 while the response
	// still carries the trace in the reasoning field, and the two are not the
	// same thing: reasoning dominance is decided from the reasoning itself
	// (RunawayGeneration), never from this number being non-zero, precisely
	// because a missing details object cannot be told from a real zero.
	// Nothing is estimated into this field; a character count is not a token
	// count, and an invented number would reach CI's usage assertions and
	// the operator string as if the provider had said it.
	ReasoningTokens int     `json:"reasoning_tokens,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	CostReported    bool    `json:"cost_reported,omitempty"`
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
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
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
	if d := w.CompletionTokensDetails; d != nil {
		u.ReasoningTokens = d.ReasoningTokens
	}
	return u
}

// RunawayGeneration reports whether a finish_reason=length response is a
// runaway generation rather than a capacity overflow.
//
// The two are told apart by what the provider actually returned, not by
// comparing sizes. A capacity overflow means the answer was too big for the
// cap: content came back and was cut off, and a bigger cap holds it. A runaway
// means the cap was never the constraint: the whole budget was spent and no
// answer was emitted, so there is nothing a bigger cap could hold either.
// Measured at two caps on one request, content arrived null with
// completion_tokens == max_tokens both times, and raising the cap from 12288
// to 24576 grew the runaway from 96 KB to 186 KB rather than producing an
// answer.
//
// answer is the usable answer -- the forced tool call's arguments in tools
// mode, otherwise the content. reasoning is the reasoning trace the response
// carries. maxTokens is the request Cite made and is only consulted when the
// response carries no reasoning at all. A response carrying a tool call never
// reaches here: the caller treats it as an answer being truncated.
//
// The empty-answer test is load-bearing on its own. The reasoning test is the
// one that decides the measured case, and it exists because the cap
// comparison cannot: a provider is free to stop below the max_tokens Cite
// asked for, and then OutputTokens >= maxTokens is false no matter how the
// generation went. Measured, the provider stopped at exactly 65536 while Cite
// requested 131072, so every genuine runaway came back as a capacity
// overflow and the file was lost. Reasoning presence does not depend on any
// number Cite chose: it is in the response, and an empty answer next to a
// large reasoning trace is the shape of a model that spent its whole
// generation thinking and never answered.
//
// The full-budget test is kept, not replaced. It still catches a provider
// that itemises no reasoning at all yet reports the whole cap spent, which is
// the same shape by another route. It is deliberately NOT the primary signal,
// so a provider that reports no usage and no reasoning reads neither way and
// stays the terminal overflow it was.
//
// A "dominance" threshold on the reasoning fraction was considered and
// rejected on its own numbers: the measured trace is 173631 characters for
// 65536 output tokens, which is about 0.66 of the budget at the usual
// four-characters-per-token, so any dominance threshold tight enough to be
// meaningful rejects the exact response this fix exists for.
//
// A tool call the cap cut in half is an answer being truncated, not a
// runaway, so a response carrying one never reaches this predicate; the
// caller checks for it first. Reasoning presence is a model property, not a
// runaway property: a gateway that puts the whole answer in the reasoning
// field and reports empty content will be re-asked once and then reported as
// runaway_generation. That is a bounded, non-terminal cost, and it was the
// terminal overflow before.
//
// A provider that refuses or filters a request also returns an empty answer,
// and the caller cannot tell that case from a runaway without a second signal.
// The cost of guessing wrong is bounded and stated here so the choice is
// visible rather than implicit: one extra call from the file's own budget,
// followed by the same fail-closed outcome either way. Note also that the only
// caller runs inside its finish_reason == "length" branch, and a refusal
// normally ends with finish_reason == "stop" instead, which never reaches here
// at all.
func RunawayGeneration(answer, reasoning string, usage Usage, maxTokens int) bool {
	if strings.TrimSpace(answer) != "" {
		return false
	}
	if strings.TrimSpace(reasoning) != "" {
		return true
	}
	return maxTokens > 0 && usage.OutputTokens >= maxTokens
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
	// Reasoning is the reasoning trace the provider returned, when it returns
	// one. It is empty for a provider that does not report reasoning at all,
	// and that absence is exactly why it is carried here separately from
	// Usage.ReasoningTokens: a provider can send 173 KB of reasoning while
	// omitting completion_tokens_details entirely, which leaves the token
	// counter at 0 and used to make the trace invisible to every line of
	// Cite but the raw capture.
	Reasoning string
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
	// ambient marks a client whose key is the job's own GITHUB_TOKEN rather
	// than a configured MODEL_API_KEY. It changes what a 401 means: the
	// permission is missing, not the secret, so the hint has to follow which
	// credential was actually sent rather than which endpoint was chosen.
	ambient bool
}

// githubModelsBase is the one endpoint the ambient GITHUB_TOKEN authenticates.
const githubModelsBase = "https://models.github.ai/inference"

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
	// A configured endpoint is never redirected to GitHub Models, and it is
	// never discarded either: it is the endpoint the operator asked for. The
	// ambient GITHUB_TOKEN authenticates GitHub Models and nothing else, so it
	// is attached to that endpoint alone; every other host gets whatever
	// MODEL_API_KEY holds, including nothing at all, which is what a server
	// needing no key asks for. Redirecting instead sent the job token to a
	// third-party host and requested a model that host does not serve, which
	// surfaced as an opaque provider error rather than as the misconfiguration
	// it was.
	if base := strings.TrimSuffix(os.Getenv("MODEL_BASE_URL"), "/"); base != "" {
		// The branch above returns whenever MODEL_API_KEY is set, so a key read
		// here is always empty: what is left to decide is which credential this
		// endpoint takes, and only GitHub Models takes the ambient token.
		model := os.Getenv("MODEL_ID")
		key := ""
		ambient := false
		if base == githubModelsBase {
			key = os.Getenv("GITHUB_TOKEN")
			ambient = key != ""
			if model == "" {
				model = "openai/gpt-4o-mini"
			}
		} else if model == "" {
			model = "gpt-5-mini"
		}
		return &OpenAICompatClient{BaseURL: base, APIKey: key, Model: model, ambient: ambient}, nil
	}
	// Zero-secret first run: nothing is configured, so GitHub's models
	// endpoint is inferred from the ambient token.
	if k := os.Getenv("GITHUB_TOKEN"); k != "" {
		model := os.Getenv("MODEL_ID")
		if model == "" {
			model = "openai/gpt-4o-mini"
		}
		return &OpenAICompatClient{BaseURL: githubModelsBase, APIKey: k, Model: model, ambient: true}, nil
	}
	return nil, fmt.Errorf("no model key found: set MODEL_API_KEY, or grant the workflow `models: read` so the ambient GITHUB_TOKEN can be used with GitHub Models (the provider is inferred from which key is present)")
}

func (c *OpenAICompatClient) ModelID() string { return c.Model }

// DescribeProvider names the endpoint this client targets (ProviderDescriber).
func (c *OpenAICompatClient) DescribeProvider() string { return c.BaseURL }

// authHint explains a 401 or 403 from the model endpoint. The two ways to
// reach it need different fixes: a client with no key at all is the missing
// secret (the Dependabot case, where Actions secrets are not readable), while
// the ambient token talking to GitHub Models is the permission case. Neither
// message helps the other, and a client holding a token for another endpoint
// has no configuration advice worth printing.
func authHint(c *OpenAICompatClient) string {
	switch {
	case c.APIKey == "" && c.BaseURL == githubModelsBase:
		// Pointed at GitHub Models with nothing to authenticate with: either
		// permission unlocks the ambient token, or a key replaces it.
		return "no credential reached GitHub Models: grant the workflow `models: read` so the ambient GITHUB_TOKEN can be used, or set MODEL_API_KEY"
	case c.APIKey == "":
		return "MODEL_API_KEY is empty; a run triggered by Dependabot cannot read Actions secrets, so the key must also exist in the Dependabot secret store"
	case c.ambient:
		return "the ambient GITHUB_TOKEN was rejected by GitHub Models; the zero-secret first run needs `models: read` on the workflow"
	}
	return ""
}

// bodyShape describes a response body that failed to decode without quoting
// any of it. The package doc is absolute that the key is never in an error
// string (§12, I4), and a gateway that reflects the request's Authorization
// header into its own page is exactly how it would get there, so no amount of
// pattern-matching on provider text — and not even one character of it — is a
// guarantee worth relying on. The body itself stays recoverable through the
// existing CITE_DEBUG capture.
func bodyShape(raw []byte) string {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	switch {
	case len(trimmed) == 0:
		return "an empty body"
	case trimmed[0] == '<':
		return "an HTML page rather than an OpenAI completion"
	case trimmed[0] == '{' || trimmed[0] == '[':
		return "a JSON document that does not decode as an OpenAI completion"
	default:
		return "a body that is not JSON"
	}
}

// typedError maps provider errors to typed codes before rendering (I4).
type typedError struct {
	Code string
	Body string // sanitised: never verbatim provider text with headers
	// Hint carries Cite's own guidance about the configuration that produced
	// the error. It is separate from Body because Body mirrors untrusted
	// provider text and is bounded on that account (I4), while a hint is
	// written here and can be as specific as the fix requires.
	Hint string
}

func (e *typedError) Error() string {
	if e.Hint == "" {
		return e.Code + ": " + e.Body
	}
	return e.Code + ": " + e.Body + " — " + e.Hint
}

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
	if req.ReasoningEffort != "" {
		httpReq["reasoning_effort"] = req.ReasoningEffort
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
		var hint string
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			code = "rate_limited"
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			code = "auth"
			hint = authHint(c)
		case resp.StatusCode >= 500:
			code = "provider_unavailable"
		}
		return nil, &typedError{Code: code, Body: msg, Hint: hint}
	}
	var out struct {
		// Provider stays raw: it is a diagnostic label, and a field of
		// an unexpected type must not fail a call whose review is fine.
		Provider json.RawMessage `json:"provider"`
		Choices  []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
				// Reasoning and ReasoningContent are the same trace under
				// the two names providers put it on: OpenAI-compatible
				// gateways that expose a thought trace use "reasoning",
				// DeepSeek's own surface uses "reasoning_content". Both
				// are decoded and neither wins by priority -- the first
				// non-empty does -- because this field is what tells a
				// runaway from a capacity overflow on a provider that
				// omits completion_tokens_details entirely.
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
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
		// Name the endpoint, the status and the shape — never the body.
		// "malformed provider response" alone left the operator nothing to act
		// on, and every way to reach it (a redirected endpoint, a gateway
		// interstitial, a model id the host does not serve) reads identically
		// without them.
		return nil, fmt.Errorf("%w: malformed provider response from %s (HTTP %d, %d bytes, %s; set CITE_DEBUG=1 to capture the body)", ErrDeterministic, c.BaseURL, resp.StatusCode, len(raw), bodyShape(raw))
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
	reasoning := reasoningTrace(ch.Message.Reasoning, ch.Message.ReasoningContent)
	if ch.FinishReason == "length" {
		// The path is overridable so a caller (the GitHub Action) can point
		// it at a directory it archives for download.
		capturePath := os.Getenv("CITE_TRUNCATED_OUT")
		if capturePath == "" {
			capturePath = "/tmp/cite-truncated-response.json"
		}
		_ = os.WriteFile(capturePath, raw, 0o600)
		usage := out.Usage.toUsage()
		// A tool call is an answer being written, even when the cap cut its
		// arguments in half: the model stopped thinking and started
		// emitting the response, which is where the cap bit. So the whole
		// runaway branch is skipped for one -- withholding only the
		// reasoning would still let a response with no reasoning field and a
		// full budget fall through to the full-budget test and call a
		// truncated answer a runaway.
		if len(ch.Message.ToolCalls) == 0 && RunawayGeneration(text, reasoning, usage, req.MaxOutputTokens) {
			// A runaway generation is not the capacity overflow this branch
			// was written for, and it is not terminal. The whole budget went
			// to reasoning and no answer came back, so the cap was never the
			// binding constraint and a bigger one cannot help: measured, the
			// same request at 12288 and at 24576 came back contentless both
			// times and the runaway merely grew with the cap (96 KB of
			// reasoning trace -> 186 KB). The captured bytes are what makes
			// this legible, so say plainly that they are a reasoning trace and
			// not a half-written answer -- an operator handed 186 KB of it
			// otherwise reads it as Cite having generated 186 KB of review.
			//
			// Three forms of `where`, because Cite cannot always name where
			// the tokens went. Each has to be true of the response it is
			// printed for: the first is the provider's own itemisation, the
			// second is the trace Cite can measure in bytes when the
			// provider itemises nothing, and the third is the old wording
			// and survives only on the branch where the cap really was
			// spent. Printing the cap wording for a response that never
			// reached the cap states a number Cite cannot verify and then
			// tells the operator it does not matter.
			where := fmt.Sprintf("the whole %d-token output cap was spent and no answer was written", req.MaxOutputTokens)
			switch {
			case usage.ReasoningTokens > 0:
				where = fmt.Sprintf("%d of %d output tokens went to reasoning and no answer was written",
					usage.ReasoningTokens, usage.OutputTokens)
			case len(reasoning) > 0:
				where = fmt.Sprintf("the provider returned %d bytes of reasoning and no answer; it stopped at %d of the %d output tokens requested and did not report how many of them were reasoning",
					len(reasoning), usage.OutputTokens, req.MaxOutputTokens)
			}
			fmt.Fprintf(os.Stderr, "cite: the model never terminated: %s. Captured reasoning trace at %s (%d bytes) -- raising the output-token cap makes this larger, not smaller\n", where, capturePath, len(raw))
			return nil, fmt.Errorf("%w: %s", ErrRunaway, where)
		}
		// A genuine overflow: content came back and was cut off. That is
		// terminal, because a truncated response truncates identically on
		// retry. Always capture the partial content first: the error discards
		// it, and without this there is nothing to inspect after the fact.
		// The partial content is what the operator needs to see, so it
		// always goes to stderr: a CI run that captures stderr keeps it in
		// the job log, where it can be downloaded later.
		fmt.Fprintf(os.Stderr, "cite: partial output before the token cap captured to %s (%d bytes):\n%s\n", capturePath, len(raw), text)
		return nil, fmt.Errorf("%w: output truncated at token cap (finish_reason=length)", ErrDeterministic)
	}
	return &CompletionResponse{
		Text:         text,
		ToolCalls:    ch.Message.ToolCalls,
		Reasoning:    reasoning,
		Usage:        out.Usage.toUsage(),
		FinishReason: ch.FinishReason,
		Model:        c.Model,
		Provider:     upstreamProvider(out.Provider),
	}, nil
}

// reasoningTrace picks the reasoning the response carries, whichever of the
// two wire names it arrived under.
func reasoningTrace(reasoning, reasoningContent string) string {
	if reasoning != "" {
		return reasoning
	}
	return reasoningContent
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
