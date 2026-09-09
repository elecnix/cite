package reviewer

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// TestAnchorOutOfRangeDropsFinding (issue #60): a well-formed review response
// carrying ONE finding whose anchor falls outside the reviewed file's actual
// line range must degrade to a dropped finding (reason anchor_out_of_range),
// keep the healthy findings, and keep the file's verdict — not fail the whole
// file's parse and return no verdict.
func TestAnchorOutOfRangeDropsFinding(t *testing.T) {
	// basePostImage() has 3 lines; the good finding anchors line 2.
	tests := []struct {
		name       string
		start, end int
		wantDetail string // substring expected in the drop detail
	}{
		{
			name:       "past EOF",
			start:      50, end: 50,
			wantDetail: "3 post-image lines",
		},
		{
			name:  "start line zero",
			start: 0, end: 2,
			wantDetail: "outside the reviewed file's line range",
		},
		{
			name:  "end before start",
			start: 3, end: 1,
			wantDetail: "outside the reviewed file's line range",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			good := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
				[]map[string]any{mkEvidence(2, "added line alpha here")})
			bad := mkFinding("f2", model.CategoryLogicInversion, tc.start, tc.end, "likely",
				[]map[string]any{mkEvidence(tc.start, "context line one")})
			c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
				if isTriageCall(req) {
					return triageJSON("a.go"), nil
				}
				return reviewJSON("a.go", "reviewed", []map[string]any{good, bad}), nil
			}}
			rec, err := runOnce(t, in, baseOptions(c))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			// The file keeps its verdict.
			var fo *model.FileOutcome
			for i := range rec.Files {
				if rec.Files[i].Path == "a.go" {
					fo = &rec.Files[i]
				}
			}
			if fo == nil || fo.State != model.FileReviewed || !fo.Reviewed {
				t.Fatalf("file outcome = %+v, want reviewed: a well-formed response with one bad anchor must not zero the verdict (issue #60)", fo)
			}

			// The good finding survives.
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d (%+v), want 1 — the healthy finding must survive", len(rec.Findings), rec.Findings)
			}
			if rec.Findings[0].ID != "f1" {
				t.Errorf("surviving finding = %q, want f1", rec.Findings[0].ID)
			}

			// The bad finding is dropped with reason anchor_out_of_range.
			var drop *model.DropEntry
			for i := range rec.Drops {
				if rec.Drops[i].Reason == model.DropAnchorOutOfRange {
					drop = &rec.Drops[i]
				}
			}
			if drop == nil {
				t.Fatalf("no anchor_out_of_range drop found; drops = %+v", rec.Drops)
			}
			if drop.Path != "a.go" {
				t.Errorf("drop path = %q, want a.go", drop.Path)
			}
			if !strings.Contains(drop.Title, "f2") {
				t.Errorf("drop title = %q, want the f2 finding title", drop.Title)
			}
			if !strings.Contains(drop.Detail, tc.wantDetail) {
				t.Errorf("drop detail %q missing %q", drop.Detail, tc.wantDetail)
			}
		})
	}
}

// TestInvalidJSONStillFailsSafe is the control for the widening above: the
// degrade-to-drop path must not widen the fail-safe hole. Genuinely invalid
// JSON still fails the file's parse.
func TestInvalidJSONStillFailsSafe(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return `{"schema_version":1,"path":"a.go","outcome":"reviewed","findings":[`, nil // truncated JSON
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var fo *model.FileOutcome
	for i := range rec.Files {
		if rec.Files[i].Path == "a.go" {
			fo = &rec.Files[i]
		}
	}
	if fo == nil || fo.State != model.FileErrored || fo.Reason != "parse_failure" {
		t.Fatalf("file outcome = %+v, want errored(parse_failure): invalid JSON must still fail safe", fo)
	}
}