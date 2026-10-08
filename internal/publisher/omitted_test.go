package publisher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// Issue #129: every dropped finding is listed in one HTML comment that an
// agent can read through the API, and nothing in it can close the comment.
func TestReviewBodyCarriesOmittedFindingsAsAHiddenBlock(t *testing.T) {
	drops := []model.DropEntry{
		{Path: "a.go", Category: model.CategoryCrash, Title: "livePid throws --> on a bad pid", Reason: model.DropPerFileBudget},
		{Path: "b.go", Category: model.CategoryConvention, Title: "naming", Reason: model.DropSuppressed, Detail: "nits off"},
	}
	posted := []model.ValidatedFinding{{Finding: model.Finding{Title: "t", Category: model.CategoryCrash}, Path: "c.go", Blocks: true}}
	body := BuildReviewBody(ReviewBodyInput{FilesReviewed: 3, Posted: posted, AnchorInvalidDrops: drops})
	start := strings.Index(body, "<!-- cite:omitted ")
	if start < 0 {
		t.Fatalf("no omitted block:\n%s", body)
	}
	end := strings.Index(body[start:], " -->")
	if end < 0 {
		t.Fatal("omitted block never closes")
	}
	payload := body[start+len("<!-- cite:omitted ") : start+end]
	if strings.Contains(payload, "-->") || strings.Contains(payload, "--") {
		t.Fatalf("payload can close the comment: %s", payload)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(payload), &rows); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, payload)
	}
	if len(rows) != 2 || rows[0]["reason"] != "per_file_budget" || rows[1]["detail"] != "nits off" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0]["title"] != "livePid throws --> on a bad pid" {
		t.Fatalf("title did not round-trip: %q", rows[0]["title"])
	}
}
