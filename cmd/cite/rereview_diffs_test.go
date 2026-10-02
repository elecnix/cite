package main

import (
	"testing"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/scope"
)

// ghPatch is a patch in the shape GitHub's PR-files API returns it: bare @@
// hunks with no "diff --git", "---" or "+++" lines.
const ghPatch = `@@ -143,6 +143,7 @@ func handler(w http.ResponseWriter, r *http.Request) {
 	if !authed(w, r) {
 		return
 	}
+	raw, err := io.ReadAll(r.Body)
+	if err != nil {
+		http.Error(w, "unreadable body", http.StatusBadRequest)
+	}
 	render(w, raw)
 }
`

func rereviewManifest() []scope.ManifestEntry {
	return []scope.ManifestEntry{
		{Status: "M", Path: "internal/http/handler.go", Adds: 3, Dels: 0},
	}
}

func rereviewExtras() map[string]githubclient.FileExtra {
	return map[string]githubclient.FileExtra{
		"internal/http/handler.go": {
			Filename: "internal/http/handler.go",
			Patch:    ghPatch,
		},
	}
}

// A GitHub patch has no file headers, so parsing the concatenated batch with
// ParseUnifiedDiff fails on the first hunk and the diff map stays empty. Every
// anchor is then unanchorable and every finding the re-review produces is
// dropped anchor_invalid.
func TestRereviewDiffsBatchParseLosesEverything(t *testing.T) {
	var sb string
	for _, e := range rereviewManifest() {
		sb += rereviewExtras()[e.Path].Patch + "\n"
	}
	if _, err := scope.ParseUnifiedDiff(sb); err == nil {
		t.Fatal("ParseUnifiedDiff accepted a header-less GitHub patch; " +
			"the whole-batch parse cannot be the bug under test")
	} else {
		t.Logf("ParseUnifiedDiff rejects the batch: %v", err)
	}

	diffs := buildRereviewDiffs(rereviewManifest(), rereviewExtras(), t.Logf)
	if len(diffs) == 0 {
		t.Fatalf("buildRereviewDiffs produced an empty diff map for %d manifest file(s) with patches; "+
			"no anchor can validate against it", len(rereviewManifest()))
	}
}

// Per-file parsing must produce anchorable post-change lines that include the
// added lines, so a finding anchored on an added line survives validation.
func TestRereviewDiffsAnchorableCoversAddedLines(t *testing.T) {
	diffs := buildRereviewDiffs(rereviewManifest(), rereviewExtras(), t.Logf)
	df, ok := diffs["internal/http/handler.go"]
	if !ok {
		t.Fatalf("no DiffFile for internal/http/handler.go, got %v", diffs)
	}
	added := df.AddedLines()
	if len(added) == 0 {
		t.Fatal("DiffFile has no added lines; blocking requires anchorHasAddedLine")
	}
	anchorable := df.AnchorableLines()
	for _, n := range added {
		if !anchorable[n] {
			t.Fatalf("added line %d is not anchorable", n)
		}
	}
	if df.Path != "internal/http/handler.go" {
		t.Fatalf("DiffFile.Path = %q, want the manifest path", df.Path)
	}
}

// A file with no patch (binary, too large, deleted) must be skipped, not fatal,
// and a patch that cannot be parsed must not discard the other files.
func TestRereviewDiffsSkipsUnpatchedAndIsolatesBadPatch(t *testing.T) {
	entries := []scope.ManifestEntry{
		{Status: "M", Path: "good.go", Adds: 1},
		{Status: "M", Path: "binary.png"},
		{Status: "D", Path: "gone.go"},
	}
	extras := map[string]githubclient.FileExtra{
		"good.go": {
			Filename: "good.go",
			Patch:    "@@ -1,1 +1,2 @@\n ctx\n+added\n",
		},
		"binary.png": {Filename: "binary.png", Patch: ""},
	}
	diffs := buildRereviewDiffs(entries, extras, t.Logf)
	if len(diffs) != 1 {
		t.Fatalf("got %d diff files, want exactly the one patched file: %v", len(diffs), diffs)
	}
	if _, ok := diffs["good.go"]; !ok {
		t.Fatalf("patched file missing from diff map: %v", diffs)
	}
}
