package reviewer

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// expectedDerivation renders the operator-facing description of the derived
// review deadline straight from the constants, so the expectation in these
// tests cannot itself be a hardcoded copy of the prose under test. Moving
// ReviewTimeoutBase, AssumedGenerationRate or DefaultReviewTimeout moves the
// expectation, which is the point: the deadline and the sentence describing
// it are calibrated together (issue #28).
func expectedDerivation(capKey string) string {
	return fmt.Sprintf("%ds + %s/%d tok/s, floored at %s",
		int(config.ReviewTimeoutBase/time.Second), capKey, config.AssumedGenerationRate,
		config.DefaultReviewTimeout)
}

// The deadline remedy is the only place an operator is told how the derived
// deadline is computed. A sentence that quotes the formula from memory goes
// stale the moment a constant moves, and one that quotes only the arithmetic
// misstates the deadline for every cap below the floor: 60s + 4096/128 is
// 92s, and the call actually died at the 15m floor it never mentions.
func TestDerivedDeadlineRemedyQuotesTheLiveConstants(t *testing.T) {
	cfg := mustCfg(t, "")
	got := deadlineRemedy(cfg, config.DerivedReviewTimeout(4096))
	if want := expectedDerivation("roles.review.max_output_tokens"); !strings.Contains(got, want) {
		t.Errorf("derived deadline remedy does not state the derivation as the code computes it\nwant substring: %q\ngot: %s", want, got)
	}
	if !strings.Contains(got, config.DefaultReviewTimeout.String()) {
		t.Errorf("derived deadline remedy omits the %s floor every cap below roughly 107000 tokens clamps to; got: %s", config.DefaultReviewTimeout, got)
	}
}

// The deadline log line in completeWithRetry names the derivation for the
// role that just expired. It must quote the same constants rather than a
// third transcription of the formula.
func TestDeadlineLogQuotesTheLiveConstants(t *testing.T) {
	var logs []string
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return "", model.ErrDeadline
	}}
	o := baseOptions(c)
	o.Logger = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if _, err := runOnce(t, baseInputs(), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(logs, "\n")
	if want := expectedDerivation("roles.review.max_output_tokens"); !strings.Contains(joined, want) {
		t.Errorf("deadline log does not state the derivation as the code computes it\nwant substring: %q\nlogs:\n%s", want, joined)
	}
}

// Two entry points resolve the same per-role settings: config.Config.Role,
// the exported resolver, and roleSettings, the one every model call reads.
// cmd/cite/review_config_test.go asserts an invariant holds "both in
// Config.Role and reviewer.roleSettings" while no test anywhere asserts that
// the two agree. This is that test: every field the review call actually
// uses must be identical from both, across each resolution layer.
func TestRoleAndRoleSettingsAgree(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{name: "no configuration at all", src: ""},
		{
			name: "explicit cap",
			src: `
model: openai/gpt-5-mini
roles:
  review: { max_output_tokens: 8192 }
`,
		},
		{
			name: "explicit cap and timeout",
			src: `
roles:
  review: { timeout: 45s, max_output_tokens: 32768, concurrency: 3 }
`,
		},
		{
			name: "model entry cap is the only cap",
			src: `
providers:
  gateway:
    base_url: https://gateway.example.invalid/v1
    api: openai-completions
    models:
      - id: narrow
        max_tokens: 8192
model: gateway/narrow
`,
		},
		{
			name: "explicit cap overrides a roomier model entry",
			src: `
providers:
  gateway:
    base_url: https://gateway.example.invalid/v1
    api: openai-completions
    models:
      - id: roomy
        max_tokens: 200000
model: gateway/roomy
roles:
  review: { max_output_tokens: 4096 }
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustCfg(t, tc.src)
			r := New(Options{Cfg: cfg, Client: &fakeClient{}})
			rc := cfg.Role(model.RoleReview)
			timeout, concurrency, maxTokens := r.roleSettings(model.RoleReview, 0, config.DefaultReviewConcurrency, config.DefaultReviewMaxOutputTokens)
			if rc.Model == "" {
				t.Error("Role(review).Model is empty; the ladder must always resolve a model")
			}
			if rc.Timeout != timeout {
				t.Errorf("review timeout disagrees: Config.Role = %v, roleSettings = %v", rc.Timeout, timeout)
			}
			if rc.Concurrency != concurrency {
				t.Errorf("review concurrency disagrees: Config.Role = %d, roleSettings = %d", rc.Concurrency, concurrency)
			}
			if rc.MaxOutputTokens != maxTokens {
				t.Errorf("review output cap disagrees: Config.Role = %d, roleSettings = %d (the cap the model call actually carries)", rc.MaxOutputTokens, maxTokens)
			}
		})
	}
}

// The triage and assemble roles carry a fixed deadline default, so the two
// entry points must agree on it as well. The built-in triage OUTPUT cap
// belongs to the call site (internal/reviewer owns that 8192 sizing), so only
// the deadline is compared here.
func TestTriageAndAssembleDeadlinesAgree(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{name: "no configuration at all", src: ""},
		{
			name: "explicit timeouts",
			src: `
roles:
  triage: { timeout: 120s }
  assemble: { timeout: 300s }
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustCfg(t, tc.src)
			r := New(Options{Cfg: cfg, Client: &fakeClient{}})
			triageTimeout, _, _ := r.roleSettings(model.RoleTriage, config.DefaultTriageTimeout, 1, defaultTriageMaxTokens)
			if got, want := cfg.Role(model.RoleTriage).Timeout, triageTimeout; got != want {
				t.Errorf("triage timeout disagrees: Config.Role = %v, roleSettings = %v", got, want)
			}
			assembleTimeout, _, _ := r.roleSettings(model.RoleAssemble, config.DefaultAssembleTimeout, 1, defaultTriageMaxTokens)
			if got, want := cfg.Role(model.RoleAssemble).Timeout, assembleTimeout; got != want {
				t.Errorf("assemble timeout disagrees: Config.Role = %v, roleSettings = %v", got, want)
			}
		})
	}
}
