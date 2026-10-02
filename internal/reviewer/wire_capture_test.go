package reviewer

// The wire capture, end to end: the reviewer, a real client and a provider
// that answers with a response the review schema rejects. The capture must
// exist for that call, must hold the bytes that were refused, and must have
// changed nothing about how the reviewer decided: the file still ends
// errored(parse_failure) after the same bounded re-asks it spends with the
// capture off (issue #73).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// wireProvider is a fake provider that answers the triage call and the review
// call differently, the way a real one does: the two calls differ by their
// system message.
func wireProvider(t *testing.T, review func(n int) string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	reviewCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if systemOf(body) == triageSystemPrompt {
			w.Write([]byte(wireEnvelope(triageJSON("a.go"))))
			return
		}
		mu.Lock()
		reviewCalls++
		n := reviewCalls
		mu.Unlock()
		w.Write([]byte(wireEnvelope(review(n))))
	}))
	t.Cleanup(srv.Close)
	return srv, &reviewCalls
}

// wireEnvelope wraps model text as the chat-completion body a provider
// returns.
func wireEnvelope(text string) string {
	b, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// systemOf reads the system message of a request body, so the fake provider
// can tell the triage call from the review call.
func systemOf(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			return m.Content
		}
	}
	return ""
}

// readWireCapture decodes one capture document. A capture that will not
// decode is the failure this feature exists to prevent, so every assertion
// goes through the decoder.
func readWireCapture(t *testing.T, dir string, index int) model.CaptureCall {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("call-%04d.json", index))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc model.CaptureCall
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s is not decodable: %v", path, err)
	}
	return doc
}

// readWireManifest decodes the capture index.
func readWireManifest(t *testing.T, dir string) model.CaptureManifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var m model.CaptureManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not decodable: %v", err)
	}
	return m
}

// captureIn installs a capture for one reviewer test and clears it after.
func captureIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	model.SetActiveCapture(model.NewCapture(dir, 0))
	t.Cleanup(func() { model.SetActiveCapture(nil) })
	return dir
}

// The regression this feature was written for: a provider answers every
// review call with a response carrying schema_version 0, which the review
// schema rejects. Every one of those calls must leave a capture behind, each
// must say it was rejected and why, and the run must end exactly as it did
// before the capture existed: one file, errored(parse_failure), after the
// same bounded re-asks.
func TestWireCaptureRecordsSchemaViolatingResponse(t *testing.T) {
	// The response a provider returns when it ignores response_format and
	// answers with its own idea of the schema.
	bad := `{"schema_version":0,"path":"a.go","outcome":"reviewed","findings":[]}`
	srv, reviewCalls := wireProvider(t, func(int) string { return bad })

	dir := captureIn(t)
	client := &model.OpenAICompatClient{BaseURL: srv.URL, APIKey: "sk-test-000000", Model: "test-model", HTTP: srv.Client()}
	rec, err := runOnce(t, baseInputs(), baseOptions(client))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Semantics first: the capture is a passive observer, so the outcome is
	// the one the reviewer produced before this feature existed.
	if want := 1 + defaultPerFileParseRetries; *reviewCalls != want {
		t.Errorf("review calls = %d, want %d (the per-file budget is unchanged)", *reviewCalls, want)
	}
	fo := fileOutcomeOf(rec, "a.go")
	if fo == nil || fo.State != model.FileErrored || fo.Reason != "parse_failure" {
		t.Fatalf("file outcome = %+v, want errored(parse_failure)", fo)
	}
	if fo.Reasks != defaultPerFileParseRetries {
		t.Errorf("Reasks = %d, want %d", fo.Reasks, defaultPerFileParseRetries)
	}
	if rec.Coverage.Complete {
		t.Error("coverage stayed complete; a file that was never evaluated cannot be complete")
	}

	// Every call is captured, the manifest indexes all of them, and the last
	// one carries the rejection and the bytes that earned it.
	manifest := readWireManifest(t, dir)
	if len(manifest.Calls) != *reviewCalls+1 { // the triage call is the first
		t.Fatalf("manifest rows = %d, want %d", len(manifest.Calls), *reviewCalls+1)
	}
	for i := 1; i <= *reviewCalls; i++ {
		doc := readWireCapture(t, dir, i+1) // call 1 is the triage call
		if doc.Outcome != model.CaptureOutcomeParseFailure {
			t.Errorf("review call %d: outcome = %q, want %q", i, doc.Outcome, model.CaptureOutcomeParseFailure)
		}
		if !strings.Contains(doc.RejectedReason, "schema: version 0, want 1") {
			t.Errorf("review call %d: rejected_reason = %q, want the schema error", i, doc.RejectedReason)
		}
		if doc.Response == nil || !strings.Contains(doc.Response.Text, `schema_version\":0`) {
			t.Errorf("review call %d: the rejected response is not in the capture: %+v", i, doc.Response)
		}
		if !strings.Contains(doc.Request.Text, "file_under_review") {
			t.Errorf("review call %d: the capture holds no prompt", i)
		}
		if doc.Model != "test-model" || doc.ProviderHost == "" {
			t.Errorf("review call %d: model = %q, provider_host = %q", i, doc.Model, doc.ProviderHost)
		}
	}
	if got := manifest.Calls[len(manifest.Calls)-1]; got.Outcome != model.CaptureOutcomeParseFailure {
		t.Errorf("the last manifest row = %q, want %q", got.Outcome, model.CaptureOutcomeParseFailure)
	}
}

// The key is a header and never enters a body, so no capture of a real run
// can hold it. This proves it on the real request path rather than by
// inspection: the provider is sent the Authorization header, and the capture
// keeps the name with the value masked.
func TestWireCaptureNeverHoldsTheKey(t *testing.T) {
	const apiKey = "sk-test-0000000000000000"
	// The handler runs on the server's goroutine, so it records what it saw
	// and the test goroutine asserts it. Calling t.Errorf from there would
	// report a failure that the test can already have finished past.
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if systemOf(body) == triageSystemPrompt {
			w.Write([]byte(wireEnvelope(triageJSON("a.go"))))
			return
		}
		w.Write([]byte(wireEnvelope(reviewJSON("a.go", "reviewed", nil))))
	}))
	defer srv.Close()

	dir := captureIn(t)
	client := &model.OpenAICompatClient{BaseURL: srv.URL, APIKey: apiKey, Model: "test-model", HTTP: srv.Client()}
	if _, err := runOnce(t, baseInputs(), baseOptions(client)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("the provider received no call")
	}
	for _, header := range got {
		if header != "Bearer "+apiKey {
			t.Errorf("the provider saw Authorization = %q, want the key", header)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading capture directory: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("capture directory holds %d files; calls were made", len(entries))
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(b), apiKey) {
			t.Fatalf("%s holds the provider key:\n%s", e.Name(), b)
		}
		if e.Name() == "call-0001.json" && !strings.Contains(string(b), model.Redacted) {
			t.Errorf("the Authorization header was dropped rather than masked:\n%s", b)
		}
	}
}
