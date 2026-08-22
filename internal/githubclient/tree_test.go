package githubclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/instructions"
)

// TestFSTreeSatisfiesTree pins the interface contract at compile time and
// exercises List/Read against a temp dir.
func TestFSTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("AGENTS.md", "# top-level instructions\n")
	write("docs/review.md", "## review rules\nbe strict\n")
	write("docs/deep/nested.txt", "x")
	write("skip.bin", "\x00\x01") // regular file, still listed

	var tree instructions.Tree = NewFSTree(root)

	all, err := tree.List("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AGENTS.md", "docs/deep/nested.txt", "docs/review.md", "skip.bin"}
	if len(all) != len(want) {
		t.Fatalf("List = %v", all)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("List not sorted/complete: %v", all)
		}
	}
	docs, err := tree.List("docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0] != "docs/deep/nested.txt" || docs[1] != "docs/review.md" {
		t.Fatalf("List(docs) = %v", docs)
	}
	raw, ok, err := tree.Read("docs/review.md")
	if err != nil || !ok || !strings.Contains(string(raw), "be strict") {
		t.Fatalf("Read = %q ok=%v err=%v", raw, ok, err)
	}
	if _, ok, err := tree.Read("missing.md"); err != nil || ok {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	// Reads must not escape the root.
	if _, _, err := tree.Read("../../etc/passwd"); err == nil {
		t.Error("path traversal accepted")
	}
}

// fakeGH serves the three endpoints APITree touches: the recursive tree
// listing, the contents API and the blobs API.
type fakeGH struct {
	t         *testing.T
	treeJSON  string
	contents  map[string]string // path -> raw file content
	truncated map[string]bool   // path -> contents payload truncated
	blob      map[string]string // sha -> raw blob content
	treesHits int
	blobHits  map[string]int
}

func (f *fakeGH) handler() http.HandlerFunc {
	f.blobHits = map[string]int{}
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/octo/hello/git/trees/"):
			if r.URL.Query().Get("recursive") != "1" {
				f.t.Errorf("tree listing must be recursive=1, got %q", r.URL.RawQuery)
			}
			f.treesHits++
			fmt.Fprint(w, f.treeJSON)
		case strings.HasPrefix(r.URL.Path, "/repos/octo/hello/contents/"):
			path := strings.TrimPrefix(r.URL.Path, "/repos/octo/hello/contents/")
			if r.URL.Query().Get("ref") != "base" {
				f.t.Errorf("contents ref = %q", r.URL.Query().Get("ref"))
			}
			raw, ok := f.contents[path]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"message":"Not Found"}`)
				return
			}
			enc := ""
			trunc := f.truncated[path]
			// Simulate GitHub's 1 MB contents cap: a truncated payload is
			// valid base64 of a content prefix.
			if trunc && len(raw) > 8 {
				raw = raw[:8]
			}
			enc = base64.StdEncoding.EncodeToString([]byte(raw))
			fmt.Fprintf(w, `{"sha":"sha-%s","content":%q,"encoding":"base64","truncated":%t}`,
				path, enc, trunc)
		case strings.HasPrefix(r.URL.Path, "/repos/octo/hello/git/blobs/"):
			sha := strings.TrimPrefix(r.URL.Path, "/repos/octo/hello/git/blobs/")
			f.blobHits[sha]++
			raw, ok := f.blob[sha]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"message":"Not Found"}`)
				return
			}
			enc := base64.StdEncoding.EncodeToString([]byte(raw))
			fmt.Fprintf(w, `{"content":%q,"encoding":"base64"}`, enc)
		default:
			f.t.Fatalf("unexpected request %s", r.URL)
		}
	}
}

func newAPITreeFixture(t *testing.T) (*APITree, *fakeGH) {
	f := &fakeGH{
		t: t,
		treeJSON: `{"sha":"tree1","truncated":false,"tree":[
			{"path":"AGENTS.md","type":"blob","sha":"blob-agents"},
			{"path":"docs","type":"tree","sha":"tree-docs"},
			{"path":"docs/review.md","type":"blob","sha":"blob-review"},
			{"path":"big.bin","type":"blob","sha":"blob-big"}
		]}`,
		contents: map[string]string{
			"AGENTS.md":      "agents instructions",
			"docs/review.md": "review rules",
			"big.bin":        "0123456789ABCDEF",
		},
		truncated: map[string]bool{"big.bin": true},
		blob:      map[string]string{"blob-big": "0123456789ABCDEF", "blob-agents": "agents instructions"},
	}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c := New("tok", srv.URL, srv.Client())
	return NewAPITree(c, "octo", "hello", "base"), f
}

func TestAPITree(t *testing.T) {
	tree, f := newAPITreeFixture(t)

	var itree instructions.Tree = tree
	all, err := itree.List("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AGENTS.md", "big.bin", "docs/review.md"} // tree entries excluded
	if fmt.Sprint(all) != fmt.Sprint(want) {
		t.Fatalf("List = %v, want %v", all, want)
	}
	if f.treesHits != 1 {
		t.Fatalf("listing fetched %d times, want 1 (cached)", f.treesHits)
	}
	// Second List uses the cache.
	if _, err := itree.List("docs"); err != nil {
		t.Fatal(err)
	}
	if f.treesHits != 1 {
		t.Fatalf("cache miss: listing fetched %d times", f.treesHits)
	}
	raw, ok, err := itree.Read("AGENTS.md")
	if err != nil || !ok || string(raw) != "agents instructions" {
		t.Fatalf("Read AGENTS.md = %q ok=%v err=%v", raw, ok, err)
	}
	if _, ok, err := itree.Read("nope.txt"); err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
	// A truncated contents payload falls back to the blobs endpoint and
	// still reads whole.
	raw, ok, err = itree.Read("big.bin")
	if err != nil || !ok {
		t.Fatalf("big.bin: ok=%v err=%v", ok, err)
	}
	if string(raw) != "0123456789ABCDEF" {
		t.Fatalf("truncated read not routed around: %q", raw)
	}
	if f.blobHits["blob-big"] != 1 {
		t.Errorf("blob endpoint hits = %v", f.blobHits)
	}
	// Non-truncated files never touch the blobs endpoint.
	if f.blobHits["blob-agents"] != 0 {
		t.Errorf("agents blob fetched despite untruncated contents: %v", f.blobHits)
	}
}

func TestAPITreeTruncatedListing(t *testing.T) {
	tree, f := newAPITreeFixture(t)
	f.treeJSON = strings.Replace(f.treeJSON, `"truncated":false`, `"truncated":true`, 1)
	if !tree.Truncated() {
		t.Error("Truncated() = false after truncated listing")
	}
	// Reads still work for paths that made it into the partial listing.
	if raw, ok, _ := tree.Read("AGENTS.md"); !ok || string(raw) != "agents instructions" {
		t.Fatalf("read over truncated listing: %q ok=%v", raw, ok)
	}
}

func TestAPITreeContextPropagation(t *testing.T) {
	tree, _ := newAPITreeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A cancelled context surfaces as an error, not a silent empty tree.
	if _, err := tree.WithContext(ctx).List(""); err == nil {
		t.Error("cancelled context accepted silently")
	}
}

func TestSortStringsIsSorted(t *testing.T) {
	in := []string{"b", "a", "c"}
	sortStrings(in)
	if !sort.StringsAreSorted(in) {
		t.Fatalf("sortStrings left %v", in)
	}
}
