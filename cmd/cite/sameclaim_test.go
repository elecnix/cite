package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

type scriptedClient struct {
	answers []string
	err     error
	reqs    []model.CompletionRequest
}

func (c *scriptedClient) Complete(_ context.Context, req model.CompletionRequest) (*model.CompletionResponse, error) {
	c.reqs = append(c.reqs, req)
	if c.err != nil {
		return nil, c.err
	}
	a := c.answers[0]
	if len(c.answers) > 1 {
		c.answers = c.answers[1:]
	}
	return &model.CompletionResponse{Text: a}, nil
}

func (c *scriptedClient) ModelID() string { return "scripted" }

func sameClaimFixture() (model.ValidatedFinding, publisher.LiveThread, map[int64]*threadFinding) {
	f := model.ValidatedFinding{Path: "a.go", Finding: model.Finding{
		Category: "auth-bypass", Title: "Sandbox mode still inherits the secret",
		Evidence: []model.Evidence{{Line: 317, Quote: "if skip[name] || pinned[name] {"}},
	}}
	th := publisher.LiveThread{ID: 9, Path: "a.go", ResolvedByHuman: true}
	data := map[int64]*threadFinding{9: {Path: "a.go", Category: "logic-inversion", Title: "Inherited secret still reaches the child process",
		Evidence: []model.Evidence{{Line: 311, Quote: "if skip[name] || pinned[name] {"}}}}
	return f, th, data
}

func TestSameClaimPromptCarriesBothClaimsWithoutJudgeFraming(t *testing.T) {
	f, _, data := sameClaimFixture()
	p := sameClaimPrompt(f, data[9])
	for _, want := range []string{"auth-bypass", "Sandbox mode still inherits the secret", "logic-inversion",
		"Inherited secret still reaches the child process", "if skip[name] || pinned[name] {", `{"same": true}`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, framing := range []string{"severity", "reviewer found", "blocking"} {
		if strings.Contains(strings.ToLower(p), framing) {
			t.Errorf("prompt carries judge framing %q", framing)
		}
	}
}

func TestParseSameClaim(t *testing.T) {
	for raw, want := range map[string][2]bool{
		`{"same": true}`:                 {true, true},
		"```json\n{\"same\":false}\n```": {false, true},
		`{"same": "yes"}`:                {false, false},
		`no`:                             {false, false},
	} {
		same, ok := parseSameClaim(raw)
		if same != want[0] || ok != want[1] {
			t.Errorf("parseSameClaim(%q) = %v, %v; want %v, %v", raw, same, ok, want[0], want[1])
		}
	}
}

func TestSameClaimMatcherAnswersFromTheModel(t *testing.T) {
	f, th, data := sameClaimFixture()
	c := &scriptedClient{answers: []string{`{"same": true}`}}
	if !newSameClaimMatcher(context.Background(), c, data, 5)(f, th) {
		t.Fatal("a yes from the model did not match")
	}
	if c.reqs[0].Temperature != 0 {
		t.Errorf("temperature = %v, want 0 for a classification", c.reqs[0].Temperature)
	}
}

// Every failure answers false, so the finding posts.
func TestSameClaimMatcherFailsTowardPosting(t *testing.T) {
	f, th, data := sameClaimFixture()
	ctx := context.Background()
	if newSameClaimMatcher(ctx, &scriptedClient{err: errors.New("503")}, data, 5)(f, th) {
		t.Error("a provider error matched")
	}
	if newSameClaimMatcher(ctx, &scriptedClient{answers: []string{"sure"}}, data, 5)(f, th) {
		t.Error("an unparseable answer matched")
	}
	c := &scriptedClient{answers: []string{`{"same": true}`}}
	if newSameClaimMatcher(ctx, c, map[int64]*threadFinding{}, 5)(f, th) || len(c.reqs) != 0 {
		t.Error("a thread without parsed claim data was sent to the model or matched")
	}
}

// The call budget bounds the run's spend: past it, the answer is false.
func TestSameClaimMatcherStopsAtItsBudget(t *testing.T) {
	f, th, data := sameClaimFixture()
	c := &scriptedClient{answers: []string{`{"same": true}`}}
	m := newSameClaimMatcher(context.Background(), c, data, 2)
	got := []bool{m(f, th), m(f, th), m(f, th)}
	if !got[0] || !got[1] || got[2] || len(c.reqs) != 2 {
		t.Fatalf("answers %v after %d calls, want [true true false] after 2", got, len(c.reqs))
	}
}
