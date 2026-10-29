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

// A provider whose only token count is a cache write: the prompt was
// written to the cache and billed as such, and the response reports no
// prompt tokens and no completion tokens. The cache counters are ones the
// cost formula prices, so this run is an unpriced run, not a free one.
const usageCacheWriteOnlyResponse = `{"choices":[{"message":{"content":"{\"schema_version\":1,\"path\":\"x.txt\",\"outcome\":\"reviewed\",\"findings\":[]}"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":5000}}}`

// localRun drives a whole local review against a fake provider and returns
// what the run printed on stdout, what the markdown report published, and
// the run record it produced. Every case below differs only in the
// provider's response and in what it expects the run to say about the cost,
// so the plumbing lives here once.
//
// The run record comes from the JSON sink and the published text from the
// markdown sink, so one run would have to be driven twice; instead the
// record is read back out of the JSON document the same sink writes.
func localRun(t *testing.T, response string) (stdout string, published string, rec model.RunRecord) {
	t.Helper()
	chdirTempWithPostImage(t)

	var called bool
	srv := startFakeProvider(t, &called, response)
	setFakeModelEnv(t, srv.URL)

	// A config with no providers block: nothing declares a rate for the
	// model, and the provider reports no cost either.
	diffPath, cfgPath := writeDiffAndConfig(t, "require_parameters: false\n")

	var report bytes.Buffer
	stdout = captureStdout(t, func() {
		err := reviewLocal(diffPath, "", cfgPath, model.StructuredOutputResponseFormat, "", false, publisher.MarkdownReportSink(&report))
		if err != nil {
			t.Fatalf("reviewLocal: %v", err)
		}
	})
	if !called {
		t.Fatal("the fake provider was never called; the test did not exercise a run")
	}
	published = report.String()

	// The record is the artifact a consumer reads later, so the flag has to
	// survive serialization. It is read back out of a JSON report of the
	// same shape, which is where the record is written in production.
	var jsonReport bytes.Buffer
	_ = captureStdout(t, func() {
		err := reviewLocal(diffPath, "", cfgPath, model.StructuredOutputResponseFormat, "", false, publisher.JSONReportSink(&jsonReport))
		if err != nil {
			t.Fatalf("reviewLocal: %v", err)
		}
	})
	var got struct {
		Run model.RunRecord `json:"run"`
	}
	if err := json.Unmarshal(jsonReport.Bytes(), &got); err != nil {
		t.Fatalf("report is not valid JSON: %v\n%s", err, jsonReport.String())
	}
	return stdout, published, got.Run
}

// The whole run, end to end: what the provider reports, what the run says
// about the cost, and what the run record carries. The three cases are the
// ones issue #204 separates: an unpriced run, a run the provider billed, and
// a run whose only tokens were a cache write.
func TestLocalReviewCostReporting(t *testing.T) {
	cases := []struct {
		name string
		// response is the provider's JSON.
		response string
		// wantCost is what the stdout summary must read for the cost.
		wantCost string
		// wantUnknown is what the run record must carry.
		wantUnknown bool
		// wantTokens, when set, must be non-zero in the run record, so the
		// test cannot pass without exercising the shape it names.
		wantTokens func(model.Usage) bool
	}{
		{
			name:        "tokens but no cost is an unpriced run",
			response:    usageNoCostResponse,
			wantCost:    "cost: unknown",
			wantUnknown: true,
			wantTokens:  func(u model.Usage) bool { return u.InputTokens > 0 || u.OutputTokens > 0 },
		},
		{
			name:     "a provider-reported cost is printed as billed",
			response: usageWithCostResponse,
			// The run makes two calls (triage and review), so the
			// reported sum is twice the per-call cost.
			wantCost:    "cost: $0.0084",
			wantUnknown: false,
		},
		{
			name:        "cache-write tokens alone are an unpriced run",
			response:    usageCacheWriteOnlyResponse,
			wantCost:    "cost: unknown",
			wantUnknown: true,
			wantTokens:  func(u model.Usage) bool { return u.CacheWriteTokens > 0 },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, published, rec := localRun(t, tc.response)

			if !strings.Contains(stdout, tc.wantCost) {
				t.Errorf("stdout summary missing %q:\n%s", tc.wantCost, stdout)
			}
			if rec.CostUnknown != tc.wantUnknown {
				t.Errorf("CostUnknown = %v, want %v", rec.CostUnknown, tc.wantUnknown)
			}
			if tc.wantTokens != nil && !tc.wantTokens(rec.Usage) {
				t.Errorf("the run record carries no token of the expected kind: %+v", rec.Usage)
			}

			// The published report says the same thing about the cost
			// as stdout does, for every case and not only for the
			// unpriced one.
			if !strings.Contains(published, tc.wantCost) {
				t.Errorf("published report missing %q:\n%s", tc.wantCost, published)
			}

			// An unknown cost is never printed as a zero.
			if tc.wantUnknown && strings.Contains(stdout, "$0.0000") {
				t.Errorf("stdout summary claims an unknown cost was zero:\n%s", stdout)
			}
			if tc.wantUnknown && strings.Contains(published, "$0.0000") {
				t.Errorf("published report claims an unknown cost was zero:\n%s", published)
			}
		})
	}
}
