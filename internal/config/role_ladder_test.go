package config

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/model"
)

// The rendered derivation must be computed from the constants, not
// transcribed beside them. This test fails if anyone re-inlines the literal
// in DeadlineFormula, and it moves by itself if a constant is recalibrated:
// the sentence an operator reads is generated from the same numbers the
// deadline is.
func TestReviewTimeoutFormulaTracksTheConstants(t *testing.T) {
	// Built from the constants rather than written down, so a changed base,
	// rate or floor changes this expectation instead of silently leaving
	// the prose behind.
	want := strconv.Itoa(int(ReviewTimeoutBase/time.Second)) + "s + roles.review.max_output_tokens/" +
		strconv.Itoa(AssumedGenerationRate) + " tok/s, floored at " + DefaultReviewTimeout.String()
	if got := ReviewTimeoutFormula(); got != want {
		t.Errorf("ReviewTimeoutFormula() = %q, want %q", got, want)
	}
	if got, want := DeadlineFormula("roles.triage.max_output_tokens"),
		strconv.Itoa(int(ReviewTimeoutBase/time.Second))+"s + roles.triage.max_output_tokens/"+
			strconv.Itoa(AssumedGenerationRate)+" tok/s, floored at "+DefaultReviewTimeout.String(); got != want {
		t.Errorf("DeadlineFormula(triage) = %q, want %q", got, want)
	}
}

// The rendered formula must describe the deadline DerivedReviewTimeout
// actually returns, at both ends of the clamp: a formula that survives the
// constants but disagrees with the function is a worse lie than a stale
// number.
func TestRenderedFormulaMatchesDerivedReviewTimeout(t *testing.T) {
	f := ReviewTimeoutFormula()
	if !strings.Contains(f, strconv.Itoa(AssumedGenerationRate)) {
		t.Errorf("formula %q does not quote the assumed generation rate %d", f, AssumedGenerationRate)
	}
	// Below the floor the deadline is the floor, and the formula says so.
	if got := DerivedReviewTimeout(4096); got != DefaultReviewTimeout {
		t.Errorf("DerivedReviewTimeout(4096) = %v, want the %v floor the formula names", got, DefaultReviewTimeout)
	}
	// Past the floor the deadline is exactly base + cap/rate.
	const cap = 230400
	if got, want := DerivedReviewTimeout(cap), ReviewTimeoutBase+time.Duration(cap)*time.Second/AssumedGenerationRate; got != want {
		t.Errorf("DerivedReviewTimeout(%d) = %v, want %v — the rendered formula promises this arithmetic", cap, got, want)
	}
}

// RoleSettings is the same ladder Role runs, with the caller's built-in
// defaults stated at the call site. Both must produce the effective cap.
func TestRoleSettingsAppliesEveryCapLayer(t *testing.T) {
	cases := []struct {
		name string
		src  string
		def  int
		want int
	}{
		{name: "built-in default applies when nothing is configured", src: "", def: 131072, want: 131072},
		{
			name: "model entry raises the cap",
			src: `
providers:
  gateway:
    base_url: https://gateway.example.invalid/v1
    api: openai-completions
    models:
      - id: roomy
        max_tokens: 200000
model: gateway/roomy
`,
			def: 131072, want: 200000,
		},
		{
			name: "the model entry lowers the cap",
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
			def: 131072, want: 8192,
		},
		{
			name: "an explicit cap outranks the model entry in both directions",
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
			def: 131072, want: 4096,
		},
		{name: "no default stated yields no cap rather than a wrong one", src: "", def: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, tc.src)
			rc := c.RoleSettings(model.RoleReview, RoleDefaults{MaxTokens: tc.def})
			if rc.MaxOutputTokens != tc.want {
				t.Errorf("RoleSettings(review).MaxOutputTokens = %d, want %d", rc.MaxOutputTokens, tc.want)
			}
			if got := rc.Timeout; got != DerivedReviewTimeout(rc.MaxOutputTokens) {
				t.Errorf("RoleSettings(review).Timeout = %v, want the derivation over the resolved cap %v", got, rc.MaxOutputTokens)
			}
			if got := c.Role(model.RoleReview); got.Timeout != rc.Timeout {
				t.Errorf("Role(review).Timeout = %v, RoleSettings = %v — one ladder, two entry points", got.Timeout, rc.Timeout)
			}
		})
	}
}

// An explicit timeout is a pin, and the pin must be reported as explicit by
// the one function that answers it. The last two cases cannot be loaded:
// validation rejects a zero and an unparsable timeout before a run starts, so
// they are built by hand to keep the defensive path honest.
func TestRoleTimeoutExplicit(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{name: "no roles block", cfg: mustParse(t, ""), want: false},
		{name: "pinned", cfg: mustParse(t, "roles:\n  review: { timeout: 45s }\n"), want: true},
		{name: "no timeout key", cfg: mustParse(t, "roles:\n  review: { max_output_tokens: 8192 }\n"), want: false},
		{name: "nil configuration", cfg: nil, want: false},
		{
			name: "zero is not a deadline",
			cfg:  &Config{Roles: map[model.Role]RoleSpec{model.RoleReview: {Timeout: "0s"}}},
			want: false,
		},
		{
			name: "unparsable",
			cfg:  &Config{Roles: map[model.Role]RoleSpec{model.RoleReview: {Timeout: "soon"}}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.RoleTimeoutExplicit(model.RoleReview); got != tc.want {
				t.Errorf("RoleTimeoutExplicit(review) = %v, want %v", got, tc.want)
			}
		})
	}
	// An absent configuration resolves to the default one rather than
	// panicking, which is what Load hands back for a missing file.
	if rc := (*Config)(nil).Role(model.RoleReview); rc.Model != DefaultModel ||
		rc.Timeout != DerivedReviewTimeout(DefaultReviewMaxOutputTokens) ||
		rc.MaxOutputTokens != DefaultReviewMaxOutputTokens || rc.Concurrency != DefaultReviewConcurrency {
		t.Errorf("Role on a nil Config = %+v, want the defaults", rc)
	}
}
