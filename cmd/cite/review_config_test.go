package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

// Issue #59: the per-call deadline is only as truthful as the config that
// produced it. Two invariants at the cmd/cite config-load boundary:
//
//  1. CONTROL (documents existing, correct behavior): when .github/cite.yml is
//     VALID and carries roles.review.timeout, that explicit timeout IS the
//     effective per-call deadline — explicit beats derivation, both in
//     Config.Role and reviewer.roleSettings (already covered by
//     TestExplicitReviewTimeoutBeatsDerivation in internal/config and
//     internal/reviewer). This test pins that a valid config reaches the
//     effective role settings intact through loadConfig.
//
//  2. REPRO (fails on main for the loadConfig-fallback reason): when
//     .github/cite.yml is INVALID, loadConfig logs a warning to stderr and
//     silently returns config.Default(), discarding the entire roles block.
//     The run then proceeds as if configured — contacting the model with a
//     deadline the operator never chose (lifedraft's explicit 1500s was never
//     in force). An invalid config must fail the run before any model call.

const reviewDiff = `diff --git a/x.txt b/x.txt
new file mode 100644
--- /dev/null
+++ b/x.txt
@@ -0,0 +1 @@
+hello world
`

// cleanReviewResponse is a well-formed per-file review with no findings, so
// the only variable under test is the config-load boundary.
const cleanReviewResponse = `{"choices":[{"message":{"content":"{\"schema_version\":1,\"path\":\"x.txt\",\"outcome\":\"reviewed\",\"findings\":[]}"}}]}`

func setFakeModelEnv(t *testing.T, srvURL string) {
	t.Helper()
	t.Setenv("MODEL_API_KEY", "test-key")
	t.Setenv("MODEL_BASE_URL", srvURL)
	t.Setenv("MODEL_ID", "fake-model")
}

func chdirTempWithPostImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func writeDiffAndConfig(t *testing.T, configYAML string) (diffPath, cfgPath string) {
	t.Helper()
	dir := t.TempDir()
	diffPath = filepath.Join(dir, "diff.txt")
	if err := os.WriteFile(diffPath, []byte(reviewDiff), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(dir, "cite.yml")
	if err := os.WriteFile(cfgPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return diffPath, cfgPath
}

// TestValidConfigExplicitTimeoutWinsWhenLoaded is the CONTROL: it must pass on
// main. An explicit roles.review.timeout in a valid config reaches the
// effective role settings; it is not lost to the derived deadline.
func TestValidConfigExplicitTimeoutWinsWhenLoaded(t *testing.T) {
	_, cfgPath := writeDiffAndConfig(t, `roles:
  review:
    timeout: 1500s
    max_output_tokens: 32768
`)
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("valid config must load: %v", err)
	}
	rc := cfg.Role(model.RoleReview)
	if rc.Timeout != 1500*time.Second {
		t.Fatalf("explicit roles.review.timeout not in force after load: got %v, want 1500s", rc.Timeout)
	}
	if rc.MaxOutputTokens != 32768 {
		t.Fatalf("roles.review.max_output_tokens not in force after load: got %d, want 32768", rc.MaxOutputTokens)
	}
}

// TestInvalidConfigFailsClosed is the REPRO: it fails on main because
// loadConfig silently substitutes defaults for an invalid config and the run
// proceeds (contacting the model) as if the config had loaded.
func TestInvalidConfigFailsClosed(t *testing.T) {
	chdirTempWithPostImage(t)

	var called bool
	srv := startFakeProvider(t, &called, cleanReviewResponse)
	setFakeModelEnv(t, srv.URL)

	// Valid YAML shape, invalid configuration: the unknown role key makes the
	// whole file invalid (strict schema, §6). This mirrors the failure mode
	// where a typo invalidates an otherwise-carefully-tuned roles block.
	diffPath, cfgPath := writeDiffAndConfig(t, `roles:
  review:
    timeout: 1500s
    max_output_tokens: 32768
  bogus_role_key: true
`)

	var report bytes.Buffer
	err := reviewLocal(diffPath, cfgPath, publisher.JSONReportSink(&report))
	t.Logf("reviewLocal error: %v (model called=%v)", err, called)
	if called {
		t.Fatalf("invalid config must fail the run BEFORE any model call, but the reviewer ran against the fake provider (deadline would be the derived default, not the configured one)")
	}
	if err == nil {
		t.Fatal("invalid config must fail the run (fail closed), not conclude cleanly on defaults")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Fatalf("failure must name the config as the cause, got: %v", err)
	}
}
