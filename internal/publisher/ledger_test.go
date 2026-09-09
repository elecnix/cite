package publisher

import (
	"testing"
	"time"
)

func TestAddAcceptedRoundtripAndSemantics(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	var l DismissalLedger
	l.Add("fp1", "o/r", "alice", "MEMBER", now)
	l.AddAcceptedFixed("fp2", "o/r", now)

	if got := len(l.Entries); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	if k := entryKind(l.Entries[1]); k != EntryAcceptedFixed {
		t.Fatalf("kind = %q, want %q", k, EntryAcceptedFixed)
	}

	// An accepted-and-fixed record is a metric, not an adjudication that
	// suppresses anything: the code changed under the finding, so there is
	// nothing to suppress.
	if l.Active("fp2", "o/r", now) {
		t.Fatal("accepted-and-fixed must never suppress re-raising")
	}
	if !l.Active("fp1", "o/r", now.Add(time.Hour)) {
		t.Fatal("dismissal should still be active")
	}

	blob, err := l.MarshalBlob()
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Entries) != 2 || entryKind(back.Entries[1]) != EntryAcceptedFixed {
		t.Fatalf("blob roundtrip lost the accepted-fixed kind: %+v", back.Entries)
	}
}

// Prune keeps accepted-and-fixed entries: they are the denominator of the
// accept-rate signal and expire with nothing.
func TestPruneKeepsAcceptedFixed(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	var l DismissalLedger
	l.Add("fp1", "o/r", "alice", "MEMBER", now)
	l.AddAcceptedFixed("fp2", "o/r", now)

	l.Prune(now.Add(LedgerExpiry + time.Hour))
	if len(l.Entries) != 1 {
		t.Fatalf("after prune entries = %d, want 1 (the accepted one)", len(l.Entries))
	}
	if entryKind(l.Entries[0]) != EntryAcceptedFixed {
		t.Fatalf("survivor kind = %q, want accepted-and-fixed", entryKind(l.Entries[0]))
	}
}

// Issue #48: a resolved entry carries the coarse fingerprint and the
// resolution-time blob SHA through the sticky-comment round trip — the
// blob SHA is the anchor for "suppress only while the file is unchanged",
// so losing either field in marshal/unmarshal silently disarms the fix.
func TestResolvedKindRoundtrip(t *testing.T) {
	var l DismissalLedger
	l.Entries = append(l.Entries, DismissalEntry{
		Kind:              EntryResolved,
		Fingerprint:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CoarseFingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Repository:        "o/r",
		BlobSHA:           "dede009e959581dd80bf8fe392816379ec8d1846",
		DismissedAt:       time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	})
	blob, err := l.MarshalBlob()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries after roundtrip = %d", len(got.Entries))
	}
	e := got.Entries[0]
	if entryKind(e) != EntryResolved {
		t.Fatalf("kind after roundtrip = %q, want %q", entryKind(e), EntryResolved)
	}
	if e.CoarseFingerprint != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || e.BlobSHA != "dede009e959581dd80bf8fe392816379ec8d1846" {
		t.Fatalf("coarse/blob lost in roundtrip: %+v", e)
	}
	if got.Active("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "o/r", e.DismissedAt.Add(time.Hour)) {
		t.Fatal("resolved entry must not count as a dismissal")
	}
	if !got.ResolvedActive("x", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "o/r", "dede009e959581dd80bf8fe392816379ec8d1846", e.DismissedAt.Add(time.Hour)) {
		t.Fatal("resolved entry must match on coarse fingerprint while blob unchanged")
	}
}
