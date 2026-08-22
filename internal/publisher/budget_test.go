package publisher

import (
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/model"
)

func TestCommentBudget(t *testing.T) {
	cases := []struct {
		name         string
		changedLines int
		cfgMax       int
		want         int
	}{
		{"tiny pr floors at 3", 40, 0, 3},
		{"zero lines floors at 3", 0, 0, 3},
		{"exactly 250 lines buys one more", 250, 0, 4},
		{"249 does not", 249, 0, 3},
		{"2000 lines hits the ceiling", 2000, 0, 10},
		{"5000 lines still capped at 10", 5000, 0, 10},
		{"config can shrink below formula", 100000, 5, 5},
		{"config capped at schema maximum", 1 << 20, 999, 10},
		{"negative lines treated as zero", -5, 0, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CommentBudget(tc.changedLines, tc.cfgMax); got != tc.want {
				t.Fatalf("CommentBudget(%d, %d) = %d, want %d", tc.changedLines, tc.cfgMax, got, tc.want)
			}
		})
	}
}

func findingAt(path, title string, i int) model.ValidatedFinding {
	f := model.ValidatedFinding{
		Finding: model.Finding{
			Category: model.CategoryCrash,
			Title:    title,
			Evidence: []model.Evidence{{Line: i, Quote: "x := y[i]"}},
		},
		Path:   path,
		Blocks: true,
	}
	f.Fingerprint = f.FingerprintOf()
	return f
}

func TestAllocateBudgetRespectsPerFileCap(t *testing.T) {
	ranked := []model.ValidatedFinding{
		findingAt("a.go", "first", 1),
		findingAt("a.go", "second", 2),
		findingAt("a.go", "third", 3),
		findingAt("b.go", "fourth", 4),
	}
	chosen, drops := AllocateBudget(ranked, 10, MaxPerFile)

	if len(chosen) != 3 {
		t.Fatalf("chosen = %d findings, want 3 (two in a.go, one in b.go)", len(chosen))
	}
	if chosen[0].Title != "first" || chosen[1].Title != "second" || chosen[2].Path != "b.go" {
		t.Fatalf("wrong findings chosen: %+v", chosen)
	}
	if len(drops) != 1 || drops[0].Entry.Reason != model.DropPerFileBudget {
		t.Fatalf("want exactly one per-file-budget drop, got %+v", drops)
	}
	if drops[0].Finding.Title != "third" {
		t.Fatalf("dropped the wrong finding: %+v", drops[0])
	}
}

func TestAllocateBudgetTotalCap(t *testing.T) {
	ranked := []model.ValidatedFinding{
		findingAt("a.go", "one", 1),
		findingAt("b.go", "two", 2),
		findingAt("c.go", "three", 3),
	}
	chosen, drops := AllocateBudget(ranked, 2, MaxPerFile)
	if len(chosen) != 2 {
		t.Fatalf("chosen = %d, want 2", len(chosen))
	}
	if len(drops) != 1 || drops[0].Entry.Reason != model.DropBudget {
		t.Fatalf("want one comment_budget drop, got %+v", drops)
	}
	for _, d := range drops {
		if d.Entry.Path == "" || !strings.Contains(d.Entry.Detail, "budget") {
			t.Errorf("drop entry missing context: %+v", d.Entry)
		}
	}
}

func TestAllocateBudgetNoCutWhenRoom(t *testing.T) {
	ranked := []model.ValidatedFinding{findingAt("a.go", "one", 1), findingAt("b.go", "two", 2)}
	chosen, drops := AllocateBudget(ranked, 10, MaxPerFile)
	if len(chosen) != 2 || len(drops) != 0 {
		t.Fatalf("expected no cuts, got chosen=%d drops=%+v", len(chosen), drops)
	}
}

func TestLedgerDismissalLifecycle(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var l DismissalLedger
	l.Add("fp1", "o/r", "alice", string(AssocMember), now)

	if !l.Active("fp1", "o/r", now.Add(time.Hour)) {
		t.Fatal("fresh dismissal should be active")
	}
	if l.Active("fp1", "o/other", now) {
		t.Fatal("dismissal must not cross repositories")
	}
	if l.Active("fp2", "o/r", now) {
		t.Fatal("unknown fingerprint must not be active")
	}
	if l.Active("fp1", "o/r", now.Add(LedgerExpiry).Add(time.Second)) {
		t.Fatal("expired dismissal must stop suppressing after 90 days")
	}
	if l.Active("fp1", "o/r", now.Add(LedgerExpiry)) {
		t.Fatal("exactly at expiry the dismissal is gone")
	}

	l.Prune(now.Add(LedgerExpiry + time.Minute))
	if len(l.Entries) != 0 {
		t.Fatalf("prune should drop expired entries, kept %d", len(l.Entries))
	}
}

func TestCanDismiss(t *testing.T) {
	cases := []struct {
		name        string
		author      string
		association string
		isPRAuthor  bool
		wantAllowed bool
	}{
		{"owner may dismiss", "ada", "OWNER", false, true},
		{"member may dismiss", "ada", "MEMBER", false, true},
		{"collaborator may dismiss", "ada", "COLLABORATOR", false, true},
		{"contributor may dismiss", "ada", "CONTRIBUTOR", false, true},
		{"plain none may dismiss", "ada", "NONE", false, true},
		{"first-time contributor may not", "newbie", "FIRST_TIME_CONTRIBUTOR", false, false},
		{"first timer may not", "newbie", "FIRST_TIMER", false, false},
		{"pr author never, even an owner", "ada", "OWNER", true, false},
		{"unknown association rejected", "ghost", "MASCOT", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CanDismiss(tc.author, tc.association, tc.isPRAuthor)
			if got := err == nil; got != tc.wantAllowed {
				t.Fatalf("CanDismiss(%q,%q,%v) allowed=%v, want %v (err=%v)", tc.author, tc.association, tc.isPRAuthor, got, tc.wantAllowed, err)
			}
		})
	}
}

func TestLedgerBlobRoundTrip(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 30, 0, 0, time.UTC)
	var l DismissalLedger
	l.Add("abc123", "o/r", "grace", string(AssocOwner), now)
	l.addPublished("def456", "o/r", now)

	blob, err := l.MarshalBlob()
	if err != nil {
		t.Fatalf("MarshalBlob: %v", err)
	}
	if strings.Contains(blob, "grace") {
		t.Fatal("blob should be opaque base64, not plaintext")
	}
	back, err := UnmarshalBlob(blob)
	if err != nil {
		t.Fatalf("UnmarshalBlob: %v", err)
	}
	if len(back.Entries) != 2 {
		t.Fatalf("round trip lost entries: %+v", back)
	}
	e := back.Entries[0]
	if e.Fingerprint != "abc123" || e.Repository != "o/r" || e.AdjudicatorAuthor != "grace" ||
		e.AuthorAssociation != string(AssocOwner) || !e.DismissedAt.Equal(now) {
		t.Fatalf("entry did not survive round trip: %+v", e)
	}
	if !back.Active("abc123", "o/r", now.Add(time.Minute)) {
		t.Fatal("round-tripped dismissal lost its activity")
	}
	if back.Active("def456", "o/r", now.Add(time.Minute)) {
		t.Fatal("published entries must never suppress")
	}
	if _, err := UnmarshalBlob("!!!not base64!!!"); err == nil {
		t.Fatal("corrupt blob must error so callers rebuild from threads")
	}
}

func TestRebuildFromThreadsRegistersPublishedOnly(t *testing.T) {
	now := time.Now()
	threads := []LiveThread{
		{ID: 1, Fingerprint: "fp-a", Path: "a.go"},
		{ID: 2, Fingerprint: "fp-b", Path: "b.go"},
		{ID: 3, Fingerprint: "fp-a", Path: "a.go"}, // duplicate fingerprint
	}
	l := RebuildFromThreads("o/r", threads, now)
	if len(l.Entries) != 2 {
		t.Fatalf("rebuild should dedupe fingerprints, got %d entries", len(l.Entries))
	}
	if !l.Published("fp-a", "o/r") || !l.Published("fp-b", "o/r") {
		t.Fatal("rebuild must register live fingerprints as published")
	}
	if l.Active("fp-a", "o/r", now) || l.Active("fp-b", "o/r", now) {
		t.Fatal("rebuilt published marks are not dismissals and must never suppress")
	}
}
