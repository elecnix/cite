package retry

import (
	"testing"
	"time"
)

func TestNextAttempt(t *testing.T) {
	p := Policy{Interval: 10 * time.Second, Max: 3}
	start := time.Unix(1000, 0)
	if got, want := p.NextAttempt(start, 1), start.Add(10*time.Second); !got.Equal(want) {
		t.Errorf("first retry: got %v, want %v", got, want)
	}
	if got, want := p.NextAttempt(start, 3), start.Add(30*time.Second); !got.Equal(want) {
		t.Errorf("third retry: got %v, want %v", got, want)
	}
}
