package model

// Wire capture: the exact request sent to a provider and the exact raw
// response received, before anything parses them, written to disk only when
// an operator opts in.
//
// Why it exists: when a model answers something the review schema rejects
// ("schema: version 0, want 1"), the forensics artifact says what the run
// concluded and nothing about what was exchanged. An operator cannot
// distinguish a provider that ignores response_format, a router that returns
// a different schema, a prompt that did not fit, and a provider that
// silently substituted prose for JSON. The bytes answer it; the verdict
// never changes because of them.
//
// What it is not: a debug switch on the request path. Capture never
// rewrites a body, never retries, never fails a run, and is a no-op unless
// CITE_CAPTURE_WIRE asks for it. With the capture off, nothing below runs.
//
// The capture necessarily contains the prompt, and the prompt contains the
// diff under review: this is the reviewer's whole input. A capture is
// private review material, which is why the action keeps it behind an
// explicit opt-in and a short retention.
//
// Redaction is allowlist-first. The request direction is entirely Cite's own
// doing, so the header values a capture keeps are a short known list and
// every other request header keeps its name and loses its value. The response
// direction is written by the provider and is the diagnosis, so it is masked
// by credential name and then by value. Under both sits a value-based pass
// over bodies, free text and headers, because the API key is an HTTP header
// that never enters a request body (§12, I4) and the only way it could reach
// a capture is a gateway reflecting it into its own error page. A URL is
// recorded as host and path; the query string is dropped whole.
//
// Nothing here is ever printed. A capture holds the prompt, so it is read
// from the files the caller uploads and never echoed into a log.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// CaptureSchemaVersion versions the capture documents and the manifest.
	// It is independent of SchemaVersion, which versions a review response:
	// a capture describes the wire, and the wire did not change when the
	// review schema did.
	CaptureSchemaVersion = 1

	// DefaultCaptureMaxBytes caps each captured body. A review prompt runs
	// to hundreds of kilobytes on a large diff, and a capture is uploaded as
	// an artifact on every run of a repository that opted in. 256 KiB holds
	// the whole exchange for an ordinary file review and cuts a pathological
	// one, always with a visible marker (never a silent truncation).
	DefaultCaptureMaxBytes = 256 << 10

	// captureMaxBytesCeiling bounds the operator's own value: a capture is a
	// diagnostic artifact, not a backup of the model provider's traffic.
	captureMaxBytesCeiling = 64 << 20
	// captureMinMaxBytes keeps a typo from producing captures that contain
	// almost nothing.
	captureMinMaxBytes = 1 << 10

	// captureManifestName is the manifest beside the per-call documents: one
	// row per call with the input hash, the model, the provider host, the
	// token counts and the outcome, so a directory can be triaged without
	// opening every document.
	captureManifestName = "manifest.json"
)

// Capture outcomes. The vocabulary is three values on purpose: a capture
// exists to answer "did this call produce a usable review", and those are
// the three answers. A call that never got an HTTP response at all is
// http-error with an outcome_detail that says so, because "no response" is
// not a fourth kind of call, it is a failed one.
const (
	// CaptureOutcomeOK: the provider answered 200 and the body decoded.
	CaptureOutcomeOK = "ok"
	// CaptureOutcomeParseFailure: the body decoded, and the reviewer then
	// rejected it (schema violation, syntax error, blank body, path echo).
	CaptureOutcomeParseFailure = "parse-failure"
	// CaptureOutcomeHTTPError: the call did not produce a usable completion.
	// A non-200, a transport failure, a malformed body, a token-cap truncation
	// and a deadline expiry all land here.
	CaptureOutcomeHTTPError = "http-error"
)

// Redacted is the placeholder that replaces a masked value. It is a literal
// an operator can grep for, so "is this masked" is answerable by reading the
// capture rather than by trusting the masker.
const Redacted = "[redacted]"

// CaptureEnvVar and friends are the environment surface of the feature. The
// GitHub Action's capture_wire input sets the first and the directory is
// pointed at $RUNNER_TEMP so the artifact step can find it.
const (
	CaptureEnvVar    = "CITE_CAPTURE_WIRE"
	CaptureDirEnvVar = "CITE_CAPTURE_DIR"
	CaptureMaxEnvVar = "CITE_CAPTURE_MAX_BYTES"
)

// capturedRequestHeaders is an ALLOWLIST of the request header values a
// capture keeps, and it is an allowlist on purpose.
//
// Cite chooses every request header it sends, so the safe set is knowable in
// advance and short. A blocklist of credential names is a guess about what a
// provider will send, and providers invent headers: one this repository has
// never seen would ride straight into a shared artifact. Anything not named
// here keeps its NAME (an operator needs to see that a header was sent, and
// which one) and loses its value.
//
// The response direction cannot afford an allowlist: its headers are the
// diagnosis (request ids, rate-limit counters, the upstream model), they are
// written by a party Cite does not control, and dropping every unknown name
// would leave an operator with a status line and nothing. It is masked by the
// credential-name list below and, as a second layer, by value.
var capturedRequestHeaders = []string{
	"content-type",
	"content-length",
	"accept",
	"user-agent",
	// Cite's own sticky-routing key: a random id, generated per run, useful
	// for tying a capture to a cache line in the run record.
	"x-session-id",
}

// maskedHeaderNames is the list of header names whose value is replaced
// wholesale, before any content matching. A header is a named channel, so
// the name alone is enough to mask it: an operator reading a capture wants
// to see that a credential was sent, never what it was. The list is a floor
// under the heuristic below, not a ceiling over it.
var maskedHeaderNames = []string{
	"authorization",
	"proxy-authorization",
	"cookie",
	"set-cookie",
	"api-key",
	"x-api-key",
	"x-goog-api-key",
	"x-auth-token",
	"x-access-token",
	"x-session-key",
	"openai-organization-key",
}

// credentialHeaderFragments catch the names this codebase has never sent:
// any header whose name reads like a credential is masked even when it is
// unknown here, because a capture outlives the code that wrote it.
var credentialHeaderFragments = []string{
	"authorization", "authenticate", "api-key", "apikey", "token", "secret",
	"cookie", "password", "credential", "session-key",
}

// bearerRe masks an inline credential in a body or a URL. A gateway error
// page that reflects the request's Authorization header is the documented
// reason a response body is untrusted text (bodyShape), and a reflected
// bearer token does not equal a known secret value.
var bearerRe = regexp.MustCompile(`(?i)\b(bearer|basic|token|apikey)\s+([A-Za-z0-9._~+/=-]{8,})`)

// minMaskableSecret is the shortest value worth searching a body for.
// Masking a three-character key would corrupt the reviewed source it sits
// in, and a capture is worth nothing if the prompt is mangled beyond use.
const minMaskableSecret = 8

// CapturedBody is one body as it went on or came off the wire, masked and
// capped. Bytes is the size on the wire; Text is what this file holds, which
// can be shorter (a masked value) or shorter still (a capped body).
type CapturedBody struct {
	SHA256         string `json:"sha256"`
	Bytes          int    `json:"bytes"`
	Text           string `json:"text"`
	Truncated      bool   `json:"truncated,omitempty"`
	OriginalBytes  int    `json:"original_bytes,omitempty"`
	TruncationNote string `json:"truncation_note,omitempty"`
}

// CapturedHeaders are the headers of one direction of a call, with every
// credential-bearing value replaced by Redacted.
type CapturedHeaders map[string]string

// CaptureCall is one model call: the exact request, the exact raw response
// before parsing, and how the call ended.
type CaptureCall struct {
	SchemaVersion      int              `json:"schema_version"`
	CallIndex          int              `json:"call_index"`
	Outcome            string           `json:"outcome"`
	OutcomeDetail      string           `json:"outcome_detail,omitempty"`
	RejectedReason     string           `json:"rejected_reason,omitempty"`
	Model              string           `json:"model"`
	ProviderHost       string           `json:"provider_host"`
	RequestMethod      string           `json:"request_method"`
	RequestPath        string           `json:"request_path"`
	Temperature        float64          `json:"temperature"`
	MaxOutputTokens    int              `json:"max_output_tokens"`
	Request            CapturedBody     `json:"request"`
	RequestHeaders     CapturedHeaders  `json:"request_headers"`
	ResponseStatus     int              `json:"response_status,omitempty"`
	ResponseStatusText string           `json:"response_status_text,omitempty"`
	ResponseHeaders    CapturedHeaders  `json:"response_headers,omitempty"`
	Response           *CapturedBody    `json:"response,omitempty"`
	FinishReason       string           `json:"finish_reason,omitempty"`
	Usage              *Usage           `json:"usage,omitempty"`
	Redaction          CaptureRedaction `json:"redaction"`
	CapturedAt         string           `json:"captured_at"`
}

// CaptureRedaction records what the masker did, so a reader can see that
// masking ran rather than assume it. ShortSecretValues is the honest
// negative: a key shorter than minMaskableSecret is not searched for in
// bodies, and the count says how many such values this run held.
type CaptureRedaction struct {
	MaskedHeaderNames      []string `json:"masked_header_names"`
	RequestHeaderAllowlist []string `json:"request_header_allowlist"`
	SecretValuesMasked     int      `json:"secret_values_masked"`
	ShortSecretValues      int      `json:"secret_values_too_short_to_mask"`
	BearerTokensMasked     int      `json:"bearer_tokens_masked"`
}

// CaptureEntry is one manifest row: enough to triage a directory of
// captures without opening a single document.
type CaptureEntry struct {
	CallIndex     int    `json:"call_index"`
	File          string `json:"file"`
	Model         string `json:"model"`
	ProviderHost  string `json:"provider_host"`
	RequestSHA256 string `json:"request_sha256"`
	InputTokens   int    `json:"input_tokens,omitempty"`
	OutputTokens  int    `json:"output_tokens,omitempty"`
	ResponseBytes int    `json:"response_bytes,omitempty"`
	Outcome       string `json:"outcome"`
	Truncated     bool   `json:"truncated,omitempty"`
	// The two omitted-byte counters are the difference between what went on
	// the wire and what the document holds, per direction. They are the only
	// thing the manifest needs to say about a cut body, and the manifest
	// itself carries no body: a capture is read from its files, never
	// reprinted into a job log.
	RequestOmittedBytes  int    `json:"request_omitted_bytes,omitempty"`
	ResponseOmittedBytes int    `json:"response_omitted_bytes,omitempty"`
	CapturedAt           string `json:"captured_at"`
}

// CaptureManifest is the index beside the per-call documents.
type CaptureManifest struct {
	SchemaVersion int            `json:"schema_version"`
	MaxBodyBytes  int            `json:"max_body_bytes"`
	Calls         []CaptureEntry `json:"calls"`
}

// Capture writes one document per model call into a directory. The zero
// value is unusable; NewCapture builds one. Every method is safe for
// concurrent use: a review runs up to roles.review.concurrency calls at
// once, and each call owns its own document.
type Capture struct {
	dir      string
	maxBytes int

	mu         sync.Mutex
	secrets    []string
	secretSeen map[string]bool
	short      int
	seq        int
	calls      map[int]*CaptureCall
	rows       []CaptureEntry
}

// NewCapture returns a capture writing into dir with a per-body ceiling of
// maxBytes (DefaultCaptureMaxBytes when maxBytes <= 0). secrets are values
// to mask wherever they appear; empty and very short values are dropped,
// and a very short one is counted in every document's redaction block.
func NewCapture(dir string, maxBytes int, secrets ...string) *Capture {
	if maxBytes <= 0 {
		maxBytes = DefaultCaptureMaxBytes
	}
	if maxBytes < captureMinMaxBytes {
		maxBytes = captureMinMaxBytes
	}
	if maxBytes > captureMaxBytesCeiling {
		maxBytes = captureMaxBytesCeiling
	}
	c := &Capture{
		dir:        dir,
		maxBytes:   maxBytes,
		secretSeen: map[string]bool{},
		calls:      map[int]*CaptureCall{},
	}
	for _, s := range secrets {
		c.registerSecret(s)
	}
	return c
}

// Dir is where the documents are written.
func (c *Capture) Dir() string { return c.dir }

// MaxBytes is the per-body ceiling in force.
func (c *Capture) MaxBytes() int { return c.maxBytes }

// registerSecret adds a value to mask. It is idempotent: the same key
// registered by every call of a run registers once.
func (c *Capture) registerSecret(s string) {
	if s == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secretSeen[s] {
		return
	}
	c.secretSeen[s] = true
	if len(s) < minMaskableSecret {
		c.short++
		return
	}
	c.secrets = append(c.secrets, s)
}

// begin reserves the next call index and returns the document the client
// fills in. The document is not on disk until write.
func (c *Capture) begin(call CaptureCall) *CaptureCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	call.SchemaVersion = CaptureSchemaVersion
	call.CallIndex = c.seq
	if call.CapturedAt == "" {
		call.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	}
	c.calls[call.CallIndex] = &call
	return &call
}

// write masks, caps and persists one call document, then rewrites the
// manifest. It is idempotent per call: the reviewer calls it again when a
// decoded response turns out to be unusable, and the second write replaces
// the first. A write failure is reported on stderr and never returned: a
// capture is a diagnostic, and failing a review over one is worse than the
// lost capture.
func (c *Capture) write(call *CaptureCall) {
	if c == nil || call == nil {
		return
	}
	doc := c.redact(call)
	name := fmt.Sprintf("call-%04d.json", call.CallIndex)
	path := filepath.Join(c.dir, name)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cite: wire capture marshal failed for %s: %v\n", name, err)
		return
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "cite: wire capture directory %s could not be created: %v\n", c.dir, err)
		return
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cite: wire capture write to %s failed: %v\n", path, err)
		return
	}
	c.manifestRow(doc, name)
}

// redact returns a masked, capped copy of the call. The caller's document is
// left alone: it holds unmasked values only in memory, and the request body
// it carries is already the exact bytes that went on the wire.
func (c *Capture) redact(call *CaptureCall) *CaptureCall {
	out := *call
	out.Request = c.body(call.Request.Text)
	if call.Response != nil {
		r := c.body(call.Response.Text)
		out.Response = &r
	}
	out.Redaction = CaptureRedaction{
		MaskedHeaderNames:      append([]string(nil), maskedHeaderNames...),
		RequestHeaderAllowlist: append([]string(nil), capturedRequestHeaders...),
	}
	// Request headers: the allowlist decides. A value that survives is one of
	// the five names Cite itself sets, so nothing is left to match there.
	out.RequestHeaders = maskRequestHeaders(call.RequestHeaders)
	// Response headers: the provider wrote them, so the credential-name list
	// and the value pass both apply.
	out.ResponseHeaders = maskResponseHeaders(call.ResponseHeaders, secretsOf(c), &out.Redaction)
	out.Redaction.ShortSecretValues = shortSecretCount(c)
	for _, field := range []*string{&out.Request.Text, &out.Request.TruncationNote, &out.OutcomeDetail, &out.RejectedReason} {
		var n int
		*field, n = c.scrub(*field)
		out.Redaction.SecretValuesMasked += n
	}
	if out.Response != nil {
		var n int
		out.Response.Text, n = c.scrub(out.Response.Text)
		out.Response.TruncationNote, n = c.scrub(out.Response.TruncationNote)
		out.Redaction.SecretValuesMasked += n
	}
	return &out
}

// secretsOf is a snapshot of the registered secret values, for a masking pass
// that must not hold the lock while it rewrites a body.
func secretsOf(c *Capture) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.secrets...)
}

// shortSecretCount is how many values this run holds that are too short to
// search a body for. Every document records it, so a reader sees the limit
// instead of inferring it.
func shortSecretCount(c *Capture) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.short
}

// scrub replaces every known secret value in s, then every inline
// bearer/basic credential, and reports how many values it masked. It is the
// second layer. The first is the request-header allowlist and the
// credential-name list, and this one catches a credential travelling where no
// name protects it: a body, a URL, a free-text field.
func (c *Capture) scrub(s string) (string, int) {
	masked := 0
	for _, secret := range secretsOf(c) {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Redacted)
			masked++
		}
	}
	out, inline := maskInline(s)
	return out, masked + inline
}

// maskInline replaces inline bearer/basic credentials in body text.
func maskInline(s string) (string, int) {
	n := 0
	out := bearerRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := bearerRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		n++
		return sub[1] + " " + Redacted
	})
	return out, n
}

// body caps one body to the ceiling and records its hash and its on-wire
// size. Truncation is always visible: a cut body carries truncated, the
// original size and a note saying which setting produced the cut.
func (c *Capture) body(text string) CapturedBody {
	raw := []byte(text)
	sum := sha256.Sum256(raw)
	b := CapturedBody{
		SHA256: hex.EncodeToString(sum[:]),
		Bytes:  len(raw),
		Text:   text,
	}
	if len(raw) <= c.maxBytes {
		return b
	}
	cut := raw[:c.maxBytes]
	// Drop at most the last few bytes, never a rescan of the prefix. A cut
	// that lands inside a rune would make the JSON document undecodable,
	// which is the one failure this feature exists to prevent, and a
	// trailing rune is at most utf8.UTFMax-1 bytes wide. An invalid byte
	// earlier in the body is a different matter: json encoding replaces it
	// with U+FFFD, so the document still decodes, and trimming back to the
	// first invalid byte would throw away everything after it for nothing.
	for n := 0; n < utf8.UTFMax && len(cut) > 0; n++ {
		r, size := utf8.DecodeLastRune(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	b.Text = string(cut)
	b.Truncated = true
	b.OriginalBytes = len(raw)
	b.TruncationNote = fmt.Sprintf(
		"truncated to the %d-byte per-body capture ceiling (%s); the body was %d bytes, %d written; raise %s or capture nothing",
		c.maxBytes, Redacted, len(raw), len(cut), CaptureMaxEnvVar)
	return b
}

// manifestRow inserts or updates one manifest row and rewrites the index.
func (c *Capture) manifestRow(doc *CaptureCall, name string) {
	row := CaptureEntry{
		CallIndex:     doc.CallIndex,
		File:          name,
		Model:         doc.Model,
		ProviderHost:  doc.ProviderHost,
		RequestSHA256: doc.Request.SHA256,
		Outcome:       doc.Outcome,
		Truncated:     doc.Request.Truncated || (doc.Response != nil && doc.Response.Truncated),
		CapturedAt:    doc.CapturedAt,
	}
	if doc.Usage != nil {
		row.InputTokens = doc.Usage.InputTokens
		row.OutputTokens = doc.Usage.OutputTokens
	}
	if doc.Request.Truncated {
		row.RequestOmittedBytes = doc.Request.OriginalBytes - len(doc.Request.Text)
	}
	if doc.Response != nil {
		row.ResponseBytes = doc.Response.Bytes
		if doc.Response.Truncated {
			row.ResponseOmittedBytes = doc.Response.OriginalBytes - len(doc.Response.Text)
		}
	}
	c.mu.Lock()
	updated := false
	for i := range c.rows {
		if c.rows[i].CallIndex == row.CallIndex {
			c.rows[i] = row
			updated = true
			break
		}
	}
	if !updated {
		c.rows = append(c.rows, row)
	}
	sort.Slice(c.rows, func(i, j int) bool { return c.rows[i].CallIndex < c.rows[j].CallIndex })
	rows := append([]CaptureEntry(nil), c.rows...)
	maxBytes := c.maxBytes
	c.mu.Unlock()

	b, err := json.MarshalIndent(CaptureManifest{
		SchemaVersion: CaptureSchemaVersion,
		MaxBodyBytes:  maxBytes,
		Calls:         rows,
	}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cite: wire capture manifest marshal failed: %v\n", err)
		return
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "cite: wire capture directory %s could not be created: %v\n", c.dir, err)
		return
	}
	if err := os.WriteFile(filepath.Join(c.dir, captureManifestName), b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cite: wire capture manifest write failed: %v\n", err)
	}
}

// captureHeaders converts wire headers into their captured form. Nothing is
// dropped here: a header an operator did not expect to see is exactly what
// makes a provider's behaviour explicable. Masking, not filtering.
func captureHeaders(h http.Header) CapturedHeaders {
	if len(h) == 0 {
		return nil
	}
	out := make(CapturedHeaders, len(h))
	for name, values := range h {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// maskRequestHeaders applies the allowlist: a listed name keeps its value,
// every other name keeps the name and loses the value. The name is the
// diagnostic (a credential was sent, and this is the one), the value is what
// must not travel. Cite chooses every request header it sends, so a name
// outside the list is one this repository has never seen, and an allowlist is
// the only rule that is right about a name nobody has thought of yet.
func maskRequestHeaders(h CapturedHeaders) CapturedHeaders {
	if h == nil {
		return nil
	}
	out := make(CapturedHeaders, len(h))
	for name, value := range h {
		if allowedRequestHeader(name) {
			out[name] = value
			continue
		}
		out[name] = Redacted
	}
	return out
}

// allowedRequestHeader reports whether a request header value is kept.
func allowedRequestHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, a := range capturedRequestHeaders {
		if n == a {
			return true
		}
	}
	return false
}

// maskResponseHeaders masks by credential name, then by value. The provider
// chose these names, so a name this repository has never seen is exactly the
// case the value pass exists for.
func maskResponseHeaders(h CapturedHeaders, secrets []string, red *CaptureRedaction) CapturedHeaders {
	if h == nil {
		return nil
	}
	out := make(CapturedHeaders, len(h))
	for name, value := range h {
		if isCredentialHeader(name) {
			out[name] = Redacted
			continue
		}
		maskedValues := 0
		for _, secret := range secrets {
			if strings.Contains(value, secret) {
				value = strings.ReplaceAll(value, secret, Redacted)
				maskedValues++
			}
		}
		red.SecretValuesMasked += maskedValues
		masked, inline := maskInline(value)
		red.BearerTokensMasked += inline
		out[name] = masked
	}
	return out
}

// isCredentialHeader reports whether a header's value is replaced wholesale.
func isCredentialHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, f := range credentialHeaderFragments {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}

// MarkRejected records, in the wire capture, that a decoded response was
// refused by the reviewer: the outcome becomes parse-failure and the reason
// travels with the raw bytes that were rejected. It is a no-op when the
// capture is off, and it never changes what the caller does next. The retry
// budget, the finding set and the verdict are all decided before this is
// called.
func (r *CompletionResponse) MarkRejected(reason error) {
	if r == nil || r.capture == nil || r.captureCall == nil || reason == nil {
		return
	}
	r.captureCall.Outcome = CaptureOutcomeParseFailure
	r.captureCall.RejectedReason = reason.Error()
	r.capture.write(r.captureCall)
}

// --- the process-wide opt-in ----------------------------------------------

var (
	activeMu     sync.Mutex
	active       *Capture
	activeLoaded bool
)

// ActiveCapture returns this process's capture, or nil when the operator did
// not ask for one. The environment is read once: a capture is a per-run
// setting, and re-reading it per call would make a run's captures depend on
// the order its goroutines happened to run in.
func ActiveCapture() *Capture {
	activeMu.Lock()
	defer activeMu.Unlock()
	if !activeLoaded {
		activeLoaded = true
		active = captureFromEnv()
		if active != nil {
			fmt.Fprintf(os.Stderr, "cite: wire capture on: one masked document per model call in %s (ceiling %d bytes per body); a capture contains the prompt, so treat the directory as private review material\n",
				active.Dir(), active.MaxBytes())
		}
	}
	return active
}

// SetActiveCapture installs a capture (or clears it with nil) for this
// process. Tests use it; a caller with its own configuration can too.
func SetActiveCapture(c *Capture) {
	activeMu.Lock()
	defer activeMu.Unlock()
	active, activeLoaded = c, true
}

// captureFromEnv builds the capture the environment asks for, or nil. Only
// the true spellings enable it, so a typo leaves the feature off rather than
// on: a mistyped input must never begin writing prompts to disk.
func captureFromEnv() *Capture {
	switch os.Getenv(CaptureEnvVar) {
	case "1", "true", "TRUE", "True":
	default:
		return nil
	}
	dir := os.Getenv(CaptureDirEnvVar)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "cite-wire")
	}
	maxBytes := DefaultCaptureMaxBytes
	if v := os.Getenv(CaptureMaxEnvVar); v != "" {
		n, err := strconv.Atoi(v)
		// Each branch assigns the value it announces. A message that says
		// "using 67108864" while the caller's larger number survives into
		// the capture is a log that lies about the artifact's size, which is
		// the one thing an operator reading it needs to be true.
		switch {
		case err != nil || n <= 0:
			fmt.Fprintf(os.Stderr, "cite: %s=%q is not a byte count; using %d\n", CaptureMaxEnvVar, v, DefaultCaptureMaxBytes)
			maxBytes = DefaultCaptureMaxBytes
		case n > captureMaxBytesCeiling:
			fmt.Fprintf(os.Stderr, "cite: %s=%d is above the %d-byte ceiling; using %d\n", CaptureMaxEnvVar, n, captureMaxBytesCeiling, captureMaxBytesCeiling)
			maxBytes = captureMaxBytesCeiling
		case n < captureMinMaxBytes:
			fmt.Fprintf(os.Stderr, "cite: %s=%d is below the %d-byte floor; using %d\n", CaptureMaxEnvVar, n, captureMinMaxBytes, captureMinMaxBytes)
			maxBytes = captureMinMaxBytes
		default:
			maxBytes = n
		}
	}
	return NewCapture(dir, maxBytes)
}

// captureURL renders a request URL for a capture: host and path, never the
// query string and never the userinfo, because both are places a key can
// travel on an endpoint Cite does not control.
func captureURL(u *url.URL) (host, path string) {
	if u == nil {
		return "", ""
	}
	return u.Host, u.Path
}
