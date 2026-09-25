package reviewer

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// scriptClient replays a fixed list of responses in order and records every
// request, so a test can inspect the follow-up conversation.
type scriptClient struct {
	mu    sync.Mutex
	reqs  []model.CompletionRequest
	resps []*model.CompletionResponse
}

func (c *scriptClient) Complete(_ context.Context, req model.CompletionRequest) (*model.CompletionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	if len(c.resps) == 0 {
		return &model.CompletionResponse{Text: "{}"}, nil
	}
	r := c.resps[0]
	c.resps = c.resps[1:]
	return r, nil
}

func (c *scriptClient) ModelID() string { return "script-client" }

func (c *scriptClient) requestAt(i int) model.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[i]
}

// toolCallResponse is what the OpenAI-compatible client returns in tools
// mode: the arguments surface as Text, and the raw call is kept so a caller
// can quote it back in a follow-up.
func toolCallResponse(name, arguments string) *model.CompletionResponse {
	call := model.ToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = arguments
	return &model.CompletionResponse{Text: arguments, ToolCalls: []model.ToolCall{call}}
}

// reviewCalls returns the indices of requests that offered the findings tool.
func reviewCalls(c *scriptClient) []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int
	for i, r := range c.reqs {
		if len(r.Tools) > 0 && r.Tools[0].Function.Name == toolNameFindings {
			out = append(out, i)
		}
	}
	return out
}

func TestToolsModeForcesToolAndFollowsUpOnRejection(t *testing.T) {
	c := &scriptClient{resps: []*model.CompletionResponse{
		toolCallResponse(toolNameTriage, triageJSON("a.go")),
		toolCallResponse(toolNameFindings, `{"bogus":true}`), // rejected: schema violation
		toolCallResponse(toolNameFindings, reviewJSON("a.go", "reviewed", nil)),
	}}
	rec, err := runOnce(t, baseInputs(), Options{
		Cfg: config.Default(), Client: c,
		StructuredOutput: model.StructuredOutputTools,
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := reviewCalls(c)
	if len(idx) != 2 {
		t.Fatalf("want 2 review calls (initial + follow-up), got %d", len(idx))
	}

	// Both calls offer the tool and force it; neither asks for response_format.
	for _, i := range idx {
		req := c.requestAt(i)
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != toolNameFindings {
			t.Fatalf("review call %d did not offer the findings tool: %+v", i, req.Tools)
		}
		if req.ToolChoice == nil {
			t.Fatalf("review call %d did not force the tool", i)
		}
		if len(req.ResponseSchema) != 0 {
			t.Fatalf("review call %d also sent response_format", i)
		}
		if !strings.Contains(req.System, "MUST call") {
			t.Fatalf("review call %d system prompt does not require the tool call", i)
		}
	}

	first := c.requestAt(idx[0])
	if len(first.History) != 0 {
		t.Fatalf("first attempt must be a fresh conversation, got %d history turns", len(first.History))
	}
	second := c.requestAt(idx[1])
	if len(second.History) != 3 {
		t.Fatalf("follow-up must carry assistant+tool+user turns, got %d: %+v", len(second.History), second.History)
	}
	if second.History[0].Role != "assistant" || len(second.History[0].ToolCalls) != 1 {
		t.Fatalf("follow-up must quote the rejected tool call: %+v", second.History[0])
	}
	if second.History[1].Role != "tool" || second.History[1].ToolCallID != "call_1" {
		t.Fatalf("follow-up needs a tool result bound to the call: %+v", second.History[1])
	}
	if !strings.Contains(second.History[1].Content, "rejected") {
		t.Fatalf("tool result must name the rejection: %q", second.History[1].Content)
	}
	if second.History[2].Role != "user" || !strings.Contains(second.History[2].Content, toolReaskPrompt) {
		t.Fatalf("follow-up must ask for a proper call: %+v", second.History[2])
	}

	// The file still reviews: the follow-up succeeded.
	if rec.Coverage.Reviewed != 1 || !rec.Coverage.Complete {
		t.Fatalf("want the file reviewed, got %+v", rec.Coverage)
	}
}

func TestToolsModeFollowsUpWhenNoToolCallWasMade(t *testing.T) {
	c := &scriptClient{resps: []*model.CompletionResponse{
		toolCallResponse(toolNameTriage, triageJSON("a.go")),
		{Text: "I reviewed it and it looks fine."}, // no tool call at all
		toolCallResponse(toolNameFindings, reviewJSON("a.go", "reviewed", nil)),
	}}
	_, err := runOnce(t, baseInputs(), Options{
		Cfg: config.Default(), Client: c,
		StructuredOutput: model.StructuredOutputTools,
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := reviewCalls(c)
	if len(idx) != 2 {
		t.Fatalf("want a follow-up after a missing tool call, got %d review calls", len(idx))
	}
	second := c.requestAt(idx[1])
	if len(second.History) != 2 {
		t.Fatalf("a missing tool call needs assistant+user turns, got %d: %+v", len(second.History), second.History)
	}
	if second.History[0].Role != "assistant" || second.History[0].Content == "" {
		t.Fatalf("follow-up must quote the assistant's prose answer: %+v", second.History[0])
	}
	if second.History[1].Role != "user" || !strings.Contains(second.History[1].Content, toolReaskPrompt) {
		t.Fatalf("follow-up must ask for a proper call: %+v", second.History[1])
	}
}

func TestToolsModeTriageFollowsUpOnceThenFallsBack(t *testing.T) {
	// A rejected triage call is answerable exactly once; triage is the cheap
	// pass, and a model that misses the tool twice must not keep the run
	// waiting while the batched fallback could already be reviewing.
	c := &triageFailClient{}
	rec, err := runOnce(t, baseInputs(), Options{
		Cfg: config.Default(), Client: c,
		StructuredOutput: model.StructuredOutputTools,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.triageCallCount(); got != 2 {
		t.Fatalf("want 1 initial + 1 follow-up triage call, got %d", got)
	}
	if rec.Coverage.Reviewed != 1 || !rec.Coverage.Complete {
		t.Fatalf("batched fallback must still review every file, got %+v", rec.Coverage)
	}
}

type triageFailClient struct {
	mu    sync.Mutex
	calls int
}

func (c *triageFailClient) Complete(_ context.Context, req model.CompletionRequest) (*model.CompletionResponse, error) {
	if len(req.Tools) > 0 && req.Tools[0].Function.Name == toolNameTriage {
		c.mu.Lock()
		c.calls++
		c.mu.Unlock()
		return toolCallResponse(toolNameTriage, `not-json`), nil
	}
	return &model.CompletionResponse{Text: reviewJSON(requestPath(req), "reviewed", nil)}, nil
}

func (c *triageFailClient) ModelID() string { return "triage-fail" }

func (c *triageFailClient) triageCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestReasoningEffortReachesTheReviewerRequest(t *testing.T) {
	// The reviewer must pass its Options value into every call it makes, or
	// the action input would be silently dropped in review mode.
	c := &scriptClient{resps: []*model.CompletionResponse{
		{Text: triageJSON("a.go")},
		{Text: reviewJSON("a.go", "reviewed", nil)},
	}}
	if _, err := runOnce(t, baseInputs(), Options{
		Cfg: config.Default(), Client: c, ReasoningEffort: "none",
	}); err != nil {
		t.Fatal(err)
	}
	for i := range c.reqs {
		if req := c.requestAt(i); req.ReasoningEffort != "none" {
			t.Fatalf("call %d lost the reasoning effort: %q", i, req.ReasoningEffort)
		}
	}
}

func TestRequireParametersOptionReachesRequests(t *testing.T) {
	// The action's require_parameters input must reach every call, and must OR
	// with the config key rather than replacing it, so an installation that
	// sets either one keeps the field.
	c := &scriptClient{resps: []*model.CompletionResponse{
		{Text: triageJSON("a.go")},
		{Text: reviewJSON("a.go", "reviewed", nil)},
	}}
	if _, err := runOnce(t, baseInputs(), Options{
		Cfg: config.Default(), Client: c, RequireParameters: true,
	}); err != nil {
		t.Fatal(err)
	}
	for i := range c.reqs {
		if req := c.requestAt(i); !req.RequireParameters {
			t.Fatalf("call %d lost require_parameters", i)
		}
	}

	// Neither source set: the field stays off for a direct provider that
	// rejects unknown request arguments.
	plain := &scriptClient{resps: []*model.CompletionResponse{
		{Text: triageJSON("a.go")},
		{Text: reviewJSON("a.go", "reviewed", nil)},
	}}
	if _, err := runOnce(t, baseInputs(), Options{Cfg: config.Default(), Client: plain}); err != nil {
		t.Fatal(err)
	}
	for i := range plain.reqs {
		if req := plain.requestAt(i); req.RequireParameters {
			t.Fatalf("call %d sent require_parameters with neither source set", i)
		}
	}
}

func TestResponseFormatModeIsUnchanged(t *testing.T) {
	// The default mode must keep sending the schema the old way and never
	// offer a tool, so existing installs are byte-for-byte unchanged.
	c := &scriptClient{resps: []*model.CompletionResponse{
		{Text: triageJSON("a.go")},
		{Text: reviewJSON("a.go", "reviewed", nil)},
	}}
	if _, err := runOnce(t, baseInputs(), Options{Cfg: config.Default(), Client: c}); err != nil {
		t.Fatal(err)
	}
	for i := range c.reqs {
		req := c.requestAt(i)
		if len(req.Tools) != 0 {
			t.Fatalf("call %d offered a tool in response_format mode", i)
		}
		if len(req.ResponseSchema) == 0 {
			t.Fatalf("call %d dropped the response schema", i)
		}
	}
}
