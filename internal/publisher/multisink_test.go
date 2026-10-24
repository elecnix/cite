package publisher

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// recordingSink keeps what Publish received, so a test can check that every
// sink behind a MultiSink saw the same run.
type recordingSink struct {
	rec     *model.RunRecord
	payload ReportPayload
	err     error
}

func (s *recordingSink) Publish(rec *model.RunRecord, payload ReportPayload) error {
	s.rec, s.payload = rec, payload
	return s.err
}

// TestMultiSinkWritesOneRunToEveryReport covers issue #215: one run, one
// Markdown report and one JSON report, both of the same review.
func TestMultiSinkWritesOneRunToEveryReport(t *testing.T) {
	var md, js bytes.Buffer
	sink := MultiSink(MarkdownReportSink(&md), JSONReportSink(&js))

	if err := sink.Publish(sampleRecord(), samplePayload()); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if !strings.Contains(md.String(), "## Cite review — owner/repo#7") ||
		!strings.Contains(md.String(), "Off-by-one in loop bound") {
		t.Fatalf("markdown report is missing the run:\n%s", md.String())
	}
	var doc struct {
		Run    model.RunRecord `json:"run"`
		Review ReportPayload   `json:"review"`
	}
	if err := json.Unmarshal(js.Bytes(), &doc); err != nil {
		t.Fatalf("json report is not JSON: %v\n%s", err, js.String())
	}
	if doc.Run.PRNumber != 7 || len(doc.Run.Findings) != 1 || doc.Run.Findings[0].ID != "f1" {
		t.Fatalf("json report run = %+v, want PR 7 with finding f1", doc.Run)
	}
	if len(doc.Review.Comments) != 1 || doc.Review.Body != samplePayload().Body {
		t.Fatalf("json report review = %+v, want the sample payload", doc.Review)
	}
}

// TestMultiSinkHandsEverySinkTheSameRecord checks the fan-out passes one
// record to every sink, not a copy built per sink.
func TestMultiSinkHandsEverySinkTheSameRecord(t *testing.T) {
	a, b := &recordingSink{}, &recordingSink{}
	rec, payload := sampleRecord(), samplePayload()

	if err := MultiSink(a, b).Publish(rec, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if a.rec != rec || b.rec != rec {
		t.Fatalf("sinks got records %p and %p, want both %p", a.rec, b.rec, rec)
	}
	if a.payload.Body != payload.Body || b.payload.Body != payload.Body {
		t.Fatalf("sinks got bodies %q and %q, want %q", a.payload.Body, b.payload.Body, payload.Body)
	}
}

// TestMultiSinkWritesTheRestWhenOneFails checks a failing report does not
// cost the user the others: every sink runs, and the error names each failure.
func TestMultiSinkWritesTheRestWhenOneFails(t *testing.T) {
	errA := errors.New("disk full")
	a, b := &recordingSink{err: errA}, &recordingSink{}

	err := MultiSink(a, b).Publish(sampleRecord(), samplePayload())
	if !errors.Is(err, errA) {
		t.Fatalf("Publish error = %v, want it to wrap %v", err, errA)
	}
	if b.rec == nil {
		t.Fatal("second sink never ran after the first failed")
	}
}

// TestMultiSinkOfOneIsThatSink checks the common single-report case adds no
// wrapper.
func TestMultiSinkOfOneIsThatSink(t *testing.T) {
	a := &recordingSink{}
	if got := MultiSink(a); got != Sink(a) {
		t.Fatalf("MultiSink(a) = %#v, want a itself", got)
	}
}
