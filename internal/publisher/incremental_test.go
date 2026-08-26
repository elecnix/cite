package publisher

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

func TestFilesToReviewOnlyContentChanges(t *testing.T) {
	prev := map[string]string{
		"unchanged.go": "aaa",
		"edited.go":    "bbb",
		"deleted.go":   "ccc",
	}
	cur := map[string]string{
		"unchanged.go": "aaa", // same blob_sha: skip, even if history churned
		"edited.go":    "B99", // content changed: review
		"moved.go":     "ddd", // new file: review
	}
	got := FilesToReview(prev, cur)
	want := []string{"edited.go", "moved.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (deterministic order)", got, want)
		}
	}
}

func TestFilesToReviewImmuneToHistoryChurn(t *testing.T) {
	// A force-push that rewrites history but produces identical content must
	// not re-review anything: the comparison is blob_sha to blob_sha.
	prev := map[string]string{"a.go": "sha-1", "b.go": "sha-2"}
	cur := map[string]string{"a.go": "sha-1", "b.go": "sha-2"}
	if got := FilesToReview(prev, cur); len(got) != 0 {
		t.Fatalf("identical content after rebase/force-push must be skipped, got %v", got)
	}
}

func TestCarryForwardSplitsByReviewedPath(t *testing.T) {
	prevFindings := []model.ValidatedFinding{
		mkFinding("untouched.go", "crash", "Old bug A", "a := b"),
		mkFinding("retouched.go", "crash", "Old bug B", "c := d"),
	}
	reviewed := map[string]bool{"retouched.go": true}

	carried, pending := CarryForward(prevFindings, reviewed)

	if len(carried) != 1 || carried[0].Path != "untouched.go" {
		t.Fatalf("findings on un-re-reviewed files must carry forward for verification, got %+v", carried)
	}
	if len(pending) != 1 || pending[0].Path != "retouched.go" {
		t.Fatalf("re-reviewed files' findings are resolution-pending, never silently dropped: %+v", pending)
	}

	// Nothing reviewed: everything carries. Incremental fails toward
	// re-review/carry-forward, never toward silent loss.
	carried, pending = CarryForward(prevFindings, map[string]bool{})
	if len(carried) != 2 || len(pending) != 0 {
		t.Fatalf("no re-review means full carry-forward, got carried=%d pending=%d", len(carried), len(pending))
	}
}

func TestBuildReviewBodyCapAndSections(t *testing.T) {
	post := mkFinding("a.go", "crash", "Off by one", "i <= n")
	unanchored := mkFinding("weird.bin", "crash", "Cannot anchor binary", "\x00")

	body := BuildReviewBody(ReviewBodyInput{
		FilesReviewed:            7,
		Posted:                   []model.ValidatedFinding{post},
		Unanchorable:             []model.ValidatedFinding{unanchored},
		DropsSummary:             "Full run record and drop log: see the run artifact.",
		InstructionsFooterLines:  []string{"<!-- cite: footer -->"},
		RiskRankedNote:           "Risk ranking capped the reviewed set.",
		ModifiedInstructionsNote: "Using 3 of 5 instruction sections; 1 were authoring.",
	})

	if !strings.Contains(body, UnanchoredHeading) {
		t.Fatal("unanchorable findings must appear in the labelled section, never silently dropped")
	}
	if !strings.Contains(body, "weird.bin") || !strings.Contains(body, "Cannot anchor binary") {
		t.Fatal("unanchored finding missing from body")
	}

	// Prose cap: opening line + two notes = 3 prose lines here. Force it over
	// the cap with four long notes and check truncation is loud.
	var body2 string
	{
		in := ReviewBodyInput{
			FilesReviewed:  1,
			Posted:         []model.ValidatedFinding{post},
			RiskRankedNote: strings.Repeat("note ", 30),
		}
		in.ModifiedInstructionsNote = strings.Repeat("mod ", 30)
		body2 = BuildReviewBody(in)
	}
	proseLines := 0
	for _, line := range strings.Split(body2, "\n") {
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "<!--") ||
			strings.Contains(line, UnanchoredHeading) {
			break
		}
		proseLines++
	}
	if proseLines > MaxProseLines {
		t.Fatalf("prose cap broken: %d lines > %d\n%s", proseLines, MaxProseLines, body2)
	}
}

func TestBuildReviewBodySanitisesModelText(t *testing.T) {
	nasty := mkFinding("a.go", "crash", "See https://evil.example @everyone fixes #1", "x")
	unanchored := mkFinding("b.go", "crash", "Mention @all and ](https://beacon.example)", "y")

	body := BuildReviewBody(ReviewBodyInput{
		Posted:       []model.ValidatedFinding{nasty},
		Unanchorable: []model.ValidatedFinding{unanchored},
	})
	for _, hazard := range []string{"https://evil.example", "@everyone", "@all", "](https://beacon.example)", "fixes #1"} {
		if strings.Contains(body, hazard) {
			t.Fatalf("model-authored hazard reached the renderer: %q in:\n%s", hazard, body)
		}
	}
}

func TestBuildReviewBodyEmptyWhenNothingToSay(t *testing.T) {
	// Silence is a valid review: nothing posted means no review body at all.
	if got := BuildReviewBody(ReviewBodyInput{FilesReviewed: 12}); got != "" {
		t.Fatalf("nothing to say must produce an empty body, got %q", got)
	}
}

// TestBuildReviewBodySurfacesAnchorInvalidDrops is the fix for issue #42: a
// review that produced candidate findings but dropped them all as
// anchor_invalid used to post nothing and exit COULD_NOT_EVALUATE, burying
// real-but-unanchored observations in the CI log. Now the dropped findings
// reach the human via a labelled body section even when no finding anchored.
func TestBuildReviewBodySurfacesAnchorInvalidDrops(t *testing.T) {
	drops := []model.DropEntry{
		{Path: "a.go", Category: model.CategoryLogicInversion, Title: "Inverted guard drops auth checks", Reason: model.DropAnchorInvalid},
		{Path: "b.go", Category: model.CategoryCrash, Title: "Unchecked nil deref after cast", Reason: model.DropAnchorInvalid},
	}
	body := BuildReviewBody(ReviewBodyInput{
		FilesReviewed:      2,
		AnchorInvalidDrops: drops,
		DropsSummary:       "2 findings dropped by safety rails this run; see the run log.",
	})
	if body == "" {
		t.Fatal("anchor_invalid drops alone must still produce a review body, not silence (issue #42)")
	}
	if !strings.Contains(body, NotableUnanchoredHeading) {
		t.Fatalf("missing the notable-but-unanchored heading %q in:\n%s", NotableUnanchoredHeading, body)
	}
	for _, d := range drops {
		if !strings.Contains(body, d.Path) {
			t.Errorf("anchor_invalid drop path %q missing from body:\n%s", d.Path, body)
		}
		if !strings.Contains(body, d.Title) {
			t.Errorf("anchor_invalid drop title %q missing from body:\n%s", d.Title, body)
		}
	}
}

// Only anchor_invalid drops surface as notable-but-unanchored: they were
// structurally sound findings that merely could not be pinned to a line.
// Other drop reasons (evidence_mismatch, budget, suppressed) stay in the
// drops-summary count and the run record — they are not promoted to the
// review body, because they are not "real but imprecise".
func TestBuildReviewBodyIgnoresOtherDropReasons(t *testing.T) {
	drops := []model.DropEntry{
		{Path: "a.go", Category: model.CategoryConvention, Title: "nit: naming", Reason: model.DropSuppressed},
		{Path: "b.go", Category: model.CategoryCrash, Title: "off by one", Reason: model.DropBudget},
		{Path: "c.go", Category: model.CategoryLogicInversion, Title: "real but unanchored", Reason: model.DropAnchorInvalid},
	}
	body := BuildReviewBody(ReviewBodyInput{
		FilesReviewed:      3,
		AnchorInvalidDrops: drops,
	})
	if !strings.Contains(body, "c.go") || !strings.Contains(body, "real but unanchored") {
		t.Fatalf("the anchor_invalid drop must appear:\n%s", body)
	}
	if strings.Contains(body, "nit: naming") || strings.Contains(body, "off by one") {
		t.Fatalf("non-anchor_invalid drops must NOT appear in the body:\n%s", body)
	}
	if strings.Contains(body, NotableUnanchoredHeading) && !strings.Contains(body, "real but unanchored") {
		t.Fatalf("heading present without the one anchor_invalid drop:\n%s", body)
	}
}

// Sanitization applies to the notable-but-unanchored section too: the dropped
// titles are model-authored text, so they run through SanitizeText before
// reaching the renderer (§12 I5).
func TestBuildReviewBodySanitisesUnanchoredDrops(t *testing.T) {
	drops := []model.DropEntry{
		{Path: "a.go", Category: model.CategoryLogicInversion, Title: "See https://evil.example @everyone fixes #1", Reason: model.DropAnchorInvalid},
	}
	body := BuildReviewBody(ReviewBodyInput{
		AnchorInvalidDrops: drops,
	})
	for _, hazard := range []string{"https://evil.example", "@everyone", "fixes #1"} {
		if strings.Contains(body, hazard) {
			t.Fatalf("model-authored hazard reached the unanchored section: %q in:\n%s", hazard, body)
		}
	}
}

// The notable-but-unanchored section is structured, not prose: it does not
// count against the five-line prose cap.
func TestBuildReviewBodyUnanchoredSectionDoesNotCountAgainstProseCap(t *testing.T) {
	post := mkFinding("a.go", "crash", "Off by one", "i <= n")
	drops := make([]model.DropEntry, 20)
	for i := range drops {
		drops[i] = model.DropEntry{Path: "a.go", Category: model.CategoryCrash, Title: "unanchored", Reason: model.DropAnchorInvalid}
	}
	body := BuildReviewBody(ReviewBodyInput{
		Posted:             []model.ValidatedFinding{post},
		AnchorInvalidDrops: drops,
		RiskRankedNote:     strings.Repeat("note ", 30),
	})
	proseLines := 0
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "<!--") ||
			strings.Contains(line, UnanchoredHeading) || strings.Contains(line, NotableUnanchoredHeading) {
			break
		}
		proseLines++
	}
	if proseLines > MaxProseLines {
		t.Fatalf("prose cap broken by unanchored section: %d lines > %d\n%s", proseLines, MaxProseLines, body)
	}
}
