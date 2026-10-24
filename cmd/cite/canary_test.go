package main

// Tests for the canary leg construction and exit rules (§6).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

func testCfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cite.yml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func TestCanaryLegsProvidersAndFallbackDedup(t *testing.T) {
	cfg := testCfg(t, `
providers:
  gateway:
    base_url: https://gw.example/v1
    api: openai-completions
    api_key: $GATEWAY_KEY
    models:
      - id: model-x
  other:
    base_url: https://other.example/v1
    api: openai-completions
    api_key: literal-key
    models:
      - id: backup-model
fallback:
  - gateway/model-x      # duplicate of the provider's first model
  - other/backup-model   # also duplicate via other
`)
	legs := canaryLegs(cfg, &settings{})
	if len(legs) != 2 {
		t.Fatalf("want 2 deduped legs, got %d: %+v", len(legs), legs)
	}
	if !legs[0].primary {
		t.Fatal("first leg must be primary")
	}
	if legs[0].baseURL != "https://gw.example/v1" || legs[0].modelID != "model-x" {
		t.Fatalf("leg0 wrong: %+v", legs[0])
	}
}

// No configuration file is a real state: the canary has nothing to exercise and
// says so, rather than panicking on the absent list.
func TestCanaryLegsWithoutConfigIsEmpty(t *testing.T) {
	if got := canaryLegs(nil, &settings{}); len(got) != 0 {
		t.Fatalf("legs = %+v, want none", got)
	}
}

func TestCanaryLegsBareFallbackUsesThePrimaryEndpoint(t *testing.T) {
	// A bare model id has no provider of its own, so it is exercised against the
	// endpoint and the credential reference the primary model resolved to.
	cfg := testCfg(t, `
fallback:
  - bare-model-id
`)
	st := &settings{ModelBaseURL: "https://primary.example/v1", ModelAPIKey: "$PRIMARY_KEY"}
	legs := canaryLegs(cfg, st)
	if len(legs) != 1 {
		t.Fatalf("want 1 leg, got %d", len(legs))
	}
	if legs[0].baseURL != "https://primary.example/v1" {
		t.Fatalf("primary base url not used: %+v", legs[0])
	}
	if legs[0].apiKey != model.CredentialExpr("$PRIMARY_KEY") {
		t.Fatalf("expected the resolved credential expr: %+v", legs[0])
	}
}

func TestCanaryLegsUnknownProviderRejectedByValidation(t *testing.T) {
	// Config validation rejects an unknown provider in the fallback chain
	// before the canary ever runs; canaryLegs therefore never sees one.
	dir := t.TempDir()
	p := filepath.Join(dir, "cite.yml")
	yaml := "providers:\n  gateway:\n    base_url: https://gw.example/v1\n    api: openai-completions\n    api_key: $K\n    models:\n      - id: model-x\nfallback:\n  - nosuch/ghost-model\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(p); err == nil {
		t.Fatal("unknown provider in fallback must fail config validation")
	}
}

func TestCanaryExitRules(t *testing.T) {
	// The exit rules are: PRIMARY fails -> error; some fallbacks fail ->
	// loud note but success; all skipped -> success. These are encoded in
	// runCanary; here we verify the decision helper logic directly by
	// exercising runCanary against a fake server as the only leg.
	srvCalled := false
	ts := startFakeProvider(t, &srvCalled, `{"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`)
	dir := t.TempDir()
	p := filepath.Join(dir, "cite.yml")
	yaml := "providers:\n  gw:\n    base_url: " + ts.URL + "\n    api: openai-completions\n    api_key: literal\n    models:\n      - id: m\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCanary([]string{"--config", p}); err != nil {
		t.Fatalf("healthy primary should pass: %v", err)
	}
	if !srvCalled {
		t.Fatal("provider was never called")
	}
}
