package reviewer

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// prefixCacheClient simulates a provider prompt cache over the bytes Cite
// actually sends. A request's cacheable prefixes are the system prompt and,
// when the user message carries the cache breakpoint, the system prompt plus
// the user message up to and including the breakpoint. A call reads the
// longest prefix an earlier call wrote and writes the rest of its longest
// prefix. Tokens are bytes/4. Nothing here is scripted per call: the counters
// follow from the real segment sizes, so a volatile byte in a shared segment
// shows up as a miss.
type prefixCacheClient struct {
	inner    *fakeClient
	provider string

	mu     sync.Mutex
	cached map[string]bool
}

func cacheablePrefixes(req model.CompletionRequest) []string {
	out := []string{req.System}
	if i := strings.Index(req.User, cacheBreakpoint); i >= 0 {
		out = append(out, req.System+req.User[:i+len(cacheBreakpoint)])
	}
	return out
}

func (c *prefixCacheClient) Complete(ctx context.Context, req model.CompletionRequest) (*model.CompletionResponse, error) {
	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached == nil {
		c.cached = map[string]bool{}
	}
	read, longest := 0, 0
	for _, p := range cacheablePrefixes(req) {
		if c.cached[p] && len(p) > read {
			read = len(p)
		}
		if len(p) > longest {
			longest = len(p)
		}
		c.cached[p] = true
	}
	resp.Usage = model.Usage{
		InputTokens:      (len(req.System) + len(req.User)) / 4,
		OutputTokens:     10,
		CacheReadTokens:  read / 4,
		CacheWriteTokens: (longest - read) / 4,
	}
	resp.Provider = c.provider
	return resp, nil
}

func (c *prefixCacheClient) ModelID() string { return c.inner.ModelID() }

// threeFileInputs is a run of three modified files, so the run makes one
// triage call and three review calls.
func threeFileInputs(t *testing.T) Inputs {
	t.Helper()
	in := baseInputs()
	for _, p := range []string{"b.go", "c.go"} {
		diffText := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,2 +1,3 @@\n context line one\n+added line alpha here\n context line two\n", p, p, p, p)
		df, err := scope.ParseUnifiedDiff(diffText)
		if err != nil {
			t.Fatal(err)
		}
		in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "M", Path: p, Adds: 1})
		in.Diffs[p] = df.Files[0]
		in.PostImage[p] = []byte("context line one\nadded line alpha here\ncontext line two\n")
	}
	return in
}

func cleanScript(flags ...string) *fakeClient {
	return &fakeClient{fn: defaultScript(flags...)}
}

// §7: "assert on the cache counters in CI, since caching failure is silent."
// The run's ceiling is computed from the prompts it sent; this test also
// computes it independently from the segment sizes (system prompt, segment B
// plus the breakpoint, each file's payload), so a change that makes a shared
// segment volatile lowers the ceiling and fails here. With a cache that
// serves every prefix it has seen, the measured rate must reach the ceiling.
func TestCacheHitRateReachesCeilingFromRealSegmentSizes(t *testing.T) {
	in := threeFileInputs(t)
	inner := cleanScript("a.go", "b.go", "c.go")
	client := &prefixCacheClient{inner: inner}
	rec, err := runOnce(t, in, baseOptions(client))
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.calls) != 4 {
		t.Fatalf("calls = %d, want 1 triage + 3 reviews", len(inner.calls))
	}

	// Independent expectation from the segment sizes.
	r := New(baseOptions(client))
	prefix := len(systemPrompt()) + len(r.contextSegment(&in)) + len(cacheBreakpoint)
	var input, cacheable float64
	firstReview := true
	for _, req := range inner.calls {
		tokens := float64((len(req.System) + len(req.User)) / 4)
		input += tokens
		if isTriageCall(req) {
			continue
		}
		if firstReview {
			firstReview = false
			continue
		}
		cacheable += tokens * float64(prefix) / float64(len(req.System)+len(req.User))
	}
	want := cacheable / input
	if want < 0.3 {
		t.Fatalf("segment-size ceiling %.3f is implausibly low; the fixture no longer exercises the shared prefix", want)
	}
	if math.Abs(rec.CacheCeiling-want) > 0.01 {
		t.Fatalf("recorded ceiling %.3f, want %.3f from the segment sizes", rec.CacheCeiling, want)
	}
	rate := rec.Usage.CacheHitRate()
	if math.Abs(rate-rec.CacheCeiling) > 0.01 {
		t.Fatalf("hit rate %.3f with a perfect cache, want the ceiling %.3f", rate, rec.CacheCeiling)
	}
	if model.CacheShortfall(rate, rec.CacheCeiling) {
		t.Fatalf("hit rate %.3f flagged short of ceiling %.3f", rate, rec.CacheCeiling)
	}
}

// Every call entry carries its own cache counters, the upstream provider and
// the prefix measurements the ceiling is computed from.
func TestCallLogCarriesCacheCountersProviderAndPrefix(t *testing.T) {
	in := threeFileInputs(t)
	client := &prefixCacheClient{inner: cleanScript("a.go"), provider: "Wafer"}
	rec, err := runOnce(t, in, baseOptions(client))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Calls) != 4 {
		t.Fatalf("call log = %d entries, want 4", len(rec.Calls))
	}
	var reads, writes int
	prefixIDs := map[string]int{}
	for _, c := range rec.Calls {
		if c.Provider != "Wafer" {
			t.Errorf("%s call provider = %q, want Wafer", c.Unit, c.Provider)
		}
		if c.PrefixID == "" || c.PrefixBytes <= 0 || c.PromptBytes <= c.PrefixBytes {
			t.Errorf("%s call prefix = %q %d/%d bytes, want an id and a prefix shorter than the prompt", c.Unit, c.PrefixID, c.PrefixBytes, c.PromptBytes)
		}
		prefixIDs[c.PrefixID]++
		reads += c.CacheReadTokens
		writes += c.CacheWriteTokens
	}
	if reads != rec.Usage.CacheReadTokens || writes != rec.Usage.CacheWriteTokens {
		t.Fatalf("per-call cache counters %d/%d do not sum to the run's %d/%d", reads, writes, rec.Usage.CacheReadTokens, rec.Usage.CacheWriteTokens)
	}
	if reads == 0 {
		t.Fatal("no call recorded a cache read")
	}
	if len(prefixIDs) != 2 {
		t.Fatalf("prefix groups = %v, want 2 (triage, review)", prefixIDs)
	}
}

// Sticky routing: every call of one run carries the same session id, and a
// second run carries a different one.
func TestEveryCallInARunSharesOneSessionID(t *testing.T) {
	sessions := func() map[string]bool {
		inner := cleanScript("a.go")
		if _, err := runOnce(t, threeFileInputs(t), baseOptions(inner)); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, req := range inner.calls {
			ids[req.SessionID] = true
		}
		return ids
	}
	first, second := sessions(), sessions()
	if len(first) != 1 || first[""] {
		t.Fatalf("session ids in one run = %v, want exactly one non-empty id", first)
	}
	for id := range first {
		if second[id] {
			t.Fatalf("two runs share session id %q", id)
		}
	}
}
