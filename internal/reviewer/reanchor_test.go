package reviewer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// deletedConstDiff removes the constant retryLimit at old line 3 and adds a
// line after it. The use site, `n := retryLimit * 2`, sits at line 30 of the
// post-image, far outside the hunk.
const deletedConstDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,5 +1,5 @@
 package a
 
-const retryLimit = 5
+const retryBudget = 5
 
 func f() {
`

func deletedConstPostImage() []byte {
	lines := []string{"package a", "", "const retryBudget = 5", "", "func f() {"}
	for len(lines) < 29 {
		lines = append(lines, fmt.Sprintf("\t_ = %d", len(lines)))
	}
	lines = append(lines, "\tn := retryLimit * 2", "}")
	return []byte(strings.Join(lines, "\n") + "\n")
}

func reanchorInputs(t *testing.T, diffText string, post []byte) Inputs {
	t.Helper()
	d, err := scope.ParseUnifiedDiff(diffText)
	if err != nil {
		t.Fatal(err)
	}
	return Inputs{
		Manifest:      baseManifest(),
		Diffs:         map[string]*scope.DiffFile{"a.go": d.Files[0]},
		PostImage:     map[string][]byte{"a.go": post},
		PRDescription: "Test PR",
		Nonce:         "nonce7f3a91",
	}
}

func runWithFinding(t *testing.T, in Inputs, f map[string]any) *model.RunRecord {
	t.Helper()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec
}

// Issue #220: a finding about a deleted definition anchors on the unchanged
// use site. The validator keeps it and moves the anchor to the deleted line.
func TestReanchorToDeletedDefinition(t *testing.T) {
	in := reanchorInputs(t, deletedConstDiff, deletedConstPostImage())
	f := mkFinding("f1", model.CategoryCrash, 30, 30, "likely",
		[]map[string]any{mkEvidence(30, "n := retryLimit * 2")})
	rec := runWithFinding(t, in, f)

	for _, d := range rec.Drops {
		if d.Reason == model.DropAnchorInvalid {
			t.Fatalf("finding dropped anchor_invalid: %+v", d)
		}
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1", len(rec.Findings))
	}
	got := rec.Findings[0]
	ra := got.Reanchor
	if ra == nil {
		t.Fatal("Reanchor is nil")
	}
	if ra.Path != "a.go" || ra.Side != "LEFT" || ra.Line != 3 || ra.Kind != model.ReanchorDeletedDefinition {
		t.Errorf("Reanchor = %+v, want a.go LEFT line 3 deleted_definition", ra)
	}
	if ra.UseSite.Line != 30 || ra.UseSite.Text != "n := retryLimit * 2" {
		t.Errorf("UseSite = %+v, want line 30 quoted", ra.UseSite)
	}
	if got.Blocks {
		t.Error("a re-anchored finding must not block: its anchor is not on an added line")
	}
}

// With no deleted definition to point at, the nearest changed line takes the
// finding.
func TestReanchorToNearestChangedLine(t *testing.T) {
	in := reanchorInputs(t, deletedConstDiff, deletedConstPostImage())
	f := mkFinding("f1", model.CategoryCrash, 30, 30, "likely",
		[]map[string]any{mkEvidence(30, "n := retryLimit * 2")})
	f["title"], f["body"] = "Unrelated problem", "Unrelated"
	// The use site names an identifier no removed line defines.
	post := strings.Replace(string(deletedConstPostImage()), "retryLimit", "other", 1)
	in.PostImage["a.go"] = []byte(post)
	f["evidence"] = []map[string]any{mkEvidence(30, "n := other * 2")}
	rec := runWithFinding(t, in, f)

	if len(rec.Findings) != 1 || rec.Findings[0].Reanchor == nil {
		t.Fatalf("want one re-anchored finding, got %+v drops %+v", rec.Findings, rec.Drops)
	}
	ra := rec.Findings[0].Reanchor
	if ra.Kind != model.ReanchorNearestChange || ra.Side != "RIGHT" || ra.Line != 3 {
		t.Errorf("Reanchor = %+v, want nearest_change RIGHT line 3", ra)
	}
}

// An anchor already inside the hunks is left alone.
func TestNoReanchorWhenAnchorInHunk(t *testing.T) {
	in := baseInputs()
	f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
		[]map[string]any{mkEvidence(2, "added line alpha here")})
	rec := runWithFinding(t, in, f)
	if len(rec.Findings) != 1 || rec.Findings[0].Reanchor != nil {
		t.Fatalf("want one finding without Reanchor, got %+v", rec.Findings)
	}
}

// A file with no hunks borrows a changed line from another file that the
// finding text points at.
func TestReanchorToOtherFileForHunklessFile(t *testing.T) {
	in := baseInputs()
	other, err := scope.ParseUnifiedDiff(strings.Replace(deletedConstDiff, "a.go", "b.go", -1))
	if err != nil {
		t.Fatal(err)
	}
	in.Diffs = map[string]*scope.DiffFile{"b.go": other.Files[0]}
	f := mkFinding("f1", model.CategoryCrash, 2, 2, "likely",
		[]map[string]any{mkEvidence(2, "added line alpha here")})
	f["body"] = "The change to retryLimit leaves this file behind."
	rec := runWithFinding(t, in, f)

	if len(rec.Findings) != 1 || rec.Findings[0].Reanchor == nil {
		t.Fatalf("want one re-anchored finding, got %+v drops %+v", rec.Findings, rec.Drops)
	}
	ra := rec.Findings[0].Reanchor
	if ra.Path != "b.go" || ra.Side != "LEFT" || ra.Line != 3 || ra.Kind != model.ReanchorOtherFile {
		t.Errorf("Reanchor = %+v, want b.go LEFT line 3 other_file", ra)
	}
}

// With nothing to point at, the finding is still dropped and reaches the
// review body (#43).
func TestHunklessFileWithNoPointerStaysDropped(t *testing.T) {
	in := baseInputs()
	in.Diffs = map[string]*scope.DiffFile{}
	f := mkFinding("f1", model.CategoryCrash, 2, 2, "likely",
		[]map[string]any{mkEvidence(2, "added line alpha here")})
	rec := runWithFinding(t, in, f)
	if len(rec.Findings) != 0 || len(rec.Drops) != 1 || rec.Drops[0].Reason != model.DropAnchorInvalid {
		t.Fatalf("want one anchor_invalid drop, got findings %+v drops %+v", rec.Findings, rec.Drops)
	}
}
