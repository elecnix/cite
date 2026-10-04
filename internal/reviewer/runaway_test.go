package reviewer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// runawayProvider is the measured CI shape: a real OpenAI-compatible endpoint
// that burns the whole output budget on reasoning and returns no answer, then
// answers normally once reasoning is bounded. Using the real client here (not
// a fake that returns a pre-cooked error) keeps the classification under test
// -- it is produced by the production code path, not simulated.
type runawayProvider struct {
	mu      sync.Mutex
	calls   []int    // max_tokens of each request, in order
	efforts []string // reasoning_effort of each request, in order ("" = omitted)
	bodies  []string // the response body returned for each request
}

func (p *runawayProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		MaxTokens       int    `json:"max_tokens"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	_ = json.Unmarshal(raw, &req)

	p.mu.Lock()
	i := len(p.calls)
	p.calls = append(p.calls, req.MaxTokens)
	p.efforts = append(p.efforts, req.ReasoningEffort)
	body := "{}"
	switch {
	case i < len(p.bodies):
		body = p.bodies[i]
	case len(p.bodies) > 0:
		body = p.bodies[len(p.bodies)-1]
	}
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(body, runawayMarker) {
		// A runaway burns exactly the budget it was given, so the fixture has
		// to echo the request's own max_tokens.
		_, _ = w.Write([]byte(runawayReview(req.MaxTokens)))
		return
	}
	_, _ = w.Write([]byte(body))
}

// runawayMarker stands in for "every call after this one runs away"; the
// handler fills in the budget the request actually asked for.
const runawayMarker = "<runaway>"

// runawayReview is the runaway shape at whatever budget was requested: the
// measured completion_tokens == max_tokens with content null.
func runawayReview(maxTokens int) string {
	return `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"` +
		strings.Repeat("still thinking ", 40) + `"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":50,"completion_tokens":` + itoa(maxTokens) + `,` +
		`"completion_tokens_details":{"reasoning_tokens":` + itoa(maxTokens) + `}}}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The defect: one runaway file cost the whole verdict. A response that spent
// the entire budget without emitting an answer carries no finding to lose and
// no answer to be deterministic about, so treating it as a terminal
// deterministic failure is what turned a recoverable miss into
// COULD_NOT_EVALUATE. The file must be reviewed instead.
func TestRunawayGenerationIsRecoveredNotTerminal(t *testing.T) {
	p := &runawayProvider{bodies: []string{
		completion(triageJSON("a.go")),  // call 0: triage flags the file
		runawayMarker,                   // call 1: review burns the budget, no answer
		completion(reviewJSON2("a.go")), // call 2: review answers
	}}
	ts := httptest.NewServer(p)
	defer ts.Close()

	c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	rec, err := runOnce(t, baseInputs(), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Files) != 1 {
		t.Fatalf("Files = %+v", rec.Files)
	}
	got := rec.Files[0]
	if got.State != model.FileReviewed {
		t.Fatalf("file state = %q reason = %q; a runaway generation must not cost the verdict",
			got.State, got.Reason)
	}
	if !got.Reviewed {
		t.Fatal("file must be marked reviewed")
	}
	if len(p.calls) != 3 {
		t.Fatalf("provider calls = %d (%v), want 3: triage, runaway, recovery", len(p.calls), p.calls)
	}
}

// The recovery must be bounded: a provider that runs away every single time
// still has to fail fast and closed, and it must not loop.
func TestRunawayRecoveryIsBoundedAndFailsClosed(t *testing.T) {
	p := &runawayProvider{bodies: []string{
		completion(triageJSON("a.go")),
		runawayMarker,
	}}
	ts := httptest.NewServer(p)
	defer ts.Close()

	c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	rec, err := runOnce(t, baseInputs(), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.calls) != 3 {
		t.Fatalf("provider calls = %d (%v), want exactly 3: triage, runaway, ONE bounded recovery",
			len(p.calls), p.calls)
	}
	if rec.Files[0].State != model.FileErrored {
		t.Fatalf("file state = %q, want errored when every attempt runs away", rec.Files[0].State)
	}
	if rec.Coverage.Complete {
		t.Fatal("a file that never evaluated must leave coverage incomplete, never green")
	}
}

// An explicit operator reasoning_effort is an operator instruction and Cite
// never overrides it, including on the recovery attempt.
func TestRunawayRecoveryRespectsExplicitOperatorReasoningEffort(t *testing.T) {
	p := &runawayProvider{bodies: []string{
		completion(triageJSON("a.go")),
		runawayMarker,
	}}
	ts := httptest.NewServer(p)
	defer ts.Close()

	c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	o := baseOptions(c)
	o.ReasoningEffort = "high"
	var logs strings.Builder
	o.Logger = func(f string, a ...any) { logs.WriteString(fmt.Sprintf(f, a...) + "\n") }
	rec, err := runOnce(t, baseInputs(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Files[0].State != model.FileErrored {
		t.Fatalf("file state = %q, want errored", rec.Files[0].State)
	}
	// Every call carried the operator's own value, including the recovery.
	for i, e := range p.efforts {
		if e != "high" {
			t.Fatalf("call %d sent reasoning_effort %q; an explicit operator value is never overridden", i, e)
		}
	}
	if len(p.efforts) != 3 {
		t.Fatalf("provider calls = %d (%v), want 3", len(p.efforts), p.efforts)
	}
	// The retry repeated the request, so the log must not claim a bound this
	// branch never applied. An operator reading "bounded at high" would
	// conclude Cite had done something it did not.
	if !strings.Contains(logs.String(), "already in force") {
		t.Fatalf("log must say the operator's bound was already in force, got:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "bounded at") {
		t.Fatalf("log claims a bound was applied when none was, got:\n%s", logs.String())
	}
}

// With no operator instruction, the one bounded recovery is what makes the
// attempt terminate: measured 2/2 with a bounded allowance versus 1/4 without
// on identical requests.
func TestRunawayRecoveryBoundsReasoningWhenUnset(t *testing.T) {
	p := &runawayProvider{bodies: []string{
		completion(triageJSON("a.go")),
		runawayMarker,
		completion(reviewJSON2("a.go")),
	}}
	ts := httptest.NewServer(p)
	defer ts.Close()

	c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	if _, err := runOnce(t, baseInputs(), baseOptions(c)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.efforts) != 3 {
		t.Fatalf("provider calls = %d, want 3", len(p.efforts))
	}
	if p.efforts[0] != "" || p.efforts[1] != "" {
		t.Fatalf("normal path must send no reasoning_effort, got %q, %q", p.efforts[0], p.efforts[1])
	}
	if p.efforts[2] == "" {
		t.Fatal("the bounded recovery attempt must bound reasoning; without it the retry repeats the failure")
	}
}

// completion wraps answer text in the OpenAI-compatible envelope the real
// client decodes, so the fixtures go through production parsing rather than
// the package-internal fake.
func completion(answer string) string {
	b, _ := json.Marshal(answer)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(b) +
		`},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":100}}`
}

// reviewJSON2 is reviewJSON with the schema fields strict mode requires, for
// use against a real endpoint rather than the package-internal fake.
func reviewJSON2(path string) string {
	return `{"schema_version":1,"path":"` + path + `","outcome":"reviewed",` +
		`"not_reviewable_reason":"","findings":[]}`
}
