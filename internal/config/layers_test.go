package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles writes name→content into one temp directory and returns the
// paths in the order given on the command line.
func writeFiles(t *testing.T, pairs ...string) []string {
	t.Helper()
	dir := t.TempDir()
	var out []string
	for i := 0; i+1 < len(pairs); i += 2 {
		p := filepath.Join(dir, pairs[i])
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(pairs[i+1]), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// A later file replaces a scalar, leaves a key only it does not name alone,
// and merges a mapping per entry rather than erasing it.
func TestLoadFilesLaterWinsAndMappingsMerge(t *testing.T) {
	paths := writeFiles(t,
		"repo.yml", `
max_comments: 4
nits: true
providers:
  gateway:
    base_url: https://gw.example/v1
    api: openai-completions
    models:
      - id: model-a
  other:
    base_url: https://other.example/v1
    api: openai-completions
    models:
      - id: model-b
`,
		"home.yml", `
max_comments: 8
providers:
  gateway:
    api_key: $GATEWAY_KEY
`)
	got, err := LoadFiles(paths)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if got.MaxComments != 8 {
		t.Errorf("max_comments = %d, want 8 (the later file wins)", got.MaxComments)
	}
	if !got.Nits {
		t.Error("nits = false, want true (a key only the earlier file names survives)")
	}
	if p := got.Providers["gateway"]; p == nil || p.BaseURL != "https://gw.example/v1" || p.APIKey != "$GATEWAY_KEY" {
		t.Errorf("providers.gateway = %+v, want base_url from the first file and api_key from the second", p)
	}
	if p := got.Providers["other"]; p == nil {
		t.Error("providers.other disappeared: a later file must not erase an entry it does not name")
	}
}

// A sequence is replaced wholesale, never concatenated: a file that cannot
// shorten a list cannot undo an earlier one.
func TestLoadFilesSequencesReplaceWholesale(t *testing.T) {
	paths := writeFiles(t,
		"repo.yml", `
paths_ignore: ["**/*.gen.go", "vendor/**"]
blocking_categories: ["crash", "injection"]
`,
		"home.yml", `
paths_ignore: ["**/*.gen.go"]
blocking_categories: ["crash"]
`)
	got, err := LoadFiles(paths)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if len(got.PathsIgnore) != 1 || got.PathsIgnore[0] != "**/*.gen.go" {
		t.Errorf("paths_ignore = %v, want the later file's one entry", got.PathsIgnore)
	}
	if len(got.BlockingCategories) != 1 || got.BlockingCategories[0] != "crash" {
		t.Errorf("blocking_categories = %v, want the later file's one entry", got.BlockingCategories)
	}
}

// An unknown key in the second file fails with that file's name and the line
// the key sits on.
func TestLoadFilesUnknownKeyNamesFileAndLine(t *testing.T) {
	paths := writeFiles(t,
		"repo.yml", "max_comments: 4\n",
		"home.yml", "max_comments: 8\nbogus_key: true\n",
	)
	_, err := LoadFiles(paths)
	if err == nil {
		t.Fatal("an unknown key in the second file must fail the load")
	}
	msg := err.Error()
	if !strings.Contains(msg, paths[1]) {
		t.Errorf("error does not name the second file:\n%s", msg)
	}
	if !strings.Contains(msg, "bogus_key") {
		t.Errorf("error does not name the offending key:\n%s", msg)
	}
	if !strings.Contains(msg, paths[1]+":2:") {
		t.Errorf("error does not name the line the key sits on:\n%s", msg)
	}
}

// Validation runs on the merged result as well as per file: a fallback entry
// whose provider only the other file declares is valid, and an invalid
// combination is caught after merging.
func TestLoadFilesValidatesTheMergedResult(t *testing.T) {
	ok := writeFiles(t,
		"repo.yml", `
providers:
  gateway:
    base_url: https://gw.example/v1
    api: openai-completions
    models:
      - id: model-a
`,
		"home.yml", `fallback: ["gateway/model-a"]`+"\n",
	)
	if _, err := LoadFiles(ok); err != nil {
		t.Fatalf("a reference the other file resolves must validate after merging: %v", err)
	}

	bad := writeFiles(t,
		"repo.yml", "max_comments: 4\n",
		"home.yml", "max_comments: 30\n",
	)
	_, err := LoadFiles(bad)
	if err == nil {
		t.Fatal("the merged result must be validated: max_comments 30 exceeds the cap")
	}
	if !strings.Contains(err.Error(), bad[1]) {
		t.Errorf("the post-merge problem does not name the file that set it:\n%s", err)
	}
}

// One log line per verdict key a non-repository file changes, and none for
// the repository file itself.
func TestLoadFilesRecordsVerdictOverrides(t *testing.T) {
	paths := writeFiles(t,
		"repo.yml", `
max_comments: 4
nits: true
`,
		"home.yml", `
max_comments: 8
paths_ignore: ["docs/**"]
`,
	)
	got, err := LoadFiles(paths)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if len(got.Overrides) != 2 {
		t.Fatalf("overrides = %d (%+v), want 2: max_comments and paths_ignore", len(got.Overrides), got.Overrides)
	}
	if got.Overrides[0].Key != "max_comments" || got.Overrides[0].Before != "4" || got.Overrides[0].After != "8" || got.Overrides[0].File != paths[1] {
		t.Errorf("override 0 = %+v", got.Overrides[0])
	}

	// A single file has nothing to override: the repository file setting a
	// value is the normal case, not a surprise to announce.
	single, err := LoadFiles(paths[:1])
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if len(single.Overrides) != 0 {
		t.Errorf("a single file produced overrides: %+v", single.Overrides)
	}
}

// A file that is not on disk is skipped, so a machine with no user config and
// a repository with no .github/cite.yml both behave as they do today.
func TestLoadFilesSkipsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "absent-a.yml"), filepath.Join(dir, "absent-b.yml")}
	got, err := LoadFiles(paths)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if got.MaxComments != DefaultMaxComments {
		t.Errorf("max_comments = %d, want the default %d", got.MaxComments, DefaultMaxComments)
	}
	if len(got.Read) != 0 {
		t.Errorf("Read = %v, want nothing", got.Read)
	}
}

// Every value the merged result carries names the file it came from, or the
// built-in default, and a credential is never printed as a value.
func TestEffectiveNamesASourceForEveryValue(t *testing.T) {
	paths := writeFiles(t,
		"repo.yml", "max_comments: 4\npaths_ignore: [\"docs/**\"]\n",
		"home.yml", "model_api_key: $OPENROUTER_API_KEY\nproviders:\n  gateway:\n    base_url: https://gw.example/v1\n    api: openai-completions\n    api_key: sk-literal-for-the-test\n    models:\n      - id: model-a\n",
	)
	got, err := LoadFiles(paths)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	src := map[string]string{}
	for _, e := range got.Effective() {
		src[e.Key] = e.Source
		if strings.Contains(e.Value, "sk-literal-for-the-test") {
			t.Errorf("%s prints a literal credential: %q", e.Key, e.Value)
		}
	}
	for key, want := range map[string]string{
		"max_comments":               paths[0],
		"model_api_key":              paths[1],
		"providers.gateway.base_url": paths[1],
		"nits":                       "built-in default",
	} {
		if src[key] != want {
			t.Errorf("%s source = %q, want %q", key, src[key], want)
		}
	}
}

// One file behaves exactly as it does today: Load and LoadFiles agree.
func TestLoadFilesSingleFileMatchesLoad(t *testing.T) {
	paths := writeFiles(t, "repo.yml", "max_comments: 6\nnits: true\nroles:\n  review:\n    timeout: 90s\n")
	want, err := Load(paths[0])
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := LoadFiles(paths)
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	if got.MaxComments != want.MaxComments || got.Nits != want.Nits {
		t.Errorf("LoadFiles = %+v, Load = %+v", got.Config, want)
	}
	if a, b := got.Role("review"), want.Role("review"); a.Timeout != b.Timeout || a.MaxOutputTokens != b.MaxOutputTokens {
		t.Errorf("role settings differ: %+v vs %+v", a, b)
	}
}

// model_api_key is a reference, never a literal: the file that holds it is a
// dotfile as often as not, and a literal secret in a dotfiles repository is a
// leaked secret.
func TestModelAPIKeyRejectsALiteral(t *testing.T) {
	paths := writeFiles(t, "home.yml", "model_api_key: sk-literal-not-a-reference\n")
	_, err := LoadFiles(paths)
	if err == nil {
		t.Fatal("a literal model_api_key must be rejected")
	}
	if !strings.Contains(err.Error(), "model_api_key") {
		t.Errorf("error does not name the key: %v", err)
	}
	for _, ok := range []string{"$VAR", "${VAR}", "!op read op://vault/item/key"} {
		paths := writeFiles(t, "home.yml", "model_api_key: "+ok+"\n")
		if _, err := LoadFiles(paths); err != nil {
			t.Errorf("model_api_key: %s rejected: %v", ok, err)
		}
	}
}

// fallback_model_ids exists because `fallback` cannot hold a bare id once the
// file declares providers: every reference there must resolve to one. The
// separate key takes the id at the primary endpoint.
func TestBareFallbackIDNeedsItsOwnKeyWhenProvidersAreDeclared(t *testing.T) {
	const providers = `
providers:
  gateway:
    base_url: https://gw.example/v1
    api: openai-completions
    models:
      - id: model-a
`
	_, err := LoadFiles(writeFiles(t, "cfg.yml", "fallback: [bare-model]"+providers))
	if err == nil {
		t.Fatal("a bare id in fallback must be rejected once providers are declared")
	}
	if !strings.Contains(err.Error(), `must be "provider/modelid"`) {
		t.Fatalf("error does not explain the requirement: %v", err)
	}

	got, err := LoadFiles(writeFiles(t, "cfg.yml", "fallback: [gateway/model-a]\nfallback_model_ids: [bare-model]"+providers))
	if err != nil {
		t.Fatalf("both keys must be accepted together: %v", err)
	}
	if len(got.FallbackModelIDs) != 1 || got.FallbackModelIDs[0] != "bare-model" {
		t.Errorf("fallback_model_ids = %v, want the bare id", got.FallbackModelIDs)
	}
	if len(got.Fallback) != 1 || got.Fallback[0] != "gateway/model-a" {
		t.Errorf("fallback = %v, want the qualified reference", got.Fallback)
	}
}
