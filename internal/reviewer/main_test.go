package reviewer

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMain skips the retry backoff: the tests script transient failures by
// the dozen and assert on outcomes, not on wall-clock waits.
func TestMain(m *testing.M) {
	retrySleep = func(context.Context, time.Duration) error { return nil }
	os.Exit(m.Run())
}
