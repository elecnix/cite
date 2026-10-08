package gate

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// Issue #170: every file errored on the judge. The scope filter resolved the
// files and the model failed on all of them, so the reason names the
// provider's failure, never the path filter.
func TestEveryFileErroredNamesTheProviderNotThePathFilter(t *testing.T) {
	rec := baseRecord()
	rec.Coverage = model.Coverage{APIFiles: 2, Errored: 2}
	rec.Files = []model.FileOutcome{
		{Path: "a.go", State: model.FileErrored, Reason: "model_error", Detail: "rate_limited: HTTP 429: monthly usage limit (ref: 1)"},
		{Path: "b.go", State: model.FileErrored, Reason: "model_error", Detail: "rate_limited: HTTP 429: monthly usage limit (ref: 2)"},
	}
	v, reason := Decide(rec, config.Default(), Options{})
	if v != model.VerdictCouldNotEvaluate {
		t.Fatalf("verdict = %s, want COULD_NOT_EVALUATE", v)
	}
	if strings.Contains(reason, "path-filter") || strings.Contains(reason, "zero in-scope") {
		t.Fatalf("reason blames the path filter: %q", reason)
	}
	for _, want := range []string{"every file errored at the model", "model_error", "HTTP 429", "2 file(s) never evaluated"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q missing %q", reason, want)
		}
	}
}

// Files that failed in different ways name no shared cause: each keeps its
// own entry in the list.
func TestMixedErrorsNameNoSharedCause(t *testing.T) {
	rec := baseRecord()
	rec.Coverage = model.Coverage{APIFiles: 2, Errored: 2}
	rec.Files = []model.FileOutcome{
		{Path: "a.go", State: model.FileErrored, Reason: "model_error"},
		{Path: "b.go", State: model.FileErrored, Reason: "runaway_generation"},
	}
	_, reason := Decide(rec, config.Default(), Options{})
	if strings.Contains(reason, "every file errored") {
		t.Fatalf("mixed causes reported as shared: %q", reason)
	}
	if !strings.Contains(reason, "a.go (model_error)") || !strings.Contains(reason, "b.go (runaway_generation)") {
		t.Fatalf("each file's reason should be listed: %q", reason)
	}
}

// The real bypass shape keeps its reason: nothing reviewed, nothing skipped,
// nothing even attempted.
func TestNothingAttemptedIsStillThePathFilterShape(t *testing.T) {
	rec := baseRecord()
	rec.Coverage = model.Coverage{APIFiles: 3}
	_, reason := Decide(rec, config.Default(), Options{})
	if !strings.Contains(reason, "path-filter bypass shape") {
		t.Fatalf("reason = %q, want the path-filter shape", reason)
	}
}

// The example detail is the first one any errored file carries, so a first
// file without one does not hide the provider's message.
func TestSharedCauseTakesTheFirstDetailPresent(t *testing.T) {
	got := sharedCause([]model.FileOutcome{
		{Path: "a.go", State: model.FileErrored, Reason: "model_error"},
		{Path: "b.go", State: model.FileErrored, Reason: "model_error", Detail: "rate_limited: HTTP 429"},
	})
	if got != "model_error (rate_limited: HTTP 429)" {
		t.Fatalf("sharedCause = %q", got)
	}
}

// An empty reason is a reason like any other, not "not seen yet".
func TestSharedCauseDoesNotTreatAnEmptyReasonAsUnset(t *testing.T) {
	got := sharedCause([]model.FileOutcome{
		{Path: "a.go", State: model.FileErrored},
		{Path: "b.go", State: model.FileErrored, Reason: "model_error"},
	})
	if got != "" {
		t.Fatalf("sharedCause = %q, want no shared cause", got)
	}
}
