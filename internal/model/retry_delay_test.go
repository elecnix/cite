package model

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRetryDelayHonoursRetryAfter(t *testing.T) {
	ts := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()
	_, err := (&OpenAICompatClient{BaseURL: ts.URL, Model: "m"}).Complete(context.Background(), CompletionRequest{MaxOutputTokens: 8})
	if got := RetryDelay(err, 0); got != 7*time.Second {
		t.Fatalf("RetryDelay = %s, want the provider's 7s", got)
	}
	if got := RetryDelay(&typedError{Code: "rate_limited", RetryAfter: time.Hour}, 0); got != maxRetryDelay {
		t.Fatalf("a Retry-After past the cap waits %s, want %s", got, maxRetryDelay)
	}
}

// Without Retry-After, a rate limit backs off from 5s and an outage from 2s,
// doubling per attempt with at most a fifth of jitter.
func TestRetryDelayBacksOff(t *testing.T) {
	cases := []struct {
		err     error
		attempt int
		lo, hi  time.Duration
	}{
		{&typedError{Code: "rate_limited"}, 0, 5 * time.Second, 6 * time.Second},
		{&typedError{Code: "rate_limited"}, 2, 20 * time.Second, 24 * time.Second},
		{&typedError{Code: "provider_unavailable"}, 0, 2 * time.Second, 2400 * time.Millisecond},
		{errors.New("provider unreachable: dial"), 1, 4 * time.Second, 4800 * time.Millisecond},
		{&typedError{Code: "rate_limited"}, 9, maxRetryDelay, maxRetryDelay},
	}
	for _, tc := range cases {
		for i := 0; i < 20; i++ {
			if d := RetryDelay(tc.err, tc.attempt); d < tc.lo || d > tc.hi {
				t.Fatalf("RetryDelay(%v, %d) = %s, want within [%s, %s]", tc.err, tc.attempt, d, tc.lo, tc.hi)
			}
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("12"); d != 12*time.Second {
		t.Fatalf("seconds: %s", d)
	}
	if d := parseRetryAfter(time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)); d < 25*time.Second || d > 31*time.Second {
		t.Fatalf("http date: %s", d)
	}
	for _, v := range []string{"", "soon", "-3", "0"} {
		if d := parseRetryAfter(v); d != 0 {
			t.Fatalf("%q: %s, want 0", v, d)
		}
	}
}
