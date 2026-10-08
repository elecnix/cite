package reviewer

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// A file an earlier run reviewed to completion, unchanged since, is recorded
// as an approved skip and costs no review call.
func TestUnchangedFileIsSkippedWithoutAModelCall(t *testing.T) {
	c := &fakeClient{fn: defaultScript("a.go")}
	in := baseInputs()
	in.Unchanged = map[string]bool{"a.go": true}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range c.calls {
		if !isTriageCall(call) && requestPath(call) == "a.go" {
			t.Fatal("an unchanged file was sent for review")
		}
	}
	if len(rec.Files) != 1 || rec.Files[0].State != model.FileSkipped || rec.Files[0].Reason != scope.SkipReasonUnchanged {
		t.Fatalf("files = %+v", rec.Files)
	}
	if !scope.IsApprovedSkipReason(rec.Files[0].Reason) {
		t.Fatal("an unchanged file must count toward coverage")
	}
}
