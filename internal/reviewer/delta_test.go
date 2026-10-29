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

// A second push that touches one of two files sends only that file to the
// model; the other file is recorded as an approved skip, so coverage still
// spans the whole pull request.
func TestSecondPushSendsOnlyTheTouchedFileToTheModel(t *testing.T) {
	c := &fakeClient{fn: defaultScript("a.go", "b.go")}
	in := baseInputs()
	b := *in.Diffs["a.go"]
	b.Path = "b.go"
	in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "M", Path: "b.go"})
	in.Diffs["b.go"] = &b
	in.PostImage["b.go"] = in.PostImage["a.go"]
	in.Unchanged = map[string]bool{"a.go": true}

	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatal(err)
	}
	reviewed := map[string]bool{}
	for _, call := range c.calls {
		if !isTriageCall(call) {
			reviewed[requestPath(call)] = true
		}
	}
	if reviewed["a.go"] || !reviewed["b.go"] {
		t.Fatalf("files sent for review = %v, want only b.go", reviewed)
	}
	cov := scope.ComputeCoverage(rec.Files, len(in.Manifest))
	if !cov.Complete {
		t.Fatalf("coverage = %+v, want every file accounted for", cov)
	}
}
