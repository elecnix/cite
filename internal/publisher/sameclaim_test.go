package publisher

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// Issue #168: the same claim, reworded and filed under another category,
// after a human resolved its thread while the cited code still stands.
func reworded() model.ValidatedFinding {
	return mkFinding("a.go", "auth-bypass", "Sandbox mode still inherits the secret from the shell", `if skip[name] || pinned[name] {`)
}

func resolvedThread() LiveThread {
	return LiveThread{ID: 40, Fingerprint: "fp-old", Path: "a.go", ResolvedByHuman: true, IsOutdated: true}
}

func spanStands(LiveThread) bool { return false }
func spanGone(LiveThread) bool   { return true }

func TestSameClaimOnResolvedThreadIsNotRefiled(t *testing.T) {
	var asked []int64
	same := func(_ model.ValidatedFinding, th LiveThread) bool { asked = append(asked, th.ID); return true }
	plan := Reconcile([]model.ValidatedFinding{reworded()}, []LiveThread{resolvedThread()}, DismissalLedger{},
		ReconcileOptions{Repository: "o/r", SpanGone: spanStands, SameClaim: same})

	if len(plan.CommentsToPost) != 0 {
		t.Fatalf("a claim matching a resolved thread was re-filed: %+v", plan.CommentsToPost)
	}
	if len(plan.MatchedResolved) != 1 {
		t.Fatalf("MatchedResolved = %+v, want the finding listed so the gate verdict is unchanged", plan.MatchedResolved)
	}
	if len(plan.ThreadsToResolve) != 0 || len(plan.ThreadsToMinimise) != 0 {
		t.Fatalf("a human-resolved thread was touched: resolve=%v minimise=%v", plan.ThreadsToResolve, plan.ThreadsToMinimise)
	}
	if len(asked) != 1 || asked[0] != 40 {
		t.Fatalf("SameClaim asked about %v, want [40]", asked)
	}
}

// #48's guard: once the code the human looked at is gone, a re-raise may be
// a genuine new occurrence, and it posts.
func TestSameClaimOnResolvedThreadPostsOnceItsSpanIsGone(t *testing.T) {
	same := func(model.ValidatedFinding, LiveThread) bool { return true }
	for name, gone := range map[string]func(LiveThread) bool{"span gone": spanGone, "span unverifiable": nil} {
		t.Run(name, func(t *testing.T) {
			plan := Reconcile([]model.ValidatedFinding{reworded()}, []LiveThread{resolvedThread()}, DismissalLedger{},
				ReconcileOptions{Repository: "o/r", SpanGone: gone, SameClaim: same})
			if len(plan.CommentsToPost) != 1 || len(plan.MatchedResolved) != 0 {
				t.Fatalf("post=%d matchedResolved=%d, want the finding posted", len(plan.CommentsToPost), len(plan.MatchedResolved))
			}
		})
	}
}

// An exact fingerprint on a resolved thread needs no model call.
func TestExactFingerprintOnResolvedThreadIsNotRefiled(t *testing.T) {
	f := reworded()
	th := resolvedThread()
	th.Fingerprint = f.Fingerprint
	plan := Reconcile([]model.ValidatedFinding{f}, []LiveThread{th}, DismissalLedger{},
		ReconcileOptions{Repository: "o/r", SpanGone: spanStands})
	if len(plan.CommentsToPost) != 0 || len(plan.MatchedResolved) != 1 {
		t.Fatalf("post=%d matchedResolved=%d, want 0 and 1", len(plan.CommentsToPost), len(plan.MatchedResolved))
	}
}

// A reworded claim joins its open thread: nothing posts, nothing resolves.
func TestSameClaimOnOpenThreadKeepsTheThread(t *testing.T) {
	open := LiveThread{ID: 41, Fingerprint: "fp-old", Path: "a.go"}
	same := func(model.ValidatedFinding, LiveThread) bool { return true }
	plan := Reconcile([]model.ValidatedFinding{reworded()}, []LiveThread{open}, DismissalLedger{},
		ReconcileOptions{Repository: "o/r", SpanGone: spanGone, SameClaim: same,
			ReReviewedFresh: func(LiveThread) bool { return true }})
	if len(plan.CommentsToPost) != 0 || len(plan.ThreadsToResolve) != 0 {
		t.Fatalf("post=%v resolve=%v, want the open thread kept for the reworded claim", plan.CommentsToPost, plan.ThreadsToResolve)
	}
}

// SameClaim is asked only about threads on the finding's own file, and a
// "no" posts the finding.
func TestSameClaimAskedOnlyOnTheSamePath(t *testing.T) {
	other := LiveThread{ID: 50, Fingerprint: "fp-b", Path: "b.go"}
	here := LiveThread{ID: 51, Fingerprint: "fp-a", Path: "a.go"}
	var asked []int64
	same := func(_ model.ValidatedFinding, th LiveThread) bool { asked = append(asked, th.ID); return false }
	plan := Reconcile([]model.ValidatedFinding{reworded()}, []LiveThread{other, here}, DismissalLedger{},
		ReconcileOptions{Repository: "o/r", SpanGone: spanStands, SameClaim: same})
	if len(asked) != 1 || asked[0] != 51 {
		t.Fatalf("SameClaim asked about %v, want only [51]", asked)
	}
	if len(plan.CommentsToPost) != 1 {
		t.Fatalf("a finding no thread claims must post, got %d", len(plan.CommentsToPost))
	}
}

// A claim that matches an open thread and an older resolved one keeps the
// open thread. Matching the resolved one first would leave the open thread
// unused, and the resolve pass would close it while the claim still stands.
func TestSameClaimPrefersTheOpenThread(t *testing.T) {
	open := LiveThread{ID: 41, Fingerprint: "fp-open", Path: "a.go"}
	same := func(model.ValidatedFinding, LiveThread) bool { return true }
	plan := Reconcile([]model.ValidatedFinding{reworded()}, []LiveThread{resolvedThread(), open}, DismissalLedger{},
		ReconcileOptions{Repository: "o/r", SpanGone: spanStands, SameClaim: same,
			ReReviewedFresh: func(LiveThread) bool { return true }})
	if len(plan.ThreadsToResolve) != 0 {
		t.Fatalf("the open thread was resolved while its claim stands: %v", plan.ThreadsToResolve)
	}
	if len(plan.CommentsToPost) != 0 || len(plan.MatchedResolved) != 0 {
		t.Fatalf("post=%d matchedResolved=%d, want the finding kept on the open thread", len(plan.CommentsToPost), len(plan.MatchedResolved))
	}
}
