package scope

import (
	"reflect"
	"testing"
)

// plainGitDiff is what `git diff main...` prints: no --name-status block, only
// file sections. The README context line in docs/intro.md has the form of a
// name-status row (" A `tool` launch ...") once its leading space is
// trimmed. A real review read such a line as an added file named "`tool`"
// and reviewed nothing else.
const plainGitDiff = `diff --git a/docs/intro.md b/docs/intro.md
index 1111111..2222222 100644
--- a/docs/intro.md
+++ b/docs/intro.md
@@ -1,3 +1,4 @@
 A ` + "`tool`" + ` launch can use one of two credentials.
+Before a launch, the tool checks for a newer release.
 M	looks/like/a/row.go
 last line
diff --git a/cmd/new.go b/cmd/new.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/cmd/new.go
@@ -0,0 +1,2 @@
+package cmd
+
diff --git a/cmd/old.go b/cmd/old.go
deleted file mode 100644
index 4444444..0000000
--- a/cmd/old.go
+++ /dev/null
@@ -1 +0,0 @@
-package cmd
diff --git a/docs/setup.md b/docs/getting-started.md
similarity index 90%
rename from docs/setup.md
rename to docs/getting-started.md
index 5555555..6666666 100644
--- a/docs/setup.md
+++ b/docs/getting-started.md
@@ -1,2 +1,2 @@
-# Setup
+# Getting started
 body
diff --git a/img/logo.png b/img/moved.png
similarity index 100%
rename from img/logo.png
rename to img/moved.png
`

func TestManifestFromDiffReadsFileSectionsOnly(t *testing.T) {
	d, err := ParseUnifiedDiff(plainGitDiff)
	if err != nil {
		t.Fatal(err)
	}
	got := ManifestFromDiff(d)
	want := []ManifestEntry{
		{Status: "M", Path: "docs/intro.md", Adds: 1},
		{Status: "A", Path: "cmd/new.go", Adds: 2},
		{Status: "D", Path: "cmd/old.go", Dels: 1},
		{Status: "R090", Path: "docs/getting-started.md", OldPath: "docs/setup.md", Adds: 1, Dels: 1},
		{Status: "R100", Path: "img/moved.png", OldPath: "img/logo.png"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ManifestFromDiff =\n%+v\nwant\n%+v", got, want)
	}
}

// TestRenameStatusIgnoresHeaderOrder: git prints "similarity index" before
// "rename from", but the status must not depend on that order.
func TestRenameStatusIgnoresHeaderOrder(t *testing.T) {
	d, err := ParseUnifiedDiff("diff --git a/a.go b/b.go\n" +
		"rename from a.go\n" +
		"rename to b.go\n" +
		"similarity index 100%\n" +
		"diff --git a/c.go b/d.go\n" +
		"copy from c.go\n" +
		"copy to d.go\n" +
		"similarity index 75%\n")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{d.Files[0].Status, d.Files[1].Status}
	if !reflect.DeepEqual(got, []string{"R100", "C075"}) {
		t.Fatalf("statuses = %v, want [R100 C075]", got)
	}
}

// TestUnparsableSimilarityIsAnError: the parse is structural, so a corrupt
// "similarity index" header fails like a corrupt hunk header does.
func TestUnparsableSimilarityIsAnError(t *testing.T) {
	_, err := ParseUnifiedDiff("diff --git a/a.go b/b.go\n" +
		"similarity index ninety%\n" +
		"rename from a.go\n" +
		"rename to b.go\n")
	if err == nil {
		t.Fatal("want an error for an unparsable similarity index")
	}
}
