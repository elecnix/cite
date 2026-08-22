package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/model"
)

// mustParse parses src and fails the test on any problem.
func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error for valid config:\n%v", err)
	}
	return c
}

// problemsOf parses src and returns the validation error (nil if none).
// Syntax errors are wrapped as a single problem so table matching is uniform.
func problemsOf(t *testing.T, src string) *ValidationError {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		return nil
	}
	if ve, ok := err.(*ValidationError); ok {
		return ve
	}
	return &ValidationError{Problems: []Problem{{Message: err.Error()}}}
}

func hasProblemContaining(ve *ValidationError, substr string) bool {
	if ve == nil {
		return false
	}
	for _, p := range ve.Problems {
		if strings.Contains(p.String(), substr) {
			return true
		}
	}
	return false
}

func TestDefault(t *testing.T) {
	c := Default()
	if c.Model != DefaultModel {
		t.Errorf("Model = %q, want %q", c.Model, DefaultModel)
	}
	if c.MaxComments != DefaultMaxComments {
		t.Errorf("MaxComments = %d, want %d", c.MaxComments, DefaultMaxComments)
	}
	if c.Gate != GateComment {
		t.Errorf("Gate = %q, want %q", c.Gate, GateComment)
	}
	if c.CompatProfile != DefaultCompatProfile {
		t.Errorf("CompatProfile = %q, want %q", c.CompatProfile, DefaultCompatProfile)
	}
	if c.Nits {
		t.Error("Nits = true, want false")
	}
	if len(c.PathsIgnore) != 0 {
		t.Errorf("PathsIgnore = %v, want empty", c.PathsIgnore)
	}
	got := c.BlockingCategories
	want := DefaultBlockingCategories()
	if len(got) != len(want) {
		t.Fatalf("BlockingCategories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("BlockingCategories[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestDefaultBlockingCategories(t *testing.T) {
	got := DefaultBlockingCategories()
	want := []model.Category{
		model.CategorySecretExposure,
		model.CategoryInjection,
		model.CategoryAuthBypass,
		model.CategoryDestructiveOp,
		model.CategoryCrash,
		model.CategoryLogicInversion,
	}
	if len(got) != len(want) {
		t.Fatalf("DefaultBlockingCategories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	// The default set is exactly the set of categories that may block.
	for _, cat := range want {
		if !cat.MayBlock() {
			t.Errorf("%v reported MayBlock()==false but is in the default blocking set", cat)
		}
	}
	if model.CategoryConvention.MayBlock() {
		t.Error("convention must never block in any configuration")
	}
}

func TestLoadMissingFileYieldsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("Load(missing) = %v, want nil error", err)
	}
	if c.MaxComments != DefaultMaxComments {
		t.Errorf("Load(missing).MaxComments = %d, want default %d", c.MaxComments, DefaultMaxComments)
	}
}

func TestEmptyFileYieldsDefault(t *testing.T) {
	for name, src := range map[string]string{
		"empty":         "",
		"only comments": "# nothing here\n\n# still nothing\n",
		"whitespace":    "   \n \t\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(src))
			if err != nil {
				t.Fatalf("Parse(%q) = %v, want nil", src, err)
			}
			d := Default()
			if c.MaxComments != d.MaxComments || c.Gate != d.Gate ||
				c.CompatProfile != d.CompatProfile || c.Model != d.Model {
				t.Errorf("Parse(empty) = %+v, want defaults %+v", c, d)
			}
		})
	}
}

func TestParseFullExample(t *testing.T) {
	// The complete example from PLAN §6, plus a blocking-set shrink.
	src := `
# .github/cite.yml — optional. This is the complete v1 surface.
model: openai/gpt-5-mini      # one string; a role map is available below
max_comments: 10              # hard-capped at 20 by the schema
paths_ignore: ["**/*.gen.go", "vendor/**"]
nits: false                   # style and test-gap findings, default off
gate: comment                 # comment | block
compat_profile: "2026-08"     # which snapshot of the instruction formats to honour

providers:
  gateway:
    base_url: https://gateway.example.invalid/v1
    api: openai-completions          # openai-completions | openai-responses | anthropic-messages
    api_key: $MODEL_API_KEY          # literal | $VAR | ${VAR} | !shell-command
    headers:
      x-tenant: acme
    models:
      - id: vendor/model-x           # the only required field
        context_window: 262144
        max_tokens: 8192
        cost: { input: 5, output: 30, cache_read: 0.5, cache_write: 6.25 }
      - id: cheap-model

roles:
  review:   { model: gateway/vendor/model-x, timeout: 90s, max_output_tokens: 8192, concurrency: 8 }
  triage:   { model: gateway/cheap-model,    timeout: 30s }
  assemble: { model: gateway/cheap-model,    timeout: 60s }

fallback: [gateway/vendor/model-x, gateway/cheap-model]
`
	c := mustParse(t, src)
	if c.Model != "openai/gpt-5-mini" {
		t.Errorf("Model = %q", c.Model)
	}
	if c.MaxComments != 10 {
		t.Errorf("MaxComments = %d", c.MaxComments)
	}
	if len(c.PathsIgnore) != 2 || c.PathsIgnore[0] != "**/*.gen.go" || c.PathsIgnore[1] != "vendor/**" {
		t.Errorf("PathsIgnore = %v", c.PathsIgnore)
	}
	if c.Nits {
		t.Error("Nits = true")
	}
	if c.Gate != "comment" || c.CompatProfile != "2026-08" {
		t.Errorf("Gate=%q CompatProfile=%q", c.Gate, c.CompatProfile)
	}
	p := c.Providers["gateway"]
	if p == nil {
		t.Fatal("provider gateway missing")
	}
	if p.BaseURL != "https://gateway.example.invalid/v1" {
		t.Errorf("BaseURL = %q", p.BaseURL)
	}
	if p.API != model.APIOpenAICompletions {
		t.Errorf("API = %q", p.API)
	}
	if p.APIKey != model.CredentialExpr("$MODEL_API_KEY") {
		t.Errorf("APIKey = %q", p.APIKey)
	}
	if p.Headers["x-tenant"] != "acme" {
		t.Errorf("Headers = %v", p.Headers)
	}
	if len(p.Models) != 2 {
		t.Fatalf("len(Models) = %d", len(p.Models))
	}
	me := p.Models[0]
	if me.ID != "vendor/model-x" || me.ContextWindow != 262144 || me.MaxTokens != 8192 {
		t.Errorf("ModelEntry = %+v", me)
	}
	if me.Cost == nil || me.Cost.Input != 5 || me.Cost.Output != 30 ||
		me.Cost.CacheRead != 0.5 || me.Cost.CacheWrite != 6.25 {
		t.Errorf("Cost = %+v", me.Cost)
	}
	if rc := c.Role(model.RoleReview); rc.Model != "gateway/vendor/model-x" ||
		rc.Timeout != 90*time.Second || rc.MaxOutputTokens != 8192 || rc.Concurrency != 8 {
		t.Errorf("Role(review) = %+v", rc)
	}
	if rc := c.Role(model.RoleTriage); rc.Model != "gateway/cheap-model" || rc.Timeout != 30*time.Second {
		t.Errorf("Role(triage) = %+v", rc)
	}
	if len(c.Fallback) != 2 || c.Fallback[0] != "gateway/vendor/model-x" {
		t.Errorf("Fallback = %v", c.Fallback)
	}
}

func TestRoleDefaults(t *testing.T) {
	c := Default()
	cases := []struct {
		role        model.Role
		timeout     time.Duration
		concurrency int
	}{
		{model.RoleReview, 120 * time.Second, 8},
		{model.RoleTriage, 30 * time.Second, 0},
		{model.RoleAssemble, 60 * time.Second, 0},
	}
	for _, tc := range cases {
		rc := c.Role(tc.role)
		if rc.Timeout != tc.timeout {
			t.Errorf("Role(%s).Timeout = %v, want %v", tc.role, rc.Timeout, tc.timeout)
		}
		if rc.Concurrency != tc.concurrency {
			t.Errorf("Role(%s).Concurrency = %d, want %d", tc.role, rc.Concurrency, tc.concurrency)
		}
		if rc.Model != DefaultModel {
			t.Errorf("Role(%s).Model = %q, want fallback to default %q", tc.role, rc.Model, DefaultModel)
		}
	}
}

func TestRoleOverrides(t *testing.T) {
	src := `
model: openai/gpt-5-mini
roles:
  review: { timeout: 45s, concurrency: 2, max_output_tokens: 1024 }
  triage: { model: gpt-5-nano }
`
	c := mustParse(t, src)
	rc := c.Role(model.RoleReview)
	if rc.Timeout != 45*time.Second || rc.Concurrency != 2 || rc.MaxOutputTokens != 1024 {
		t.Errorf("Role(review) = %+v", rc)
	}
	if rc.Model != "openai/gpt-5-mini" {
		t.Errorf("Role(review).Model = %q, want fallback to top-level model", rc.Model)
	}
	rc = c.Role(model.RoleTriage)
	if rc.Model != "gpt-5-nano" || rc.Timeout != 30*time.Second {
		t.Errorf("Role(triage) = %+v", rc)
	}
}

// ---- validation: every rule gets a failing and (where relevant) a passing case ----

func TestValidationTable(t *testing.T) {
	providers := `
providers:
  gateway:
    base_url: https://gw.example/v1
    api: openai-completions
    api_key: $KEY
`
	providersWithModels := providers + `    models:
      - id: m1
`
	tests := []struct {
		name string
		src  string
		want string // substring expected in some problem message; "" means no error
	}{
		{"empty ok", "", ""},
		{"cap boundary ok", "max_comments: 20", ""},
		{"cap exceeded", "max_comments: 21", "hard cap of 20"},
		{"negative comments", "max_comments: -1", "must not be negative"},
		{"gate ok block", "gate: block", ""},
		{"gate bad", "gate: enforce", `"enforce" is not one of`},
		{"unknown top key", "gates: block", "unknown key"},
		{"unknown nested key in provider", providers + "    typo_key: 1\n", "unknown key"},
		{"unknown key in model entry", providers + "    models:\n      - id: m1\n        seedy: 1\n", "unknown key"},
		{"unknown role name", providers + "roles:\n  audit: { model: gateway/m1 }", "unknown key"},
		{"wrong type for nits", "nits: yes-please", "expected true or false"},
		{"wrong type for max_comments", "max_comments: ten", "expected an integer"},
		{"wrong type for paths_ignore", "paths_ignore: vendor", "expected a list"},
		{"provider missing api", "providers:\n  g:\n    base_url: https://x\n    api_key: $K", "required field is missing"},
		{"bad api style", "providers:\n  g:\n    base_url: https://x\n    api: grpc\n", `"grpc" is not one of`},
		{"model entry missing id", providers + "    models:\n      - context_window: 5\n", "required field is missing"},
		{"bad timeout", providersWithModels + "roles:\n  review: { model: gateway/m1, timeout: soon }", "not a parseable duration"},
		{"zero timeout", providersWithModels + "roles:\n  review: { model: gateway/m1, timeout: 0s }", "must be positive"},
		{"unknown provider in role", providersWithModels + "roles:\n  review: { model: other/m1 }", `unknown provider "other"`},
		{"unknown model id in role", providersWithModels + "roles:\n  review: { model: gateway/nope }", `declares no model with id "nope"`},
		{"plain name rejected when providers declared", providers + "roles:\n  review: { model: gpt-5-mini }", `must be "provider/modelid"`},
		{"slash form rejected without providers", "roles:\n  review: { model: gateway/m1 }", `unknown provider "gateway"`},
		{"plain builtin ok without providers", "roles:\n  review: { model: gpt-5-mini }", ""},
		{"fallback resolves", providersWithModels + "fallback: [gateway/m1]", ""},
		{"fallback unknown", providersWithModels + "fallback: [gateway/nope]", `declares no model with id`},
		{"api_key bad var", "providers:\n  g:\n    base_url: https://x\n    api: anthropic-messages\n    api_key: $1BAD", "must be $VAR"},
		{"api_key bad brace", "providers:\n  g:\n    base_url: https://x\n    api: anthropic-messages\n    api_key: ${UNCLOSED", "must be ${VAR}"},
		{"api_key empty command", "providers:\n  g:\n    base_url: https://x\n    api: anthropic-messages\n    api_key: \"!\"", `must not be empty`},
		{"api_key literal ok", "providers:\n  g:\n    base_url: https://x\n    api: anthropic-messages\n    api_key: sk-literal", ""},
		{"api_key command ok", "providers:\n  g:\n    base_url: https://x\n    api: anthropic-messages\n    api_key: \"!op read vault\"", ""},
		{"api_key env ok", "providers:\n  g:\n    base_url: https://x\n    api: anthropic-messages\n    api_key: ${MODEL_KEY}", ""},
		{"duplicate yaml key", "model: a\nmodel: b", "duplicate key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ve := problemsOf(t, tc.src)
			if tc.want == "" {
				if ve != nil {
					t.Fatalf("want no problems, got:\n%v", ve)
				}
				return
			}
			if !hasProblemContaining(ve, tc.want) {
				t.Fatalf("want problem containing %q, got:\n%v", tc.want, ve)
			}
		})
	}
}

func TestValidationAggregatesAllProblems(t *testing.T) {
	src := `
gate: nope
max_comments: 99
providers:
  g:
    base_url: https://x
    api: openai-responses
    api_key: $OK
    models:
      - id: m1
roles:
  review: { model: g/nope, timeout: never }
`
	ve := problemsOf(t, src)
	if ve == nil {
		t.Fatal("want problems")
	}
	for _, want := range []string{"gate", "hard cap", "declares no model", "not a parseable duration"} {
		if !hasProblemContaining(ve, want) {
			t.Errorf("expected a problem containing %q in:\n%v", want, ve)
		}
	}
}

func TestBlockingCategoriesShrinkOk(t *testing.T) {
	src := `
blocking_categories: [secret-exposure, injection]
`
	c := mustParse(t, src)
	if len(c.BlockingCategories) != 2 {
		t.Fatalf("BlockingCategories = %v", c.BlockingCategories)
	}
	if c.BlockingCategories[0] != model.CategorySecretExposure ||
		c.BlockingCategories[1] != model.CategoryInjection {
		t.Errorf("BlockingCategories = %v", c.BlockingCategories)
	}
}

func TestBlockingCategoriesGrowRejected(t *testing.T) {
	// resource-leak is a real category but not in the default blocking set.
	src := "blocking_categories: [secret-exposure, resource-leak]\n"
	ve := problemsOf(t, src)
	if !hasProblemContaining(ve, "may never block") {
		t.Fatalf("growing the blocking set must be rejected, got:\n%v", ve)
	}
}

func TestBlockingCategoriesConventionNeverBlocks(t *testing.T) {
	src := "blocking_categories: [convention]\n"
	ve := problemsOf(t, src)
	if !hasProblemContaining(ve, "convention can never block") {
		t.Fatalf("convention must never be blockable, got:\n%v", ve)
	}
}

func TestBlockingCategoriesUnknownAndDuplicate(t *testing.T) {
	ve := problemsOf(t, "blocking_categories: [severity-critical]\n")
	if !hasProblemContaining(ve, "not one of") {
		t.Fatalf("unknown category must be rejected, got:\n%v", ve)
	}
	ve = problemsOf(t, "blocking_categories: [crash, crash]\n")
	if !hasProblemContaining(ve, "duplicate") {
		t.Fatalf("duplicate category must be rejected, got:\n%v", ve)
	}
}

// ---- YAML subset edge cases ----

func TestYAMLEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // substring of the error, "" means parse succeeds
	}{
		{"quoted string with colon", `model: "a: b"`, ""},
		{"quoted string with hash", `model: "a # b"`, ""},
		{"single quoted", `compat_profile: '2026-08'`, ""},
		{"comment after value", "model: x # note", ""},
		{"hash inside unquoted value kept", "compat_profile: 2026-08 # c", ""},
		{"url value not a mapping", "providers:\n  g:\n    base_url: https://gw.example/v1\n    api: openai-responses\n", ""},
		{"tab indentation", "providers:\n\tg: {}\n", "tab in indentation"},
		{"unterminated quote", `model: "abc`, "unterminated quoted string"},
		{"unterminated flow", "paths_ignore: [a, b", "unterminated flow"},
		{"missing colon", "model x\n", `expected "key: value"`},
		{"bad indentation", "providers:\n    g:\n      api: x\n   h: 1\n", "unexpected indentation"},
		{"top level not a mapping", "- a\n- b\n", "must be a mapping"},
		{"stray content after doc", "model: x\nstray\n", `expected "key: value"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestYAMLScalarTyping(t *testing.T) {
	src := `
nits: true
max_comments: 12
providers:
  g:
    base_url: https://x
    api: anthropic-messages
    api_key: "12345"        # quoted: stays a string, fine as a literal
    models:
      - id: m
        cost:
          input: 0
          output: 3.5
`
	c := mustParse(t, src)
	if !c.Nits || c.MaxComments != 12 {
		t.Errorf("Nits=%v MaxComments=%d", c.Nits, c.MaxComments)
	}
	cost := c.Providers["g"].Models[0].Cost
	if cost == nil || cost.Input != 0 || cost.Output != 3.5 {
		t.Errorf("Cost = %+v", cost)
	}
	if c.Providers["g"].APIKey != model.CredentialExpr("12345") {
		t.Errorf("APIKey = %q", c.Providers["g"].APIKey)
	}
}

func TestLoadFromDiskAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cite.yml")
	if err := os.WriteFile(path, []byte("max_comments: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	ve, ok := err.(*ValidationError)
	if !ok || !hasProblemContaining(ve, "hard cap of 20") {
		t.Fatalf("Load(bad file) = %v, want ValidationError about the cap", err)
	}

	if err := os.WriteFile(path, []byte("gate: block\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if c.Gate != GateBlock {
		t.Errorf("Gate = %q", c.Gate)
	}
}

func TestSchemaJSONIsEmbeddedAndValid(t *testing.T) {
	if len(schemaJSON) == 0 {
		t.Fatal("schema.json not embedded")
	}
	if _, ok := rootSchema()["properties"]; !ok {
		t.Fatal("schema has no properties")
	}
	// The schema must cap max_comments at 20.
	props := rootSchema()["properties"].(map[string]interface{})
	mc := props["max_comments"].(map[string]interface{})
	if mc["maximum"].(float64) != 20 {
		t.Errorf("schema maximum = %v, want 20", mc["maximum"])
	}
}
