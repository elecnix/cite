package model

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestParseStructuredOutputMode(t *testing.T) {
	cases := map[string]StructuredOutputMode{
		"":                StructuredOutputResponseFormat,
		"response_format": StructuredOutputResponseFormat,
		"tools":           StructuredOutputTools,
	}
	for in, want := range cases {
		got, err := ParseStructuredOutputMode(in)
		if err != nil || got != want {
			t.Fatalf("ParseStructuredOutputMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// A typo must fail loudly: silently defaulting hides that the mode the
	// operator asked for is not the mode in force.
	if _, err := ParseStructuredOutputMode("nonsense"); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
}

func TestToolsModeSendsToolAndExtractsArguments(t *testing.T) {
	var gotBody map[string]any
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"report_findings","arguments":"{\"findings\":[]}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	resp, err := c.Complete(context.Background(), CompletionRequest{
		System: "s", User: "u", Temperature: 0, MaxOutputTokens: 64,
		Tools:      []ToolDef{NewToolDef("report_findings", "desc", json.RawMessage(`{"type":"object"}`))},
		ToolChoice: map[string]any{"type": "function", "function": map[string]string{"name": "report_findings"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["response_format"]; ok {
		t.Fatal("tools mode must not also send response_format")
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools not sent: %v", gotBody["tools"])
	}
	if _, ok := gotBody["tool_choice"]; !ok {
		t.Fatal("tool_choice not sent; the tool would not be forced")
	}
	if resp.Text != `{"findings":[]}` {
		t.Fatalf("Text = %q, want the tool arguments", resp.Text)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls not surfaced: %+v", resp.ToolCalls)
	}
}

func TestToolsModeIgnoresAnUnrelatedToolCall(t *testing.T) {
	// A call to a function Cite did not offer is not the schema's answer;
	// treating its arguments as the review would launder arbitrary JSON.
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"prose","tool_calls":[{"id":"c","type":"function","function":{"name":"other","arguments":"{\"nope\":true}"}}]},"finish_reason":"stop"}]}`))
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	resp, err := c.Complete(context.Background(), CompletionRequest{
		System: "s", User: "u", Temperature: 0, MaxOutputTokens: 8,
		Tools: []ToolDef{NewToolDef("report_findings", "desc", json.RawMessage(`{}`))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "prose" {
		t.Fatalf("Text = %q, want the content when the offered tool was not called", resp.Text)
	}
}

func TestToolsModeUsesContentWhenNoToolCall(t *testing.T) {
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"prose answer"},"finish_reason":"stop"}],"usage":{}}`))
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	resp, err := c.Complete(context.Background(), CompletionRequest{
		System: "s", User: "u", Temperature: 0, MaxOutputTokens: 8,
		Tools: []ToolDef{NewToolDef("report_findings", "desc", json.RawMessage(`{}`))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "prose answer" {
		t.Fatalf("Text = %q, want the prose content so the caller can reject and follow up", resp.Text)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
}

func TestHistoryAppendedToMessages(t *testing.T) {
	var msgs []map[string]any
	srv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		msgs = body.Messages
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	})
	ts := newTestServer(srv)
	defer ts.Close()
	c := &OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	_, err := c.Complete(context.Background(), CompletionRequest{
		System: "sys", User: "ask", MaxOutputTokens: 8,
		Tools: []ToolDef{NewToolDef("report_findings", "desc", json.RawMessage(`{}`))},
		History: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function"}}},
			{Role: "tool", ToolCallID: "call_1", Content: "rejected: bad schema"},
			{Role: "user", Content: "Please call the tool properly."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("want 2 base + 3 history messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[2]["role"] != "assistant" {
		t.Fatalf("history[0] role = %v", msgs[2]["role"])
	}
	if _, ok := msgs[2]["tool_calls"]; !ok {
		t.Fatal("assistant history turn must carry tool_calls")
	}
	if msgs[3]["tool_call_id"] != "call_1" {
		t.Fatalf("tool turn lost tool_call_id: %+v", msgs[3])
	}
	if !strings.Contains(msgs[4]["content"].(string), "Please call the tool properly") {
		t.Fatalf("user follow-up missing: %+v", msgs[4])
	}
}
