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
