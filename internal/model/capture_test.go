package model

// The wire capture: an opt-in, passive record of what went to the provider
// and what came back before anything parsed it. These tests hold it to the
// three properties that make it safe to switch on: a default run writes
// nothing, a written capture never holds a credential, and a response the
// review schema rejected is captured with the bytes that were refused.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// captureFile is the document of one call.
func captureFile(dir string, index int) string {
	return filepath.Join(dir, fmt.Sprintf("call-%04d.json", index))
}

// readCapture decodes one capture document. A capture that cannot be decoded
// is the one failure this feature exists to prevent, so every test that reads
// a document goes through here.
func readCapture(t *testing.T, dir string, index int) CaptureCall {
	t.Helper()
	b, err := os.ReadFile(captureFile(dir, index))
	if err != nil {
		t.Fatalf("reading capture: %v", err)
	}
	var doc CaptureCall
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("capture is not decodable: %v", err)
	}
	return doc
}

// readManifest reads the capture index.
func readManifest(t *testing.T, dir string) CaptureManifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var m CaptureManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not decodable: %v", err)
	}
	return m
}

// captureIn installs a capture for one test and clears it afterwards, so a
// test's capture can never leak into the next.
func captureIn(t *testing.T, maxBytes int, secrets ...string) *Capture {
	t.Helper()
	c := NewCapture(t.TempDir(), maxBytes, secrets...)
	SetActiveCapture(c)
	t.Cleanup(func() { SetActiveCapture(nil) })
	return c
}

// freshActive forgets the process-wide capture, so the next ActiveCapture
// call re-reads the environment exactly as a run that just started would.
func freshActive(t *testing.T) {
	t.Helper()
	activeMu.Lock()
	prev, prevLoaded := active, activeLoaded
	active, activeLoaded = nil, false
	activeMu.Unlock()
	t.Cleanup(func() {
		activeMu.Lock()
		active, activeLoaded = prev, prevLoaded
		activeMu.Unlock()
	})
}

// completionBody is a minimal well-formed chat completion carrying text.
func completionBody(text string) string {
	return `{"choices":[{"message":{"content":` + jsonString(text) + `},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7}}`
}

// holdsText reports whether a captured body carries text, which reaches it
// JSON-escaped as a string value inside the completion envelope.
func holdsText(t *testing.T, body, text string) bool {
	t.Helper()
	escaped, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("escaping %q: %v", text, err)
	}
	return strings.Contains(body, string(escaped[1:len(escaped)-1]))
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// testClient is a client pointed at a test server, with a key that is
// obviously fake.
func testClient(url string) *OpenAICompatClient {
	return &OpenAICompatClient{BaseURL: url, APIKey: "sk-test-000000", Model: "test-model"}
}

// A default run writes nothing at all: the capture is off, so a review on a
// repository that never opted in produces no file, no directory and no
// manifest. This is the property that lets the feature ship opt-in.
func TestCaptureOffWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	t.Setenv(CaptureEnvVar, "")
	t.Setenv(CaptureDirEnvVar, dir)
	freshActive(t)

	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(`{"schema_version":1}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("capture directory %s exists with the capture off (err=%v)", dir, err)
	}
}

// A capture holds the exact request that went on the wire and the exact raw
// bytes that came back, plus the status and the headers, before parsing. The
// request body a provider saw is the reviewer's prompt, its system message
// and its response-format payload included.
func TestCaptureHoldsRequestAndRawResponse(t *testing.T) {
	cap := captureIn(t, 0)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-42")
		w.Write([]byte(completionBody(`{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[]}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	schema := json.RawMessage(`{"type":"object"}`)
	resp, err := c.Complete(context.Background(), CompletionRequest{
		System: "you are the reviewer", User: `<file_under_review path="a.go">`,
		MaxOutputTokens: 4096, Temperature: 0, ResponseSchema: schema,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("Complete returned no text")
	}
	doc := readCapture(t, cap.Dir(), 1)
	if doc.Outcome != CaptureOutcomeOK {
		t.Errorf("outcome = %q, want %q", doc.Outcome, CaptureOutcomeOK)
	}
	if doc.ResponseStatus != http.StatusOK {
		t.Errorf("response_status = %d, want 200", doc.ResponseStatus)
	}
	if got := doc.ResponseHeaders["X-Request-Id"]; got != "req-42" {
		t.Errorf("response header X-Request-Id = %q, want req-42", got)
	}
	for _, want := range []string{"you are the reviewer", "a.go", "json_schema", `"max_tokens":4096`} {
		if !strings.Contains(doc.Request.Text, want) {
			t.Errorf("the request capture has no %q: %s", want, doc.Request.Text)
		}
	}
	if doc.MaxOutputTokens != 4096 {
		t.Errorf("max_output_tokens = %d, want 4096", doc.MaxOutputTokens)
	}
	// The captured body is the raw completion envelope, in which the
	// completion's own text sits JSON-escaped as a string value.
	if doc.Response == nil {
		t.Fatalf("the response capture has no body at all: %+v", doc)
	}
	if !strings.Contains(doc.Response.Text, `schema_version`) {
		t.Errorf("the response capture has no raw body: %+v", doc.Response)
	}
	if !holdsText(t, doc.Response.Text, resp.Text) {
		t.Errorf("the response capture is not the provider's own bytes: %+v", doc.Response)
	}
	if doc.Response.Bytes == 0 || doc.Response.SHA256 == "" {
		t.Errorf("the response capture has no size or hash: %+v", doc.Response)
	}
	if doc.Request.SHA256 == "" {
		t.Error("the request capture has no input hash")
	}
	if doc.ProviderHost == "" || doc.RequestPath != "/chat/completions" {
		t.Errorf("provider_host = %q, request_path = %q", doc.ProviderHost, doc.RequestPath)
	}
	if doc.Usage == nil || doc.Usage.InputTokens != 11 || doc.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want the provider's counters", doc.Usage)
	}
	m := readManifest(t, cap.Dir())
	if len(m.Calls) != 1 {
		t.Fatalf("manifest rows = %d, want 1", len(m.Calls))
	}
	row := m.Calls[0]
	if row.Model != "test-model" || row.Outcome != CaptureOutcomeOK || row.File != "call-0001.json" {
		t.Errorf("manifest row = %+v", row)
	}
	if row.RequestSHA256 != doc.Request.SHA256 || row.InputTokens != 11 || row.OutputTokens != 7 {
		t.Errorf("manifest row = %+v, want the input hash and the token counts", row)
	}
	if m.MaxBodyBytes != DefaultCaptureMaxBytes {
		t.Errorf("manifest max_body_bytes = %d, want %d", m.MaxBodyBytes, DefaultCaptureMaxBytes)
	}
}

// Request headers are masked by an ALLOWLIST, not by a list of credential
// names. Cite chooses every request header it sends, so the safe set is
// knowable: a listed name keeps its value, everything else keeps the name and
// loses the value. A header nobody here has heard of is masked by the rule
// rather than by luck.
func TestCaptureRequestHeadersAreAllowlisted(t *testing.T) {
	for _, name := range []string{
		// A header this repository has never sent, carrying a secret-shaped
		// value: the allowlist is what masks it, not a name heuristic.
		"X-Vendor-Session", "Authorization", "X-Api-Key", "Cookie",
		"X-Tenant-Trace-Id", "Proxy-Authorization",
	} {
		t.Run(name, func(t *testing.T) {
			cap := captureIn(t, 0)
			srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(completionBody(`{}`)))
			})
			defer srv.Close()
			c := testClient(srv.URL)
			c.HTTP = srv.Client()
			c.ExtraHeaders = map[string]string{name: "value-that-must-not-survive"}
			if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			doc := readCapture(t, cap.Dir(), 1)
			// Canonical, as http.Header spells it: the document records header
			// names in the form the wire carries them.
			if got := doc.RequestHeaders[http.CanonicalHeaderKey(name)]; got != Redacted {
				t.Errorf("request header %s = %q, want %q", name, got, Redacted)
			}
			raw, err := os.ReadFile(captureFile(cap.Dir(), 1))
			if err != nil {
				t.Fatalf("reading capture: %v", err)
			}
			if strings.Contains(string(raw), "value-that-must-not-survive") {
				t.Errorf("the header value reached the capture:\n%s", raw)
			}
		})
	}
}

// The allowlisted names keep their real values: a capture that could not show
// the content type or the sticky-routing session id would be a worse
// diagnostic than no capture at all.
func TestCaptureKeepsAllowlistedRequestHeaderValues(t *testing.T) {
	cap := captureIn(t, 0)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(`{}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	if _, err := c.Complete(context.Background(), CompletionRequest{
		System: "s", User: "u", MaxOutputTokens: 8, SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	doc := readCapture(t, cap.Dir(), 1)
	if got := doc.RequestHeaders["Content-Type"]; got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := doc.RequestHeaders["X-Session-Id"]; got != "sess-1" {
		t.Errorf("X-Session-Id = %q, want the session id", got)
	}
}

// The allowlist a capture was built with travels in the document, so a reader
// can see which values were kept by name rather than trusting the masker.
func TestCaptureRecordsTheRequestHeaderAllowlist(t *testing.T) {
	cap := captureIn(t, 0)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(`{}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	doc := readCapture(t, cap.Dir(), 1)
	if len(doc.Redaction.RequestHeaderAllowlist) == 0 {
		t.Fatal("the document does not record the request-header allowlist")
	}
	for _, want := range []string{"content-type", "x-session-id"} {
		found := false
		for _, a := range doc.Redaction.RequestHeaderAllowlist {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the allowlist does not name %q: %v", want, doc.Redaction.RequestHeaderAllowlist)
		}
	}
	if len(doc.Redaction.MaskedHeaderNames) == 0 {
		t.Error("the document does not record which response header names are masked")
	}
}

// A URL is recorded as scheme, host and path. The query string is dropped
// whole: a parameter name is not a reliable place to look for a credential,
// and one provider's `?key=` is the next provider's `?access_token=`.
func TestCaptureDropsTheURLQueryString(t *testing.T) {
	cap := captureIn(t, 0)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(`{}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL + "/v1?api_key=sk-test-000000&trace=1")
	c.HTTP = srv.Client()
	if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	doc := readCapture(t, cap.Dir(), 1)
	// The base URL ends in a query here, so the path is what precedes it and
	// the "chat/completions" suffix lands inside the query string. Either
	// way the capture keeps the path and nothing of the query.
	if doc.RequestPath != "/v1" {
		t.Errorf("request_path = %q, want the path without the query", doc.RequestPath)
	}
	raw, err := os.ReadFile(captureFile(cap.Dir(), 1))
	if err != nil {
		t.Fatalf("reading capture: %v", err)
	}
	for _, forbidden := range []string{"api_key=", "trace=1", "sk-test-000000"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the capture holds %q from the URL:\n%s", forbidden, raw)
		}
	}
}

// Response headers are masked the same way, and the status of a failed call
// is recorded with them.
func TestCaptureMasksResponseHeaders(t *testing.T) {
	for _, tc := range []struct{ header, value string }{
		// A response challenge is masked too: some gateways answer a
		// rejected key with a challenge that quotes it.
		{"WWW-Authenticate", `Bearer realm="api"`},
		{"Set-Cookie", "session=abc123"},
		{"X-Api-Key", "key-material"},
		{"X-Request-Id", "req-7"},
	} {
		t.Run(tc.header, func(t *testing.T) {
			cap := captureIn(t, 0)
			srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(tc.header, tc.value)
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":{"message":"denied"}}`))
			})
			defer srv.Close()
			c := testClient(srv.URL)
			c.HTTP = srv.Client()
			if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err == nil {
				t.Fatal("Complete: want an error for a non-200")
			}
			doc := readCapture(t, cap.Dir(), 1)
			// http.Header canonicalises names, so the document spells them
			// "Www-Authenticate" whatever the server wrote.
			name := http.CanonicalHeaderKey(tc.header)
			if doc.ResponseStatus != http.StatusUnauthorized {
				t.Errorf("response_status = %d, want 401", doc.ResponseStatus)
			}
			if doc.ResponseStatusText == "" {
				t.Error("response_status_text is empty; the capture must name the status line")
			}
			if doc.Outcome != CaptureOutcomeHTTPError {
				t.Errorf("outcome = %q, want %q", doc.Outcome, CaptureOutcomeHTTPError)
			}
			if doc.Response == nil || !strings.Contains(doc.Response.Text, "denied") {
				t.Errorf("the error body was not captured: %+v", doc.Response)
			}
			if name == "X-Request-Id" {
				if got := doc.ResponseHeaders[name]; got != tc.value {
					t.Errorf("a non-credential header was masked: %q", got)
				}
				return
			}
			if got := doc.ResponseHeaders[name]; got != Redacted {
				t.Errorf("response header %s = %q, want %q", name, got, Redacted)
			}
		})
	}
}

// Masking is by value as well as by header name. A provider that reflects
// the key into its own error page is the documented reason a response body
// is untrusted text, and a value-based pass is what closes it: a key that
// appears in a body or a URL is masked wherever it appears. The key used
// here is obviously fake.
func TestCaptureMasksKeyValueAnywhereItAppears(t *testing.T) {
	const key = "sk-test-0000000000000000"
	for _, tc := range []struct {
		name     string
		respBody string
	}{
		{name: "quoted in an error message", respBody: `{"error":{"message":"invalid api key ` + key + `"}}`},
		{name: "reflected as a bearer token", respBody: `{"error":{"message":"Authorization: Bearer ` + key + ` rejected"}}`},
		{name: "in an HTML gateway page", respBody: `<html><body>token ` + key + ` expired</body></html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := captureIn(t, 0, key)
			srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(tc.respBody))
			})
			defer srv.Close()
			c := testClient(srv.URL)
			c.APIKey = key
			c.HTTP = srv.Client()
			if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err == nil {
				t.Fatal("Complete: want an error")
			}
			raw, err := os.ReadFile(captureFile(cap.Dir(), 1))
			if err != nil {
				t.Fatalf("reading capture: %v", err)
			}
			if strings.Contains(string(raw), key) {
				t.Fatalf("the capture holds the provider key:\n%s", raw)
			}
			doc := readCapture(t, cap.Dir(), 1)
			if doc.Response == nil || !strings.Contains(doc.Response.Text, Redacted) {
				t.Errorf("the echoed key was not marked as masked: %+v", doc.Response)
			}
			if doc.Usage != nil {
				t.Error("a failed call has no usage to record")
			}
		})
	}
}

// A capture is capped, and the cap is always visible: truncated, the
// original size, and a note naming the setting that produced the cut. A
// silent truncation is a lie an operator cannot detect.
func TestCaptureCapsBodyAndSaysSo(t *testing.T) {
	cap := captureIn(t, captureMinMaxBytes)
	big := strings.Repeat("x", 4*captureMinMaxBytes)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(big)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	if _, err := c.Complete(context.Background(), CompletionRequest{
		System: strings.Repeat("s", 4*captureMinMaxBytes), User: "u", MaxOutputTokens: 8,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	doc := readCapture(t, cap.Dir(), 1)
	if !doc.Request.Truncated || doc.Request.OriginalBytes <= captureMinMaxBytes {
		t.Errorf("the request body was not truncated with a recorded original size: %+v", doc.Request)
	}
	if doc.Request.TruncationNote == "" || !strings.Contains(doc.Request.TruncationNote, CaptureMaxEnvVar) {
		t.Errorf("truncation_note = %q, want a note naming the setting", doc.Request.TruncationNote)
	}
	if doc.Response == nil || !doc.Response.Truncated {
		t.Errorf("the response body was not truncated: %+v", doc.Response)
	}
	row := readManifest(t, cap.Dir()).Calls[0]
	if !row.Truncated {
		t.Error("the manifest row does not record the truncation")
	}
	if row.RequestOmittedBytes <= 0 || row.ResponseOmittedBytes <= 0 {
		t.Errorf("the manifest row does not record the omitted bytes: %+v", row)
	}
}

// A response the review schema rejects is the case the capture exists for:
// the document holds the raw bytes that were refused and says the call was
// rejected. The rejection itself is the reviewer's business, and the capture
// observes it without changing anything.
func TestCaptureMarksSchemaViolationWithRawBytes(t *testing.T) {
	cap := captureIn(t, 0)
	// schema_version 0 against a parser that wants 1: the exact shape a
	// provider returns when it ignores the requested schema.
	raw := `{"schema_version":0,"path":"a.go","outcome":"reviewed","findings":[]}`
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(raw)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	resp, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	_, perr := ParseFileReview([]byte(resp.Text))
	if perr == nil {
		t.Fatal("the fixture must be a schema violation")
	}
	resp.MarkRejected(perr)

	doc := readCapture(t, cap.Dir(), 1)
	if doc.Outcome != CaptureOutcomeParseFailure {
		t.Errorf("outcome = %q, want %q", doc.Outcome, CaptureOutcomeParseFailure)
	}
	if !strings.Contains(doc.RejectedReason, "schema: version 0, want 1") {
		t.Errorf("rejected_reason = %q, want the parse error", doc.RejectedReason)
	}
	// The exact text the parser refused is inside the captured body. The
	// captured body is the whole completion envelope, in which that text
	// sits JSON-escaped as a string value, so the comparison is against the
	// escaped form of the same bytes.
	if doc.Response == nil || !holdsText(t, doc.Response.Text, resp.Text) {
		t.Errorf("the rejected bytes are not in the capture: %+v", doc.Response)
	}
	if got := readManifest(t, cap.Dir()).Calls[0].Outcome; got != CaptureOutcomeParseFailure {
		t.Errorf("manifest outcome = %q, want %q after the rejection", got, CaptureOutcomeParseFailure)
	}
	// MarkRejected is an observer: the caller still holds what it had, which
	// is what keeps the retry budget and the verdict untouched.
	if resp.Text != raw {
		t.Errorf("MarkRejected changed the response: %q", resp.Text)
	}
}

// MarkRejected on a response from a client with no capture is a no-op, not a
// panic: the reviewer calls it on every parse failure and most runs have the
// capture off.
func TestMarkRejectedWithoutCaptureIsNoOp(t *testing.T) {
	SetActiveCapture(nil)
	t.Cleanup(func() { SetActiveCapture(nil) })
	resp := &CompletionResponse{Text: "{}"}
	resp.MarkRejected(errors.New("schema: version 0, want 1"))
}

// Every call of a run gets its own document, numbered in call order, and the
// manifest carries one row each: a review runs several calls at once, and a
// directory an operator cannot index is a directory they open one file at a
// time.
func TestCaptureOneDocumentPerCall(t *testing.T) {
	cap := captureIn(t, 0)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(`{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[]}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	for i := 0; i < 3; i++ {
		if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(cap.Dir())
	if err != nil {
		t.Fatalf("reading capture directory: %v", err)
	}
	if len(entries) != 4 { // three documents and the manifest
		t.Errorf("capture directory holds %d files, want 4", len(entries))
	}
	m := readManifest(t, cap.Dir())
	if len(m.Calls) != 3 {
		t.Fatalf("manifest rows = %d, want 3", len(m.Calls))
	}
	for i, row := range m.Calls {
		if row.CallIndex != i+1 || row.File != fmt.Sprintf("call-%04d.json", i+1) {
			t.Errorf("row %d = %+v", i, row)
		}
	}
}

// The opt-in itself: only the true spellings enable the capture, and the
// ceiling is the default unless the environment names one. A typo must leave
// the feature off rather than start writing prompts to disk.
func TestCaptureFromEnv(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		env       string
		wantOn    bool
		maxEnv    string
		wantBytes int
	}{
		{env: "", wantOn: false, wantBytes: DefaultCaptureMaxBytes},
		{env: "false", wantOn: false, wantBytes: DefaultCaptureMaxBytes},
		{env: "yes-please", wantOn: false, wantBytes: DefaultCaptureMaxBytes},
		{env: "1", wantOn: true, wantBytes: DefaultCaptureMaxBytes},
		{env: "true", wantOn: true, wantBytes: DefaultCaptureMaxBytes},
		{env: "True", wantOn: true, wantBytes: DefaultCaptureMaxBytes},
		{env: "true", maxEnv: "4096", wantOn: true, wantBytes: 4096},
		{env: "true", maxEnv: "16", wantOn: true, wantBytes: captureMinMaxBytes},
		// Above the ceiling: the message says the ceiling, so the ceiling is
		// what the capture uses. A log that under-reports the artifact's
		// size is worse than no log.
		{env: "true", maxEnv: "1000000000", wantOn: true, wantBytes: captureMaxBytesCeiling},
		{env: "true", maxEnv: "not-a-number", wantOn: true, wantBytes: DefaultCaptureMaxBytes},
		{env: "true", maxEnv: "0", wantOn: true, wantBytes: DefaultCaptureMaxBytes},
	} {
		t.Run("CITE_CAPTURE_WIRE="+tc.env+"/"+tc.maxEnv, func(t *testing.T) {
			t.Setenv(CaptureEnvVar, tc.env)
			t.Setenv(CaptureDirEnvVar, dir)
			t.Setenv(CaptureMaxEnvVar, tc.maxEnv)
			got := captureFromEnv()
			if tc.wantOn != (got != nil) {
				t.Fatalf("capture on = %t, want %t", got != nil, tc.wantOn)
			}
			if got != nil {
				if got.MaxBytes() != tc.wantBytes {
					t.Errorf("ceiling = %d, want %d", got.MaxBytes(), tc.wantBytes)
				}
				if got.Dir() != dir {
					t.Errorf("dir = %q, want %q", got.Dir(), dir)
				}
			}
		})
	}
}

// Nothing from a capture is printed. A capture holds the prompt, and the
// prompt holds the diff, so a capture is read from the files the artifact
// carries and is never echoed, catted or logged. The one line Cite prints
// about the capture names the directory and the ceiling, and that is all.
func TestCaptureNeverPrintsItsContents(t *testing.T) {
	const marker = "a-source-line-that-must-not-be-logged"
	dir := t.TempDir()
	t.Setenv(CaptureEnvVar, "true")
	t.Setenv(CaptureDirEnvVar, dir)
	t.Setenv(CaptureMaxEnvVar, "")
	freshActive(t)

	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(marker)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()

	stdout, stderr := captureOutput(t, func() {
		if _, err := c.Complete(context.Background(), CompletionRequest{
			System: "s", User: marker, MaxOutputTokens: 8,
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	})
	for name, out := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if strings.Contains(out, marker) {
			t.Errorf("%s carries capture content:\n%s", name, out)
		}
	}
	if !strings.Contains(stderr, dir) {
		t.Errorf("stderr does not say where the capture is being written:\n%s", stderr)
	}
}

// captureOutput runs fn with both standard streams redirected and returns
// what each one received.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	read := func(r *os.File) chan string {
		ch := make(chan string, 1)
		go func() {
			b, _ := io.ReadAll(r)
			ch <- string(b)
		}()
		return ch
	}
	outCh, errCh := read(outR), read(errR)
	fn()
	os.Stdout, os.Stderr = origOut, origErr
	_ = outW.Close()
	_ = errW.Close()
	return <-outCh, <-errCh
}

// A cut body must lose at most the trailing partial rune. A body that also
// holds an invalid byte in the middle keeps everything after that byte: json
// encoding replaces the invalid byte with U+FFFD, so the document still
// decodes, and trimming back to the first invalid byte would discard the rest
// of the body the capture exists to preserve.
func TestCaptureCutKeepsEverythingAfterAnInvalidByte(t *testing.T) {
	cap := captureIn(t, captureMinMaxBytes)
	// Valid text, then an invalid byte a third of the way in, then more
	// valid text, then enough filler to pass the ceiling.
	body := []byte(strings.Repeat("a", captureMinMaxBytes/3))
	body = append(body, 0xff)
	body = append(body, []byte(strings.Repeat("b", 4*captureMinMaxBytes))...)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()
	if _, err := c.Complete(context.Background(), CompletionRequest{System: "s", User: "u", MaxOutputTokens: 8}); err == nil {
		t.Fatal("Complete: want an error for a body that is not a completion")
	}
	doc := readCapture(t, cap.Dir(), 1)
	if doc.Response == nil || !doc.Response.Truncated {
		t.Fatalf("the response body was not truncated: %+v", doc.Response)
	}
	// The document decoded, which is the point of the loop, and it kept
	// everything up to the ceiling minus at most one rune.
	if n := len(doc.Response.Text); n < captureMinMaxBytes-utf8.UTFMax {
		t.Errorf("kept %d bytes, want at least %d: the cut dropped more than a partial rune",
			n, captureMinMaxBytes-utf8.UTFMax)
	}
	if doc.Response.OriginalBytes != len(body) {
		t.Errorf("original_bytes = %d, want %d", doc.Response.OriginalBytes, len(body))
	}
}

// A review runs several calls at once, and the manifest is one file. Every
// call that lands must appear in it: an index that lost a row would hide a
// call whose document sits right there on disk.
func TestCaptureManifestSurvivesConcurrentCalls(t *testing.T) {
	cap := captureIn(t, 0)
	srv := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(completionBody(`{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[]}`)))
	})
	defer srv.Close()
	c := testClient(srv.URL)
	c.HTTP = srv.Client()

	const calls = 12
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Complete(context.Background(), CompletionRequest{
				System: "s", User: "u", MaxOutputTokens: 8,
			}); err != nil {
				t.Errorf("Complete: %v", err)
			}
		}()
	}
	wg.Wait()

	m := readManifest(t, cap.Dir())
	if len(m.Calls) != calls {
		rows := make([]int, 0, len(m.Calls))
		for _, row := range m.Calls {
			rows = append(rows, row.CallIndex)
		}
		t.Errorf("manifest rows = %d (%v), want %d: a concurrent write dropped a row", len(m.Calls), rows, calls)
	}
	for i := 1; i <= calls; i++ {
		if _, err := os.Stat(captureFile(cap.Dir(), i)); err != nil {
			t.Errorf("call %d has no document: %v", i, err)
		}
	}
}
