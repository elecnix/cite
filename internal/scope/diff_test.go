package scope

import (
	"reflect"
	"testing"
)

const sampleDiff = `diff --git a/internal/webhook/handler.go b/internal/webhook/handler.go
index 1111111..2222222 100644
--- a/internal/webhook/handler.go
+++ b/internal/webhook/handler.go
@@ -140,6 +140,8 @@ func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 	body, err := io.ReadAll(r.Body)
 	if err != nil {
 		http.Error(w, "bad request", http.StatusBadRequest)
+		return
 		return
 	}
+	sig := r.Header.Get("Stripe-Signature")
 	if sig == "" {
@@ -200,5 +201,3 @@ func (h *Handler) verify(sig string, body []byte) error {
 	old1
-	dead := compute(body)
-	more := dead + 1
 	old2
 }
diff --git a/docs/setup.md b/docs/getting-started.md
similarity index 90%
rename from docs/setup.md
rename to docs/getting-started.md
diff --git a/new_file.txt b/new_file.txt
new file mode 100644
--- /dev/null
+++ b/new_file.txt
@@ -0,0 +1,2 @@
+hello
+world
`

func TestParseUnifiedDiff(t *testing.T) {
	d, err := ParseUnifiedDiff(sampleDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if len(d.Files) != 3 {
		t.Fatalf("files = %d, want 3", len(d.Files))
	}

	h := d.Files[0]
	if h.Path != "internal/webhook/handler.go" || h.OldPath != "internal/webhook/handler.go" {
		t.Fatalf("paths = %q %q", h.OldPath, h.Path)
	}
	if len(h.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(h.Hunks))
	}

	first := h.Hunks[0]
	if first.OldStart != 140 || first.OldLines != 6 || first.NewStart != 140 || first.NewLines != 8 {
		t.Fatalf("hunk header = %+v", first)
	}

	// Line numbering: context lines carry both numbers; added lines only new.
	l0 := first.Lines[0]
	if l0.Kind != LineContext || l0.OldNo != 140 || l0.NewNo != 140 || l0.Content != "\tbody, err := io.ReadAll(r.Body)" {
		t.Fatalf("line 0 = %+v", l0)
	}
	l3 := first.Lines[3] // "+\t\treturn"
	if l3.Kind != LineAdded || l3.NewNo != 143 || l3.OldNo != 0 {
		t.Fatalf("added line = %+v", l3)
	}
	last := first.Lines[len(first.Lines)-1]
	if last.Kind != LineContext || last.OldNo != 145 || last.NewNo != 147 {
		t.Fatalf("last = %+v", last)
	}

	// Removed lines carry OLD numbers only.
	second := h.Hunks[1]
	if second.OldStart != 200 || second.NewStart != 201 {
		t.Fatalf("second header = %+v", second)
	}
	var removed []Line
	for _, l := range second.Lines {
		if l.Kind == LineRemoved {
			removed = append(removed, l)
		}
	}
	if len(removed) != 2 || removed[0].OldNo != 201 || removed[1].OldNo != 202 {
		t.Fatalf("removed = %+v", removed)
	}
	for _, l := range removed {
		if l.NewNo != 0 {
			t.Fatalf("removed line has post-change number: %+v", l)
		}
	}
}

func TestParseUnifiedDiffRenameAndNewFile(t *testing.T) {
	d, err := ParseUnifiedDiff(sampleDiff)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	rn := d.Files[1]
	if rn.OldPath != "docs/setup.md" || rn.Path != "docs/getting-started.md" {
		t.Fatalf("rename paths = %q -> %q", rn.OldPath, rn.Path)
	}

	nf := d.Files[2]
	if nf.OldPath != "" || nf.Path != "new_file.txt" {
		t.Fatalf("new file paths = %q %q", nf.OldPath, nf.Path)
	}
	added := nf.AddedLines()
	if !reflect.DeepEqual(added, []int{1, 2}) {
		t.Fatalf("AddedLines = %v", added)
	}
}

func TestAddedAndAnchorableLines(t *testing.T) {
	d, _ := ParseUnifiedDiff(sampleDiff)
	h := d.Files[0]

	wantAdded := []int{143, 146}
	if got := h.AddedLines(); !reflect.DeepEqual(got, wantAdded) {
		t.Fatalf("AddedLines = %v, want %v", got, wantAdded)
	}

	// Anchorable = added ∪ context, by post-change number. First hunk
	// covers 140–147; second hunk (@@ -200,5 +201,3 @@) covers 201–203.
	wantAnchor := map[int]bool{
		140: true, 141: true, 142: true, 143: true, 144: true, 145: true, 146: true, 147: true,
		201: true, 202: true, 203: true,
	}
	if !reflect.DeepEqual(h.AnchorableLines(), wantAnchor) {
		t.Fatalf("AnchorableLines = %v, want %v", h.AnchorableLines(), wantAnchor)
	}
	// Removed lines are never anchorable: they have no post-change anchor.
	for _, old := range []int{200, 201, 202, 203, 204} {
		if h.AnchorableLines()[old] && old == 200 {
			t.Fatal("old-only line must not be anchorable")
		}
	}

	// RemovedLines returns old-numbered lines for the <removed_lines> block.
	rm := h.RemovedLines()
	if len(rm) != 2 || rm[0].OldNo != 201 || rm[1].OldNo != 202 {
		t.Fatalf("RemovedLines = %+v", rm)
	}
}

func TestFileByPath(t *testing.T) {
	d, _ := ParseUnifiedDiff(sampleDiff)
	if d.FileByPath("internal/webhook/handler.go") == nil {
		t.Fatal("handler not found")
	}
	// Rename source resolves to the same file entry.
	f := d.FileByPath("docs/setup.md")
	if f == nil || f.Path != "docs/getting-started.md" {
		t.Fatalf("rename source lookup = %+v", f)
	}
	if d.FileByPath("nope.go") != nil {
		t.Fatal("unknown path returned a file")
	}
}

func TestParseUnifiedDiffErrors(t *testing.T) {
	if _, err := ParseUnifiedDiff("@@ -1,1 +1,1 @@\n+x\n"); err == nil {
		t.Fatal("hunk before file header must error")
	}
	if _, err := ParseUnifiedDiff("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ garbage @@\n+x\n"); err == nil {
		t.Fatal("malformed hunk header must error")
	}
}
