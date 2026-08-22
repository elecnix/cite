package publisher

import (
	"testing"
	"time"

	"github.com/elecnix/cite/internal/model"
)

func mkFinding(path, category, title, quote string) model.ValidatedFinding {
	f := model.ValidatedFinding{
		Finding: model.Finding{
			Category: model.Category(category),
			Title:    title,
			Evidence: []model.Evidence{{Line: 10, Quote: quote}},
		},
		Path:   path,
		Blocks: true,
	}
	f.Fingerprint = f.FingerprintOf()
	return f
}

// The invariant test §10 says is worth more than the argument: replay the
// same pull request twice and assert zero new threads.
func TestReplaySamePRTwicePostsZeroNewThreads(t *testing.T) {
	current := []model.ValidatedFinding{
		mkFinding("a.go", "crash", "Index out of range in loop", "for i := 0; i <= len(xs); i++"),
		mkFinding("b.go", "resource-leak", "File never closed on error path", "f, err := os.Open(p)"),
	}

	first := Reconcile(current, nil, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(first.CommentsToPost) != 2 {
		t.Fatalf("first run should post 2, got %d", len(first.CommentsToPost))
	}

	live := []LiveThread{
		{ID: 101, Fingerprint: first.CommentsToPost[0].Fingerprint, Path: "a.go"},
		{ID: 102, Fingerprint: first.CommentsToPost[1].Fingerprint, Path: "b.go"},
	}

	second := Reconcile(current, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(second.CommentsToPost) != 0 {
		t.Fatalf("INVARIANT BROKEN: replay posted %d new threads", len(second.CommentsToPost))
	}
	if len(second.ThreadsToResolve) != 0 || len(second.ThreadsToMinimise) != 0 {
		t.Fatalf("healthy threads must not be touched: resolve=%v minimise=%v",
			second.ThreadsToResolve, second.ThreadsToMinimise)
	}
}

func TestReplayWithDuplicateFindingsAndOrdinals(t *testing.T) {
	// Two byte-identical findings in one file share a fingerprint and are
	// told apart by their occurrence ordinal.
	current := []model.ValidatedFinding{
		mkFinding("a.go", "crash", "Nil dereference", "x.Method()"),
		mkFinding("a.go", "crash", "Nil dereference", "x.Method()"),
	}
	first := Reconcile(current, nil, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(first.CommentsToPost) != 2 {
		t.Fatalf("want 2 posts, got %d", len(first.CommentsToPost))
	}
	if first.CommentsToPost[0].Occurrence != 1 || first.CommentsToPost[1].Occurrence != 2 {
		t.Fatalf("occurrence ordinals not assigned: %d, %d",
			first.CommentsToPost[0].Occurrence, first.CommentsToPost[1].Occurrence)
	}
	live := []LiveThread{
		{ID: 7, Fingerprint: first.CommentsToPost[0].Fingerprint, Path: "a.go"},
		{ID: 9, Fingerprint: first.CommentsToPost[1].Fingerprint, Path: "a.go"},
	}
	second := Reconcile(current, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(second.CommentsToPost) != 0 {
		t.Fatalf("duplicate fingerprints must still match on replay, got %d new", len(second.CommentsToPost))
	}
}

func TestDismissalHonouredButListedInSuppressedByLedger(t *testing.T) {
	now := time.Now()
	f := mkFinding("a.go", "convention", "Prefer errors.Is", "err == target")
	var ledger DismissalLedger
	ledger.Add(f.Fingerprint, "o/r", "ada", string(AssocMember), now)

	plan := Reconcile([]model.ValidatedFinding{f}, nil, ledger,
		ReconcileOptions{Repository: "o/r", Now: now})

	if len(plan.CommentsToPost) != 0 {
		t.Fatalf("dismissed fingerprint was re-raised: %+v", plan.CommentsToPost)
	}
	if len(plan.SuppressedByLedger) != 1 || plan.SuppressedByLedger[0].Fingerprint != f.Fingerprint {
		t.Fatalf("dismissed finding must appear in SuppressedByLedger so the gate verdict is unchanged: %+v", plan.SuppressedByLedger)
	}

	// Repository scope: same fingerprint in another repo is NOT suppressed.
	plan = Reconcile([]model.ValidatedFinding{f}, nil, ledger,
		ReconcileOptions{Repository: "o/other", Now: now})
	if len(plan.CommentsToPost) != 1 || len(plan.SuppressedByLedger) != 0 {
		t.Fatal("a dismissal never crosses repositories")
	}

	// Expired dismissal stops suppressing.
	plan = Reconcile([]model.ValidatedFinding{f}, nil, ledger,
		ReconcileOptions{Repository: "o/r", Now: now.Add(LedgerExpiry + time.Second)})
	if len(plan.CommentsToPost) != 1 {
		t.Fatal("expired dismissals stop suppressing")
	}
}

func TestDisappearedFingerprintIsNotResolutionWithoutSpanGone(t *testing.T) {
	// The finding vanished between pushes. Without verification that the span
	// is gone from the file, the thread stays open: an attacker reformatting
	// the file must not clear a real finding.
	live := []LiveThread{{ID: 5, Fingerprint: "gone-fp", Path: "secret.go"}}
	plan := Reconcile(nil, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(plan.ThreadsToResolve) != 0 {
		t.Fatal("a merely-disappeared fingerprint must never be resolved")
	}

	spanGoneSaysNo := func(LiveThread) bool { return false }
	plan = Reconcile(nil, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r", SpanGone: spanGoneSaysNo})
	if len(plan.ThreadsToResolve) != 0 {
		t.Fatal("unverified span must not resolve")
	}

	verifiedGone := func(LiveThread) bool { return true }
	plan = Reconcile(nil, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r", SpanGone: verifiedGone})
	if len(plan.ThreadsToResolve) != 1 || plan.ThreadsToResolve[0] != 5 {
		t.Fatalf("verified-gone span should resolve thread 5, got %v", plan.ThreadsToResolve)
	}
}

func TestOutdatedThreadsMinimisedWhenStillMatched(t *testing.T) {
	f := mkFinding("a.go", "crash", "Off by one", "i <= n")
	live := []LiveThread{{ID: 42, Fingerprint: f.Fingerprint, Path: "a.go", IsOutdated: true}}
	plan := Reconcile([]model.ValidatedFinding{f}, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(plan.CommentsToPost) != 0 {
		t.Fatal("matched finding must not repost")
	}
	if len(plan.ThreadsToMinimise) != 1 || plan.ThreadsToMinimise[0] != 42 {
		t.Fatalf("outdated matched thread should be minimised, got %v", plan.ThreadsToMinimise)
	}
}

func TestHumanResolvedThreadLeftAlone(t *testing.T) {
	live := []LiveThread{{ID: 3, Fingerprint: "fp-x", Path: "a.go", ResolvedByHuman: true}}
	verifiedGone := func(LiveThread) bool { return true }
	plan := Reconcile(nil, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r", SpanGone: verifiedGone})
	if len(plan.ThreadsToResolve) != 0 || len(plan.ThreadsToMinimise) != 0 {
		t.Fatal("human-resolved threads are never touched again")
	}
}

func TestExactMatchSurvivesRename(t *testing.T) {
	// Path is a locator, not part of the fingerprint: a rename must not
	// orphan the thread or re-raise the finding.
	f := mkFinding("new/path.go", "crash", "Off by one", "i <= n")
	live := []LiveThread{{ID: 11, Fingerprint: f.Fingerprint, Path: "old/path.go"}}
	plan := Reconcile([]model.ValidatedFinding{f}, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(plan.CommentsToPost) != 0 {
		t.Fatal("rename must not re-raise an exactly-matched finding")
	}
}

func TestFuzzyMatchCatchesReformat(t *testing.T) {
	// Same finding after a reformat churns the fingerprint (quotes differ in
	// punctuation/whitespace). Category matches and token similarity of
	// evidence+title is above 0.6, so the fuzzy fallback pairs them instead
	// of posting a duplicate.
	RegisterThreadText("churned-fp", model.CategoryCrash,
		[]string{"for i := 0; i <= len(items); i++ {", "total += items[i]"},
		"Loop iterates one past the end of items")
	churned := model.ValidatedFinding{
		Finding: model.Finding{
			Category: model.CategoryCrash,
			Title:    "Loop iterates one past the end of items",
			Evidence: []model.Evidence{
				{Line: 12, Quote: "for i:=0; i<=len(items); i++ {"},
				{Line: 13, Quote: "total += items[i]"},
			},
		},
		Path:   "calc.go",
		Blocks: true,
	}
	churned.Fingerprint = churned.FingerprintOf() // different from "churned-fp"

	live := []LiveThread{{ID: 77, Fingerprint: "churned-fp", Path: "calc.go"}}
	plan := Reconcile([]model.ValidatedFinding{churned}, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(plan.CommentsToPost) != 0 {
		t.Fatalf("reformat should fuzzy-match, not duplicate-post: %+v", plan.CommentsToPost)
	}

	// And when the reformatted finding disappears entirely, the fuzzy match
	// means the thread is only resolvable via verified SpanGone — the
	// fingerprint churn did not silently clear it.
	plan = Reconcile(nil, live, DismissalLedger{}, ReconcileOptions{Repository: "o/r"})
	if len(plan.ThreadsToResolve) != 0 {
		t.Fatal("no SpanGone, no resolution")
	}
}

func TestJaccardSimilarity(t *testing.T) {
	a := tokensOf("alpha beta gamma")
	b := tokensOf("alpha beta delta")
	if s := jaccard(a, b); s < 0.4 || s > 0.6 {
		t.Fatalf("jaccard(alpha,beta overlap) = %f, want 0.5", s)
	}
	if s := jaccard(a, tokensOf("x y z")); s != 0 {
		t.Fatalf("disjoint sets must score 0, got %f", s)
	}
	if s := jaccard(map[string]bool{}, a); s != 0 {
		t.Fatal("empty set scores 0, never 1")
	}
	if s := jaccard(a, tokensOf("alpha beta gamma")); s != 1 {
		t.Fatalf("identical sets must score 1, got %f", s)
	}
}
