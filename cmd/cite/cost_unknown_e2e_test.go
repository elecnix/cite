package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

// A provider that reports token counts and no cost is the shape issue #204 is
// about: Ollama Cloud answers with prompt_tokens, completion_tokens and
// total_tokens, and no cost key, and bills in aggregate behind an endpoint
// Cite never calls.
const usageNoCostResponse = `{"choices":[{"message":{"content":"{\"schema_version\":1,\"path\":\"x.txt\",\"outcome\":\"reviewed\",\"findings\":[]}"}}],"usage":{"prompt_tokens":1200,"completion_tokens":80,"total_tokens":1280}}`

// A provider that bills per call, the OpenRouter shape, adds the cost key.
const usageWithCostResponse = `{"choices":[{"message":{"content":"{\"schema_version\":1,\"path\":\"x.txt\",\"outcome\":\"reviewed\",\"findings\":[]}"}}],"usage":{"prompt_tokens":1200,"completion_tokens":80,"total_tokens":1280,"cost":0.0042}}`

// The whole run, end to end: a provider that reports tokens and no cost, and
// a config that declares no rates. The run's cost cannot be determined, so
// every surface that prints one must say so instead of printing $0.0000.
func TestLocalReviewReportsUnknownCost(t *testing.T) {
	chdirTempWithPostImage(t)

	var called bool
	srv := startFakeProvider(t, &called, usageNoCostResponse)
	setFakeModelEnv(t, srv.URL)

	// A config with no providers block: nothing declares a rate for the
	// model, and the provider reports no cost either.
	diffPath, cfgPath := writeDiffAndConfig(t, "require_parameters: false\n")

	var report bytes.Buffer
	stdout := captureStdout(t, func() {
		err := reviewLocal(diffPath, "", cfgPath, model.StructuredOutputResponseFormat, "", false, publisher.MarkdownReportSink(&report))
		if err != nil {
			t.Fatalf("reviewLocal: %v", err)
		}
	})

	if !called {
		t.Fatal("the fake provider was never called; the test did not exercise a run")
	}

	// The stdout summary.
	if !strings.Contains(stdout, "cost: unknown") {
		t.Errorf("stdout summary missing the unknown cost line:\n%s", stdout)
	}
	if strings.Contains(stdout, "$0.0000") {
		t.Errorf("stdout summary claims an unknown cost was zero:\n%s", stdout)
	}

	// The published report.
	if !strings.Contains(report.String(), "cost: unknown") {
		t.Errorf("published report missing the unknown cost line:\n%s", report.String())
	}
	if strings.Contains(report.String(), "$0.0000") {
		t.Errorf("published report claims an unknown cost was zero:\n%s", report.String())
	}
}

// The run record is the artifact a consumer reads later, so it has to carry
// the unknown-cost flag and not only a zero: a reader of the record cannot
// tell "$0.0000" apart from "$0.0000 that was never priced.
func TestLocalReviewRecordCarriesUnknownCost(t *testing.T) {
	chdirTempWithPostImage(t)

	var called bool
	srv := startFakeProvider(t, &called, usageNoCostResponse)
	setFakeModelEnv(t, srv.URL)

	diffPath, cfgPath := writeDiffAndConfig(t, "require_parameters: false\n")

	var report bytes.Buffer
	_ = captureStdout(t, func() {
		err := reviewLocal(diffPath, "", cfgPath, model.StructuredOutputResponseFormat, "", false, publisher.JSONReportSink(&report))
		if err != nil {
			t.Fatalf("reviewLocal: %v", err)
		}
	})

	var got struct {
		Run model.RunRecord `json:"run"`
	}
	if err := json.Unmarshal(report.Bytes(), &got); err != nil {
		t.Fatalf("report is not valid JSON: %v\n%s", err, report.String())
	}
	if !got.Run.CostUnknown {
		t.Error("the run record dropped the unknown-cost flag; a consumer cannot tell it from a zero cost")
	}
	if got.Run.Usage.InputTokens == 0 && got.Run.Usage.OutputTokens == 0 {
		t.Fatal("the run reported no tokens; the test did not exercise the unknown-cost case")
	}
}

// The same run against a provider that bills per call: the cost is known, and
// it is the provider's number rather than a guess. The run makes two calls
// (triage and review), so the reported sum is twice the per-call cost.
func TestLocalReviewReportsProviderBilledCost(t *testing.T) {
	chdirTempWithPostImage(t)

	var called bool
	srv := startFakeProvider(t, &called, usageWithCostResponse)
	setFakeModelEnv(t, srv.URL)

	diffPath, cfgPath := writeDiffAndConfig(t, "require_parameters: false\n")

	var report bytes.Buffer
	stdout := captureStdout(t, func() {
		err := reviewLocal(diffPath, "", cfgPath, model.StructuredOutputResponseFormat, "", false, publisher.JSONReportSink(&report))
		if err != nil {
			t.Fatalf("reviewLocal: %v", err)
		}
	})

	if !strings.Contains(stdout, "cost: $0.0084") {
		t.Errorf("stdout summary missing the provider-billed cost line:\n%s", stdout)
	}
	if strings.Contains(stdout, "cost: unknown") {
		t.Errorf("a provider-billed cost must not read as unknown:\n%s", stdout)
	}
}
