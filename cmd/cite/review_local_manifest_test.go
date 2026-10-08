package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

// localPlainDiff is `git diff` output, the input the README documents for
// `cite review --diff`. Its first context line reads as a name-status row
// ("A", then a path) once trimmed.
const localPlainDiff = "diff --git a/x.txt b/x.txt\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/x.txt\n" +
	"+++ b/x.txt\n" +
	"@@ -1,2 +1,2 @@\n" +
	" A `tool` launch\n" +
	"-goodbye world\n" +
	"+hello world\n"

// TestReviewLocalManifestComesFromTheDiff fails on main: local mode built its
// manifest by reading name-status rows out of the diff text, so a plain diff
// gave the reviewer one forged file, "`tool`", and never reviewed x.txt.
func TestReviewLocalManifestComesFromTheDiff(t *testing.T) {
	dir := chdirTempWithFile(t, "x.txt", "A `tool` launch\nhello world\n")

	var called bool
	srv := startFakeProvider(t, &called, cleanReviewResponse)
	setFakeModelEnv(t, srv.URL)

	diffPath := filepath.Join(t.TempDir(), "diff.txt")
	if err := os.WriteFile(diffPath, []byte(localPlainDiff), 0o644); err != nil {
		t.Fatal(err)
	}

	var report bytes.Buffer
	_ = reviewLocal(diffPath, filepath.Join(dir, "absent.yml"), model.StructuredOutputResponseFormat, "", false, publisher.JSONReportSink(&report))

	var got struct {
		Run struct {
			Files []struct {
				Path  string `json:"path"`
				State string `json:"state"`
			} `json:"files"`
		} `json:"run"`
	}
	if err := json.Unmarshal(report.Bytes(), &got); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, report.String())
	}
	if len(got.Run.Files) != 1 || got.Run.Files[0].Path != "x.txt" || got.Run.Files[0].State != "reviewed" {
		t.Fatalf("files = %+v, want only x.txt reviewed\n%s", got.Run.Files, report.String())
	}
}
