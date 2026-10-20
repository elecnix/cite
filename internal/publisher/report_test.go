package publisher

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

func sampleRecord() *model.RunRecord {
	return &model.RunRecord{
		SchemaVersion: model.SchemaVersion,
		Repository:    "owner/repo",
		PRNumber:      7,
		Model:         "test-model",
		Files: []model.FileOutcome{
			{Path: "a.go", Status: "M", State: model.FileReviewed, Findings: 1, Reviewed: true},
			{Path: "b.go", Status: "A", State: model.FileErrored, Reason: "parse_failure"},
		},
		Findings: []model.ValidatedFinding{
			{
				Finding: model.Finding{
					ID:         "f1",
					Category:   model.CategoryLogicInversion,
					Confidence: model.ConfidenceCertain,
					Title:      "Off-by-one in loop bound",
					Body:       "The loop skips the last element.",
					Impact:     "Results are silently truncated.",
					Evidence: []model.Evidence{
						{Line: 42, Quote: "for i := 0; i < len(xs)-1; i++"},
					},
					Anchor:       model.Anchor{StartLine: 42, EndLine: 42},
					IntroducedBy: model.IntroducedAddedLine,
				},
				Path:        "a.go",
				Fingerprint: "fp1",
				Blocks:      true,
			},
		},
		Drops: []model.DropEntry{
			{Path: "a.go", Reason: model.DropSuppressed, Title: "convention nit"},
		},
		Coverage:      model.Coverage{APIFiles: 2, Reviewed: 1, Errored: 1},
		Verdict:       model.VerdictFound,
		VerdictReason: "1 blocking finding",
	}
}

func samplePayload() ReportPayload {
	return ReportPayload{
		Body: "Cite reviewed 1 file and posted 1 comment.",
		Comments: []InlineComment{
			{Path: "a.go", StartLine: 42, Line: 42, Side: "RIGHT", Body: "**logic-inversion**: Off-by-one in loop bound"},
		},
		Unanchorable: 0,
	}
}

func TestJSONReportSinkRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	rec := sampleRecord()
	if err := JSONReportSink(&buf).Publish(rec, samplePayload()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var got struct {
		Run    model.RunRecord `json:"run"`
		Review ReportPayload   `json:"review"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got.Run.Verdict != rec.Verdict || got.Run.Repository != rec.Repository {
		t.Errorf("run record mismatch: %+v", got.Run)
	}
	if len(got.Run.Findings) != 1 || got.Run.Findings[0].Title != "Off-by-one in loop bound" {
		t.Errorf("findings mismatch: %+v", got.Run.Findings)
	}
	if len(got.Review.Comments) != 1 || got.Review.Comments[0].Path != "a.go" {
		t.Errorf("payload comments mismatch: %+v", got.Review.Comments)
	}
}

func TestMarkdownReportSinkRendersOutcome(t *testing.T) {
	var buf bytes.Buffer
	rec := sampleRecord()
	payload := samplePayload()
	if err := MarkdownReportSink(&buf).Publish(rec, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"## Cite review — owner/repo#7",
		string(model.VerdictFound),
		"1 blocking finding",
		"Off-by-one in loop bound",
		"`for i := 0; i < len(xs)-1; i++`",
		"a.go",
		payload.Body,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown report missing %q\n---\n%s", want, out)
		}
	}
}

func TestMarkdownReportSinkHandlesCleanRun(t *testing.T) {
	var buf bytes.Buffer
	rec := sampleRecord()
	rec.Findings = nil
	rec.Drops = nil
	rec.Verdict = model.VerdictPass
	rec.VerdictReason = "nothing to say (§10)"
	if err := MarkdownReportSink(&buf).Publish(rec, ReportPayload{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, string(model.VerdictPass)) {
		t.Errorf("missing verdict\n---\n%s", out)
	}
	if strings.Contains(out, "<nil>") || strings.Contains(out, "panic") {
		t.Errorf("clean run rendered degenerate output:\n%s", out)
	}
}

// Issue #204: the published report must not state a price Cite could not
// determine. A run whose provider reported tokens but no cost, in a config
// that declares no rates, is billed; "$0.0000" would claim it was free.
func TestMarkdownReportSinkShowsUnknownCost(t *testing.T) {
	var buf bytes.Buffer
	rec := sampleRecord()
	rec.Usage = model.Usage{InputTokens: 45_000, OutputTokens: 2_000}
	rec.CostUnknown = true
	if err := MarkdownReportSink(&buf).Publish(rec, samplePayload()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "cost: unknown") {
		t.Errorf("published report missing the unknown cost line:\n%s", out)
	}
	if strings.Contains(out, "$0.0000") {
		t.Errorf("published report claims an unknown cost was zero:\n%s", out)
	}
}

// A provider-reported zero is a price. The published report keeps it.
func TestMarkdownReportSinkShowsBilledZeroCost(t *testing.T) {
	var buf bytes.Buffer
	rec := sampleRecord()
	rec.Usage = model.Usage{InputTokens: 45_000, OutputTokens: 2_000, CostReported: true}
	if err := MarkdownReportSink(&buf).Publish(rec, samplePayload()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "cost: $0.0000") {
		t.Errorf("published report missing the billed zero cost line:\n%s", out)
	}
	if strings.Contains(out, "cost: unknown") {
		t.Errorf("a provider-reported zero must not read as unknown:\n%s", out)
	}
}

// The JSON report carries the run record as it is persisted, so the unknown
// cost has to survive serialization: a consumer reading the artifact must
// be able to tell an unknown cost from a zero one.
func TestJSONReportSinkCarriesUnknownCost(t *testing.T) {
	var buf bytes.Buffer
	rec := sampleRecord()
	rec.Usage = model.Usage{InputTokens: 45_000, OutputTokens: 2_000}
	rec.CostUnknown = true
	if err := JSONReportSink(&buf).Publish(rec, samplePayload()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var got struct {
		Run model.RunRecord `json:"run"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if !got.Run.CostUnknown {
		t.Error("the JSON report dropped the unknown-cost flag; a consumer cannot tell it from a zero cost")
	}
}
