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

	"github.com/elecnix/cite/internal/config"
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
	calls, _ := p.snapshot()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %d (%v), want 3: triage, runaway, recovery", len(calls), calls)
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
	calls, _ := p.snapshot()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %d (%v), want exactly 3: triage, runaway, ONE bounded recovery",
			len(calls), calls)
	}
	if rec.Files[0].State != model.FileErrored {
		t.Fatalf("file state = %q, want errored when every attempt runs away", rec.Files[0].State)
	}
	if rec.Coverage.Complete {
		t.Fatal("a file that never evaluated must leave coverage incomplete, never green")
	}
}

// An explicit operator reasoning_effort is an operator instruction and Cite
// never overrides it. That also means the recovery has nothing left to add,
// so the file goes terminal on the first runaway instead of paying a second
// full-budget call to repeat a request byte for byte.
func TestRunawayWithOperatorReasoningEffortSpendsNoRecovery(t *testing.T) {
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
	if rec.Files[0].Reason != "runaway_generation" {
		t.Fatalf("reason = %q, want runaway_generation", rec.Files[0].Reason)
	}
	// triage plus one review. A third call would be a recovery the branch
	// cannot make different.
	calls, efforts := p.snapshot()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d (%v), want 2: no recovery is possible with an operator bound in force",
			len(calls), calls)
	}
	for i, e := range efforts {
		if e != "high" {
			t.Fatalf("call %d sent reasoning_effort %q; an explicit operator value is never overridden", i, e)
		}
	}
	// The remedy names the knob the operator actually holds.
	if !strings.Contains(logs.String(), `reasoning_effort="high"`) {
		t.Fatalf("remedy must name the operator's own reasoning_effort, got:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "max_output_tokens") {
		t.Fatalf("remedy must not point at the output cap, got:\n%s", logs.String())
	}
}

// The recovery bound belongs to the recovery alone. When that attempt comes
// back unparseable and the loop re-asks, the next request must go out exactly
// as the operator configured it, not carrying a limit they never set.
func TestRunawayBoundDoesNotLeakIntoLaterReasks(t *testing.T) {
	p := &runawayProvider{bodies: []string{
		completion(triageJSON("a.go")),
		runawayMarker,
		completion("{not json"),         // the recovery answers unparseably
		completion(reviewJSON2("a.go")), // a parse re-ask follows
	}}
	ts := httptest.NewServer(p)
	defer ts.Close()

	c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	if _, err := runOnce(t, baseInputs(), baseOptions(c)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, efforts := p.snapshot()
	if len(efforts) < 4 {
		t.Fatalf("provider calls = %d (%v), want at least 4", len(efforts), efforts)
	}
	if efforts[2] == "" {
		t.Fatal("the recovery attempt must carry the bound")
	}
	for i, e := range efforts[3:] {
		if e != "" {
			t.Fatalf("call %d carried reasoning_effort=%q; the recovery bound leaked into a later re-ask", i+3, e)
		}
	}
	// The output budget is the other half of the bound, and it is bounded the
	// same way: to the one recovery call, never to the request the loop
	// reuses.
	calls, _ := p.snapshot()
	if calls[2] >= calls[1] {
		t.Fatalf("recovery asked for max_tokens=%d against the failed attempt's %d; the budget bound is not in force",
			calls[2], calls[1])
	}
	for i, tk := range calls[3:] {
		if tk != calls[1] {
			t.Fatalf("call %d asked for max_tokens=%d, want the operator's %d; the recovery budget leaked into a later re-ask",
				i+3, tk, calls[1])
		}
	}
}

// snapshot copies the recorded request attributes under the lock. The
// handler appends from the server goroutine while the test reads from the
// test goroutine, so every read goes through here.
func (p *runawayProvider) snapshot() (calls []int, efforts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.calls...), append([]string(nil), p.efforts...)
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

// The defect this pins: the runaway recovery re-sent max_tokens unchanged, so
// it was identical to the attempt that just failed in the one dimension the
// provider provably obeys. Measured on deepseek-v4.1-flash, a runaway's
// reasoning length tracks the budget it was handed rather than the task: the
// captured trace burned 65536 output tokens on one documentation file, reaching
// its decision by token 42912 and then spending the remaining 34.5% of the
// budget emitting a single repeated token without ever writing the review.
//
// A recovery that asks for the same budget again is therefore not a second
// chance, it is the same roll. It has to ask for less.
func TestRunawayRecoveryAsksForASmallerOutputBudget(t *testing.T) {
	p := &runawayProvider{bodies: []string{
		completion(triageJSON("a.go")),
		runawayMarker,
		completion(reviewJSON2("a.go")),
	}}
	ts := httptest.NewServer(p)
	defer ts.Close()

	c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
	rec, err := runOnce(t, baseInputs(), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Files[0].State != model.FileReviewed {
		t.Fatalf("file state = %q reason = %q; the recovery must still be able to answer",
			rec.Files[0].State, rec.Files[0].Reason)
	}
	calls, _ := p.snapshot()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %d (%v), want 3: triage, runaway, recovery", len(calls), calls)
	}
	if calls[1] <= 0 {
		t.Fatalf("call 1 sent max_tokens=%d; the harness must record a real budget", calls[1])
	}
	if calls[2] >= calls[1] {
		t.Fatalf("recovery asked for max_tokens=%d against the failed attempt's %d; "+
			"a recovery that hands the model the same budget repeats the request that just ran away",
			calls[2], calls[1])
	}
}

// The bound must never be smaller than a review answer needs, and never larger
// than the operator's own cap: an operator who configured 4096 has said
// something about how big a review is, and the recovery is not the place to
// argue with it.
func TestRunawayRecoveryBudgetRespectsTheOperatorCap(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
	}{
		{"cap below the bound", 1024},
		{"cap equal to the bound", 8192},
		{"cap above the bound", 65536},
		{"far above the bound", 131072},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &runawayProvider{bodies: []string{
				completion(triageJSON("a.go")),
				runawayMarker,
				completion(reviewJSON2("a.go")),
			}}
			ts := httptest.NewServer(p)
			defer ts.Close()

			c := &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}
			cfg := config.Default()
			cfg.Roles = map[model.Role]config.RoleSpec{
				model.RoleReview: {MaxOutputTokens: tc.cap},
			}
			rec, err := runOnce(t, baseInputs(), Options{Cfg: cfg, Client: c})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if rec.Files[0].State != model.FileReviewed {
				t.Fatalf("file state = %q reason = %q", rec.Files[0].State, rec.Files[0].Reason)
			}
			calls, _ := p.snapshot()
			if len(calls) != 3 {
				t.Fatalf("provider calls = %d (%v), want 3", len(calls), calls)
			}
			if calls[1] != tc.cap {
				t.Fatalf("call 1 sent max_tokens=%d, want the operator cap %d", calls[1], tc.cap)
			}
			if calls[2] > tc.cap {
				t.Fatalf("recovery asked for max_tokens=%d, above the operator cap %d", calls[2], tc.cap)
			}
		})
	}
}
