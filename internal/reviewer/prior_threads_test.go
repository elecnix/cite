package reviewer

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/scope"
)

// Issue #168: the review call for a file carries that file's earlier Cite
// threads and their replies, after the cache breakpoint, so segment B stays
// byte-identical across the run's calls.
func TestReviewCallCarriesPriorThreadsForItsFile(t *testing.T) {
	c := &fakeClient{fn: defaultScript()}
	in := baseInputs()
	in.PriorThreads = map[string][]scope.PriorThread{
		"a.go": {{
			ID: 42, Category: "logic-inversion", Title: "alpha is inverted",
			StartLine: 2, EndLine: 2,
			Replies: []scope.PriorReply{{Author: "maintainer", Body: "False positive, see the test."}},
		}},
		"other.go": {{ID: 7, Title: "not this file"}},
	}
	if _, err := runOnce(t, in, baseOptions(c)); err != nil {
		t.Fatal(err)
	}
	var review string
	for _, call := range c.calls {
		if !isTriageCall(call) && requestPath(call) == "a.go" {
			review = call.User
		}
	}
	if review == "" {
		t.Fatal("no review call for a.go")
	}
	parts := strings.SplitN(review, cacheBreakpoint, 2)
	if len(parts) != 2 {
		t.Fatal("review call has no cache breakpoint")
	}
	if strings.Contains(parts[0], "prior_threads") {
		t.Error("prior threads leaked into segment B, which must be identical across files")
	}
	for _, want := range []string{
		`<prior_threads path="a.go"`,
		"| thread=42 category=logic-inversion lines=2-2 resolved=false",
		"| claim: alpha is inverted",
		"|   False positive, see the test.",
	} {
		if !strings.Contains(parts[1], want) {
			t.Errorf("payload missing %q:\n%s", want, parts[1])
		}
	}
	if strings.Contains(parts[1], "not this file") {
		t.Error("another file's thread reached this file's review call")
	}
}
