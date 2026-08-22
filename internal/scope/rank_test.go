package scope

import (
	"fmt"
	"strings"
	"testing"
)

func TestRankForReviewOrderAndCutoff(t *testing.T) {
	var entries []ManifestEntry
	// 45 files; file i adds i lines (1..45). Only the top 40 by adds pass.
	for i := 1; i <= 45; i++ {
		entries = append(entries, ManifestEntry{
			Status: "M",
			Path:   fmt.Sprintf("f%02d.go", i),
			Adds:   i,
		})
	}

	if !ShouldRiskRank(entries) {
		t.Fatal("45 flagged files must trip the >40 rule")
	}
	if ShouldRiskRank(entries[:40]) {
		t.Fatal("40 files is not above the cutoff")
	}

	review, cut := RankForReview(entries, RiskRankCutoff)
	if len(review) != 40 || len(cut) != 5 {
		t.Fatalf("review=%d cut=%d, want 40/5", len(review), len(cut))
	}
	// Sorted descending by added lines: first is 45, last reviewed is 6.
	if review[0].Adds != 45 || review[39].Adds != 6 {
		t.Fatalf("order wrong: first=%d last=%d", review[0].Adds, review[39].Adds)
	}
	for _, e := range cut {
		if e.Adds > 5 {
			t.Fatalf("cut entry with high adds leaked into cutoff: %+v", e)
		}
	}

	// Deterministic tie-break by path.
	tied := []ManifestEntry{
		{Status: "A", Path: "b.go", Adds: 10},
		{Status: "A", Path: "a.go", Adds: 10},
		{Status: "A", Path: "c.go", Adds: 10},
	}
	review, _ = RankForReview(tied, 3)
	want := []string{"a.go", "b.go", "c.go"}
	for i, p := range want {
		if review[i].Path != p {
			t.Fatalf("tie-break order = %v", review)
		}
	}

	// limit >= len: nothing cut.
	review, cut = RankForReview(entries[:5], 10)
	if len(review) != 5 || cut != nil {
		t.Fatalf("no-cutoff case: review=%d cut=%v", len(review), cut)
	}
}

func TestRiskRankedNoteData(t *testing.T) {
	var entries []ManifestEntry
	for i := 0; i < 87; i++ {
		entries = append(entries, ManifestEntry{Status: "M", Path: fmt.Sprintf("f%02d.go", i), Adds: 87 - i})
	}
	review, cut := RankForReview(entries, RiskRankCutoff)
	note := RiskRankedNote(len(review), len(review)+len(cut))
	want := "Risk-ranked: reviewed the top 40 of 87 flagged files by added source lines; the remaining 47 were recorded as skipped(risk_rank_cutoff)."
	if note != want {
		t.Fatalf("note = %q\nwant  %q", note, want)
	}
	// The caller records every cut file with the named reason — the run
	// record carries the data this line summarizes (§7: never silently).
	var recorded []string
	for _, e := range cut {
		recorded = append(recorded, SkipReasonRiskCutoff+":"+e.Path)
	}
	if len(recorded) != 47 || !strings.HasPrefix(recorded[0], "risk_rank_cutoff:") {
		t.Fatalf("recorded cut entries = %d, want 47", len(recorded))
	}
}
