package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// writeFiles writes name→content pairs into one temp directory and returns the
// paths in order, the way a layered --config list names them.
func writeFiles(t *testing.T, pairs ...string) []string {
	t.Helper()
	dir := t.TempDir()
	var out []string
	for i := 0; i+1 < len(pairs); i += 2 {
		p := filepath.Join(dir, pairs[i])
		if err := os.WriteFile(p, []byte(pairs[i+1]), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// testSettings resolves the operator settings exactly as a command does, from
// the flags a test sets and the environment it sets, so a test exercises the
// real ladder rather than a hand-built struct.
func testSettings(t *testing.T, groups flagGroup, configPaths []string, args ...string) (*settings, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	op := newOperatorFlags(fs, groups)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	for _, p := range configPaths {
		if err := op.config.Set(p); err != nil {
			return nil, err
		}
	}
	return op.resolve(givenFlags(fs))
}

// mustSettings is testSettings for the cases where a resolution error is a test
// failure.
func mustSettings(t *testing.T, configPaths ...string) *settings {
	t.Helper()
	st, err := testSettings(t, groupConfig|groupEndpoint|groupOutput|groupDiagnostics|groupBehaviour, configPaths)
	if err != nil {
		t.Fatalf("resolving settings: %v", err)
	}
	return st
}

// The ladder, from strongest to weakest, for one representative string setting:
// flag, then environment, then file, then the built-in default.
func TestSettingPrecedence(t *testing.T) {
	paths := writeFiles(t, "cfg.yml", "model_id: file-model\n")
	st, err := testSettings(t, groupConfig|groupEndpoint, paths, "--model-id", "flag-model")
	if err != nil {
		t.Fatal(err)
	}
	if st.ModelID != "flag-model" {
		t.Errorf("model id = %q, want the flag to win", st.ModelID)
	}

	t.Setenv("MODEL_ID", "env-model")
	st, err = testSettings(t, groupConfig|groupEndpoint, paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.ModelID != "env-model" {
		t.Errorf("model id = %q, want the environment to beat the file", st.ModelID)
	}

	t.Setenv("MODEL_ID", "")
	st, err = testSettings(t, groupConfig|groupEndpoint, paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.ModelID != "file-model" {
		t.Errorf("model id = %q, want the file to beat the default", st.ModelID)
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err = testSettings(t, groupConfig|groupEndpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.ModelID != "" {
		t.Errorf("model id = %q, want nothing when no rung names one", st.ModelID)
	}
}

// With no --config and no CITE_CONFIG, a run reads the repository file and then
// the machine file. On a machine that has one, its values apply without the
// operator naming the file.
func TestDefaultConfigListReadsTheMachineFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "cite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	machine := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(machine, []byte("max_comments: 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := mustSettings(t)
	if st.Config.MaxComments != 6 {
		t.Fatalf("max_comments = %d, want 6 from %s", st.Config.MaxComments, machine)
	}
	if len(st.Config.Read) != 1 || st.Config.Read[0] != machine {
		t.Fatalf("read = %v, want %s alone (the test runs outside a repository)", st.Config.Read, machine)
	}
}

// A boolean setting has two flags, because a file may set the value the command
// line wants to overturn.
func TestBooleanSettingCanBeTurnedOffFromTheCommandLine(t *testing.T) {
	paths := writeFiles(t, "cfg.yml", "verify: true\nrelated_code: false\n")
	st, err := testSettings(t, groupConfig|groupBehaviour, paths, "--no-verify", "--related-code")
	if err != nil {
		t.Fatal(err)
	}
	if st.Verify {
		t.Error("--no-verify did not turn verify off")
	}
	if !st.RelatedCode {
		t.Error("--related-code did not turn related code on")
	}

	t.Setenv("CITE_VERIFY", "off")
	st, err = testSettings(t, groupConfig|groupBehaviour, paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.Verify {
		t.Error("CITE_VERIFY=off did not turn verify off")
	}
}

// A variable that is not a byte count falls through to the configuration file
// rather than to the built-in default: a typo must not discard a setting the
// file states.
func TestBadCaptureMaxFallsThroughToTheFile(t *testing.T) {
	paths := writeFiles(t, "cfg.yml", "capture_max_bytes: 4096\n")
	t.Setenv("CITE_CAPTURE_MAX_BYTES", "not-a-count")
	st, err := testSettings(t, groupConfig|groupDiagnostics, paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.CaptureMaxBytes != 4096 {
		t.Fatalf("capture_max_bytes = %d, want the file's 4096", st.CaptureMaxBytes)
	}
	if st.Sources["capture_max_bytes"] != "config" {
		t.Errorf("source = %q, want config", st.Sources["capture_max_bytes"])
	}
}

// --config given once behaves as it always has, and passing it at all replaces
// the default list, so the home directory is never consulted behind a wrapper's
// back.
func TestConfigListReplacesTheDefault(t *testing.T) {
	paths := writeFiles(t, "only.yml", "max_comments: 3\n")
	st, err := testSettings(t, groupConfig, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 1 || st.Files[0] != paths[0] {
		t.Fatalf("files = %v, want exactly the file named", st.Files)
	}
	if st.Config.MaxComments != 3 {
		t.Errorf("max_comments = %d", st.Config.MaxComments)
	}
}

// CITE_CONFIG carries the same list colon-separated, in the same order.
func TestConfigListFromEnvironment(t *testing.T) {
	paths := writeFiles(t, "repo.yml", "max_comments: 3\n", "home.yml", "max_comments: 5\n")
	t.Setenv("CITE_CONFIG", paths[0]+":"+paths[1])
	st, err := testSettings(t, groupConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 2 {
		t.Fatalf("files = %v, want both from CITE_CONFIG", st.Files)
	}
	if st.Config.MaxComments != 5 {
		t.Errorf("max_comments = %d, want the later file to win", st.Config.MaxComments)
	}
}
