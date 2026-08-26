package scope

import (
	"strings"
	"testing"
)

// GitHub's pull-request files API returns patches that start directly at the
// @@ hunk header — no "diff --git", "---", or "+++" lines. Feeding those into
// ParseUnifiedDiff fails with "hunk header before any file header", which the
// PR review path used to swallow silently: every finding was then dropped as
// anchor_invalid against an empty hunk table. ParseFilePatch synthesizes the
// missing headers from the manifest's known path/status so per-file patches
// parse standalone.
const bareNewFilePatch = `@@ -0,0 +1,5 @@
+package main
+
+func main() {
+	println("hi")
+}
`

func TestParseFilePatchAddedFile(t *testing.T) {
	df, err := ParseFilePatch("cmd/x/main.go", "A", bareNewFilePatch)
	if err != nil {
		t.Fatalf("ParseFilePatch: %v", err)
	}
	if df.Path != "cmd/x/main.go" {
		t.Errorf("path = %q", df.Path)
	}
	lines := df.AnchorableLines()
	for _, l := range []int{1, 3, 5} {
		if !lines[l] {
			t.Errorf("line %d not anchorable", l)
		}
	}
	if lines[6] {
		t.Errorf("line 6 should not exist")
	}
}

const bareModifiedPatch = `@@ -10,3 +10,3 @@ func existing() {
	context line stays
+	added line here
	more context
`

func TestParseFilePatchModifiedFile(t *testing.T) {
	df, err := ParseFilePatch("src/loop.go", "M", bareModifiedPatch)
	if err != nil {
		t.Fatalf("ParseFilePatch: %v", err)
	}
	lines := df.AnchorableLines()
	if !lines[10] || !lines[11] || !lines[12] {
		t.Errorf("hunk lines not anchorable: %v", lines)
	}
	if lines[9] || lines[13] {
		t.Errorf("lines outside the hunk are anchorable: %v", lines)
	}
}

// A structurally invalid hunk header must return an error (not a
// half-parsed file) so the caller can fail loudly for that one file.
func TestParseFilePatchMalformed(t *testing.T) {
	if _, err := ParseFilePatch("a.go", "M", "@@ garbage @@\n+code\n"); err == nil {
		t.Fatal("expected error for malformed hunk header")
	}
}

func TestParseFilePatchEmpty(t *testing.T) {
	if _, err := ParseFilePatch("a.go", "M", ""); err == nil {
		t.Fatal("expected error for empty patch")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Errorf("unexpected error text: %v", err)
	}
}
