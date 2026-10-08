package model

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

type legStub struct {
	id    string
	err   error
	calls int
}

func (l *legStub) ModelID() string { return l.id }
func (l *legStub) Complete(context.Context, CompletionRequest) (*CompletionResponse, error) {
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	return &CompletionResponse{Text: "{}"}, nil
}

func TestFailoverMovesOnAFailureTheNextModelMayNotShare(t *testing.T) {
	for name, err := range map[string]error{
		"runaway":     fmt.Errorf("%w: no answer", ErrRunaway),
		"retired":     &typedError{Code: "model_unavailable", Body: "HTTP 410", Terminal: true},
		"overflow":    fmt.Errorf("%w: output truncated", ErrDeterministic),
		"rate":        &typedError{Code: "rate_limited", Body: "HTTP 429"},
		"outage":      &typedError{Code: "provider_unavailable", Body: "HTTP 503"},
		"unreachable": fmt.Errorf("provider unreachable: %w", &url.Error{Op: "Post", URL: "x", Err: errors.New("dns")}),
	} {
		t.Run(name, func(t *testing.T) {
			a, b := &legStub{id: "a", err: err}, &legStub{id: "b"}
			var logs []string
			c := NewFailoverClient(a, []Client{b}, func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) })
			resp, gotErr := c.Complete(context.Background(), CompletionRequest{})
			if gotErr != nil {
				t.Fatalf("want the fallback to answer, got %v", gotErr)
			}
			if resp.Model != "b" || a.calls != 1 || b.calls != 1 {
				t.Fatalf("served by %q, calls a=%d b=%d", resp.Model, a.calls, b.calls)
			}
			if len(logs) != 2 {
				t.Fatalf("want the failover and the serving leg logged, got %q", logs)
			}
		})
	}
}

func TestFailoverStopsOnADeadlineOrCancel(t *testing.T) {
	for name, err := range map[string]error{
		"deadline": fmt.Errorf("%w (per-call deadline was 60s)", ErrDeadline),
		"canceled": context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			a, b := &legStub{id: "a", err: err}, &legStub{id: "b"}
			_, gotErr := NewFailoverClient(a, []Client{b}, nil).Complete(context.Background(), CompletionRequest{})
			if !errors.Is(gotErr, err) || b.calls != 0 {
				t.Fatalf("err=%v b.calls=%d, want the first error and no fallback call", gotErr, b.calls)
			}
		})
	}
}

// Every leg failing keeps the last error's class, so the reviewer still
// recognises a runaway and records the right reason.
func TestFailoverExhaustedKeepsTheErrorClass(t *testing.T) {
	a := &legStub{id: "a", err: fmt.Errorf("%w: a", ErrRunaway)}
	b := &legStub{id: "b", err: fmt.Errorf("%w: b", ErrRunaway)}
	_, err := NewFailoverClient(a, []Client{b}, nil).Complete(context.Background(), CompletionRequest{})
	if !errors.Is(err, ErrRunaway) {
		t.Fatalf("want ErrRunaway through the chain, got %v", err)
	}
}

func TestNoFallbackIsThePrimaryItself(t *testing.T) {
	a := &legStub{id: "a"}
	if c := NewFailoverClient(a, nil, nil); c != Client(a) {
		t.Fatal("without a chain the primary must be used directly")
	}
}
