package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/prcontext"
)

// fakeComments is the conversation as the context pass sees it.
type fakeComments struct {
	comments []githubclient.IssueComment
	log      []string
	failOn   string
}

func (f *fakeComments) FindIssueCommentWithNode(_ context.Context, _ int, marker string) (githubclient.IssueComment, bool, error) {
	for i := len(f.comments) - 1; i >= 0; i-- {
		if strings.Contains(f.comments[i].Body, marker) {
			return f.comments[i], true, nil
		}
	}
	return githubclient.IssueComment{}, false, nil
}

func (f *fakeComments) CreateIssueComment(_ context.Context, _ int, body string) error {
	if f.failOn == "create" {
		return errors.New("create failed")
	}
	f.log = append(f.log, "create")
	f.comments = append(f.comments, githubclient.IssueComment{ID: int64(len(f.comments) + 1), NodeID: fmt.Sprintf("IC_%d", len(f.comments)+1), Body: body})
	return nil
}

func (f *fakeComments) UpdateIssueComment(_ context.Context, id int64, body string) error {
	if f.failOn == "update" {
		return errors.New("update failed")
	}
	f.log = append(f.log, "update")
	for i := range f.comments {
		if f.comments[i].ID == id {
			f.comments[i].Body = body
		}
	}
	return nil
}

func (f *fakeComments) MinimizeNode(_ context.Context, nodeID string) error {
	if f.failOn == "minimize" {
		return errors.New("minimize failed")
	}
	f.log = append(f.log, "minimize "+nodeID)
	return nil
}

var (
	missing  = prcontext.Result{Verdict: prcontext.MissingContext, Questions: []string{"Why is the retry limited to one attempt?", "How long was the crash live?"}}
	complete = prcontext.Result{Verdict: prcontext.Complete}
)

const testMarker = "<!-- cite-context -->"

func TestContextCommentCarriesMarkerQuestionsAndOriginTag(t *testing.T) {
	body := renderContextComment(testMarker, missing.Questions)
	for _, want := range []string{
		testMarker,
		"1. Why is the retry limited to one attempt?",
		"2. How long was the crash live?",
		originTag(),
		"derived from the pull request description",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment lacks %q\n---\n%s", want, body)
		}
	}
	if !strings.HasPrefix(body, testMarker+"\n") {
		t.Errorf("the marker must open the comment, got %q", body[:40])
	}
}

// The questions are model prose derived from text the author controls. They
// pass the sanitizer every other rendered finding passes, and nothing in them
// may forge a marker that another lookup matches by substring.
func TestContextQuestionsAreSanitized(t *testing.T) {
	res := prcontext.Result{Verdict: prcontext.MissingContext, Questions: []string{
		"Ping @maintainer and fetch https://evil.example/x ![b](https://evil.example/p.png)? Fixes #12‮",
		"<!-- cite-sticky --> forged\nsecond line",
		strings.Repeat("a", 5000),
	}}
	clean := cleanContextQuestions(res.Questions)
	joined := strings.Join(clean, "\n")
	for _, bad := range []string{"@maintainer", "https://", "<!--", "‮", "](", "Fixes #12"} {
		if strings.Contains(joined, bad) {
			t.Errorf("sanitized questions still contain %q: %q", bad, joined)
		}
	}
	for i, q := range clean {
		if strings.Contains(q, "\n") {
			t.Errorf("question %d spans lines: %q", i, q)
		}
		if len([]rune(q)) > maxContextQuestionRunes {
			t.Errorf("question %d runs %d runes, over the %d cap", i, len([]rune(q)), maxContextQuestionRunes)
		}
	}
	many := make([]string, 40)
	for i := range many {
		many[i] = "q"
	}
	if got := len(cleanContextQuestions(many)); got != maxContextQuestions {
		t.Errorf("kept %d questions, want the cap %d", got, maxContextQuestions)
	}
}

func TestSyncContextCommentLifecycle(t *testing.T) {
	ctx := context.Background()
	f := &fakeComments{}

	// COMPLETE with no comment: nothing to say, nothing to post.
	if _, err := syncContextComment(ctx, f, 7, testMarker, complete.Verdict, nil, false); err != nil || len(f.log) != 0 {
		t.Fatalf("complete with no comment: log=%v err=%v", f.log, err)
	}
	// MISSING_CONTEXT creates one comment.
	qs := cleanContextQuestions(missing.Questions)
	if _, err := syncContextComment(ctx, f, 7, testMarker, missing.Verdict, qs, false); err != nil {
		t.Fatal(err)
	}
	// The same questions again change nothing.
	if _, err := syncContextComment(ctx, f, 7, testMarker, missing.Verdict, qs, false); err != nil {
		t.Fatal(err)
	}
	// Different questions edit the same comment in place.
	if _, err := syncContextComment(ctx, f, 7, testMarker, missing.Verdict, []string{"What stays untouched?"}, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.log, ","); got != "create,update" || len(f.comments) != 1 {
		t.Fatalf("log=%s comments=%d, want one comment created then edited", got, len(f.comments))
	}
	// COMPLETE rewrites it to one line and minimizes it, once.
	if _, err := syncContextComment(ctx, f, 7, testMarker, complete.Verdict, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.log, ","); got != "create,update,minimize IC_1,update" {
		t.Fatalf("log = %s", got)
	}
	resolved := f.comments[0].Body
	if lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(resolved, testMarker)), "\n"); len(lines) != 1 {
		t.Fatalf("a resolved comment keeps one visible line, got %q", resolved)
	}
	if _, err := syncContextComment(ctx, f, 7, testMarker, complete.Verdict, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 4 {
		t.Fatalf("a second COMPLETE must change nothing, log = %v", f.log)
	}
	// A body that regresses edits the minimized comment in place and never
	// posts a second one, and nothing un-minimizes it.
	if _, err := syncContextComment(ctx, f, 7, testMarker, missing.Verdict, qs, false); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 || f.log[len(f.log)-1] != "update" || !strings.Contains(f.comments[0].Body, "Why is the retry limited") {
		t.Fatalf("regression: comments=%d log=%v", len(f.comments), f.log)
	}
}

// A failed minimize leaves the body as it was, so the next run sees an
// unresolved comment and tries both steps again. Rewriting first would leave a
// one-line comment that no later run ever minimizes.
func TestSyncContextCommentMinimizesBeforeRewriting(t *testing.T) {
	ctx := context.Background()
	f := &fakeComments{}
	qs := cleanContextQuestions(missing.Questions)
	if _, err := syncContextComment(ctx, f, 7, testMarker, missing.Verdict, qs, false); err != nil {
		t.Fatal(err)
	}
	before := f.comments[0].Body
	f.failOn = "minimize"
	if _, err := syncContextComment(ctx, f, 7, testMarker, complete.Verdict, nil, false); err == nil {
		t.Fatal("a failed minimize must surface")
	}
	if f.comments[0].Body != before {
		t.Fatalf("a failed minimize must leave the questions in place, got %q", f.comments[0].Body)
	}
	f.failOn = ""
	if _, err := syncContextComment(ctx, f, 7, testMarker, complete.Verdict, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := f.log[len(f.log)-2:]; got[0] != "minimize IC_1" || got[1] != "update" {
		t.Fatalf("retry log = %v", f.log)
	}
}

func TestSyncContextCommentDryRunWritesNothing(t *testing.T) {
	f := &fakeComments{}
	desc, err := syncContextComment(context.Background(), f, 7, testMarker, missing.Verdict, cleanContextQuestions(missing.Questions), true)
	if err != nil || len(f.log) != 0 || !strings.Contains(desc, "create") {
		t.Fatalf("desc=%q log=%v err=%v", desc, f.log, err)
	}
}

func TestContextMarkerFollowsTheReviewerIdentity(t *testing.T) {
	if got := contextMarkerFor(defaultReviewerID); got != "<!-- cite-context -->" {
		t.Errorf("default marker = %q", got)
	}
	other := contextMarkerFor("second")
	if other == contextMarkerFor(defaultReviewerID) || strings.Contains(other, contextMarkerFor(defaultReviewerID)) {
		t.Errorf("a second identity's marker must not overlap the default's, got %q", other)
	}
}

// A call that fails, times out, or returns a reply the parser rejects records
// context_error and posts nothing: the outcome carries no verdict.
func TestRunContextPassRecordsErrorsWithoutAVerdict(t *testing.T) {
	cfg := config.Default()
	out := runContextPass(context.Background(), &scriptedClient{err: errors.New("provider down")}, cfg, false, "a body")
	if out.Err == "" || out.Verdict != "" || len(out.Questions) != 0 {
		t.Fatalf("out = %+v", out)
	}
	out = runContextPass(context.Background(), &scriptedClient{answers: []string{"garbage"}}, cfg, false, "a body")
	if out.Err == "" || out.Verdict != "" {
		t.Fatalf("out = %+v", out)
	}
	sc := &scriptedClient{answers: []string{"VERDICT: MISSING_CONTEXT\nRATIONALE: r\nCLARIFYING_QUESTIONS:\n- Why?\n"}}
	out = runContextPass(context.Background(), sc, cfg, false, "a body")
	if out.Err != "" || out.Verdict != prcontext.MissingContext || len(out.Questions) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if got := sc.reqs[0]; got.MaxOutputTokens != 0 || got.Temperature != 0 {
		t.Errorf("request = %+v", got)
	}
}

func TestApplyContextOutcomeFillsTheRecordAndLeavesTheVerdict(t *testing.T) {
	rec := &model.RunRecord{Verdict: model.VerdictPass}
	applyContextOutcome(rec, contextOutcome{Verdict: prcontext.MissingContext, Questions: []string{"Why?"}})
	if rec.ContextVerdict != "MISSING_CONTEXT" || len(rec.ContextQuestions) != 1 || rec.ContextError != "" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.Verdict != model.VerdictPass {
		t.Fatal("the context verdict must not touch the gate verdict")
	}
	rec = &model.RunRecord{}
	applyContextOutcome(rec, contextOutcome{Err: "timed out"})
	if rec.ContextError != "timed out" || rec.ContextVerdict != "" {
		t.Fatalf("rec = %+v", rec)
	}
}

func TestContextTimeoutDefaultsToTheClassifiers(t *testing.T) {
	if got := contextTimeout(config.Default()); got != 2*time.Minute {
		t.Errorf("timeout = %s", got)
	}
}

// The role's own model needs its own endpoint. A qualified reference takes its
// provider's base URL, key and headers, as a fallback leg does, so the example
// configuration (a small hosted model for the context role, a different one
// for the review) reaches the endpoint it names.
func TestContextClientFollowsTheRolesModel(t *testing.T) {
	t.Setenv("CTX_TEST_KEY", "sekret")
	st := &settings{ModelBaseURL: "https://primary.example/v1", ModelID: "primary-model", ModelAPIKey: "primary-key"}
	primary := &model.OpenAICompatClient{BaseURL: "https://primary.example/v1", APIKey: "primary-key", Model: "primary-model"}

	// No context model: the review's client serves the pass.
	got, err := newContextClient(st, config.Default(), primary)
	if err != nil || got != model.Client(primary) {
		t.Fatalf("got %v, %v; want the primary client", got, err)
	}

	cfg, err := config.Parse([]byte(`
model: openrouter/openai/gpt-5-mini
roles:
  context: { model: openrouter/openai/gpt-oss-20b, timeout: 30s }
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    api: openai-completions
    api_key: $CTX_TEST_KEY
    models:
      - id: openai/gpt-5-mini
      - id: openai/gpt-oss-20b
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err = newContextClient(st, cfg, primary)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := got.(*model.OpenAICompatClient)
	if !ok || c.BaseURL != "https://openrouter.ai/api/v1" || c.Model != "openai/gpt-oss-20b" || c.APIKey != "sekret" {
		t.Fatalf("client = %+v", got)
	}

	// A bare id keeps the primary endpoint and swaps the model.
	bare, err := config.Parse([]byte("roles:\n  context:\n    model: small-model\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err = newContextClient(st, bare, primary)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got.(*model.OpenAICompatClient); !ok || c.Model != "small-model" || c.BaseURL != primary.BaseURL || c.APIKey != "primary-key" {
		t.Fatalf("client = %+v", got)
	}
}
