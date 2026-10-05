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
	bodies := []string{
		completion(triageJSON("a.go")),
		runawayMarker,
		completion("{not json"),         // the recovery answers unparseably
		completion(reviewJSON2("a.go")), // a parse re-ask follows
	}
	// A configured cap rather than the built-in default, so that every budget
	// this test compares against has one unambiguous source. The operator
	// asked for 40000 here, and nothing else in this test may produce 40000.
	const operatorCap = 40000
	var logs strings.Builder
	_, calls, efforts := runAgainstRecovery(t, operatorCap, &logs, bodies...)
	want := fmt.Sprintf("the output budget cut from %d to %d tokens", operatorCap, recoveryMaxOutputTokens)
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("recovery log must contain %q, got:\n%s", want, logs.String())
	}
	// t.Fatalf ends this goroutine, so every index below is behind the guard.
	if len(calls) < 4 || len(efforts) < 4 {
		t.Fatalf("provider calls = %d budgets and %d efforts, want at least 4 of each", len(calls), len(efforts))
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
	//
	// Every later re-ask must go out at the cap the operator configured. The
	// bound check comes first because it is what makes that comparison bite:
	// before this bound every call shared one budget, so a leak onto the
	// reused request was indistinguishable from correct behaviour. Comparing
	// a later call against the recovery's own budget as well would be
	// unreachable, since the loop has already established it differs.
	if calls[2] >= calls[1] {
		t.Fatalf("recovery asked for max_tokens=%d against the failed attempt's %d; the budget bound is not in force",
			calls[2], calls[1])
	}
	if calls[1] != operatorCap {
		t.Fatalf("call 1 sent max_tokens=%d, want the operator's configured %d", calls[1], operatorCap)
	}
	for i, tk := range calls[3:] {
		if tk != operatorCap {
			t.Fatalf("call %d asked for max_tokens=%d, want the operator's configured %d; a later re-ask went out with something the operator never configured",
				i+3, tk, operatorCap)
		}
	}
}

// runAgainstRecovery serves the given bodies, runs one review of a.go through
// the reviewer, and returns the record with the budgets and efforts the
// provider saw. Every runaway test here needs that same three-line shape, and
// the duplicate detector is right that spelling it out each time says nothing
// any of them does not already say.
func runAgainstRecovery(t *testing.T, cap int, log *strings.Builder, bodies ...string) (*model.RunRecord, []int, []string) {
	t.Helper()
	p := &runawayProvider{bodies: bodies}
	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)
	cfg := config.Default()
	if cap > 0 {
		cfg.Roles = map[model.Role]config.RoleSpec{
			model.RoleReview: {MaxOutputTokens: cap},
		}
	}
	o := Options{Cfg: cfg, Client: &model.OpenAICompatClient{BaseURL: ts.URL, Model: "m"}}
	if log != nil {
		o.Logger = func(f string, a ...any) { log.WriteString(fmt.Sprintf(f, a...) + "\n") }
	}
	rec, err := runOnce(t, baseInputs(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls, efforts := p.snapshot()
	return rec, calls, efforts
}

// wantOneReviewed fails unless the run recorded exactly one file and evaluated
// it, which every test below needs before it may read Files[0].
func wantOneReviewed(t *testing.T, rec *model.RunRecord) {
	t.Helper()
	if len(rec.Files) != 1 {
		t.Fatalf("Files = %+v, want exactly one", rec.Files)
	}
	if rec.Files[0].State != model.FileReviewed {
		t.Fatalf("file state = %q reason = %q; the recovery must still be able to answer",
			rec.Files[0].State, rec.Files[0].Reason)
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
	rec, calls, _ := runAgainstRecovery(t, 0, nil,
		completion(triageJSON("a.go")),
		runawayMarker,
		completion(reviewJSON2("a.go")))
	wantOneReviewed(t, rec)
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
			rec, calls, _ := runAgainstRecovery(t, tc.cap, nil,
				completion(triageJSON("a.go")),
				runawayMarker,
				completion(reviewJSON2("a.go")))
			wantOneReviewed(t, rec)
			if len(calls) != 3 {
				t.Fatalf("provider calls = %d (%v), want 3", len(calls), calls)
			}
			if calls[1] != tc.cap {
				t.Fatalf("call 1 sent max_tokens=%d, want the operator cap %d", calls[1], tc.cap)
			}
			// Exactly, not an upper bound. A recovery that sent the operator's
			// cap back unchanged satisfies "no more than the cap" on every
			// entry here, so a table of ceilings pins nothing. Above the
			// bound the recovery must land on the bound, and at or below it
			// the operator's own number stands.
			want := tc.cap
			if tc.cap > recoveryMaxOutputTokens {
				want = recoveryMaxOutputTokens
			}
			if calls[2] != want {
				t.Fatalf("recovery asked for max_tokens=%d, want %d (operator cap %d, bound %d)",
					calls[2], want, tc.cap, recoveryMaxOutputTokens)
			}
		})
	}
}

// The bound applies to every cap an operator can configure, and to nothing
// else. A cap below the bound stands, a cap above it comes down to the bound,
// and a non-positive cap is not a budget at all: it passes through, so the
// recovery never carries a number the caller did not hold.
func TestRunawayRecoveryBudgetNeverExceedsTheCap(t *testing.T) {
	for _, tc := range []struct {
		cap  int
		want int
	}{
		{-1, -1},
		{0, 0},
		{1, 1},
		{1024, 1024},
		{recoveryMaxOutputTokens - 1, recoveryMaxOutputTokens - 1},
		{recoveryMaxOutputTokens, recoveryMaxOutputTokens},
		{recoveryMaxOutputTokens + 1, recoveryMaxOutputTokens},
		{65536, recoveryMaxOutputTokens},
		{131072, recoveryMaxOutputTokens},
	} {
		if got := runawayRecoveryBudget(tc.cap); got != tc.want {
			t.Errorf("runawayRecoveryBudget(%d) = %d, want %d", tc.cap, got, tc.want)
		}
	}
}

// The recovery log is the forensics an operator reads when a file dies of a
// runaway, so the budget it names has to be the budget the provider was
// actually asked for. The two were computed at separate call sites, so they
// agreed only for as long as both expressions stayed identical. This asserts
// the agreement rather than either expression, so it keeps holding if the
// budget is ever derived some other way.
func TestRunawayRecoveryLogNamesTheBudgetItSent(t *testing.T) {
	var logs strings.Builder
	_, calls, _ := runAgainstRecovery(t, 0, &logs,
		completion(triageJSON("a.go")),
		runawayMarker,
		completion(reviewJSON2("a.go")))
	if len(calls) != 3 {
		t.Fatalf("provider calls = %d (%v), want 3", len(calls), calls)
	}
	// "the output budget cut from 131072 to 8192 tokens"
	want := fmt.Sprintf("the output budget cut from %d to %d tokens", calls[1], calls[2])
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("recovery log must contain %q, so the budget it reports is the one the provider saw, got:\n%s",
			want, logs.String())
	}
}

// The log must describe what the call did, not what the code would have done
// with a different cap. A role pinned at or below the bound keeps its own cap,
// so there is no cut to announce, and "cut from 4096 to 4096 tokens" would put
// a change in the forensics that never happened.
func TestRunawayRecoveryLogDoesNotClaimACutItDidNotMake(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
		want string
	}{
		{"cap below the bound", 4096, "the output budget held at 4096 tokens"},
		{"cap equal to the bound", recoveryMaxOutputTokens, fmt.Sprintf("the output budget held at %d tokens", recoveryMaxOutputTokens)},
		{"cap above the bound", 65536, fmt.Sprintf("the output budget cut from 65536 to %d tokens", recoveryMaxOutputTokens)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			runAgainstRecovery(t, tc.cap, &logs,
				completion(triageJSON("a.go")),
				runawayMarker,
				completion(reviewJSON2("a.go")))
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("recovery log must contain %q, got:\n%s", tc.want, logs.String())
			}
			if strings.Contains(logs.String(), fmt.Sprintf("cut from %d to %d", tc.cap, tc.cap)) {
				t.Fatalf("recovery log claims a cut that did not happen at cap %d, got:\n%s", tc.cap, logs.String())
			}
		})
	}
}
