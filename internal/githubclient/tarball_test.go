package githubclient

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.WriteHeader(&tar.Header{Name: "owner-repo-abc123/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestTarballStripsTheTopDirectoryAndFilters(t *testing.T) {
	archive := makeTarball(t, map[string]string{
		"a/b.go":    "package a\n",
		"README.md": "# hi\n",
		"big/c.go":  strings.Repeat("x", 100),
		"a/d.go":    "package a\n// second\n",
	})
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	c := New("tok", srv.URL, nil)
	files, truncated, err := c.Tarball(context.Background(), "owner", "repo", "abc123",
		func(p string) bool { return strings.HasSuffix(p, ".go") }, 50, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close() // orders the handler's writes before these reads
	if gotPath != "/repos/owner/repo/tarball/abc123" || gotAuth != "Bearer tok" {
		t.Fatalf("request: path=%q auth=%q", gotPath, gotAuth)
	}
	if truncated {
		t.Fatal("nothing hit the total cap")
	}
	if string(files["a/b.go"]) != "package a\n" || files["a/d.go"] == nil {
		t.Fatalf("missing kept files: %v", keys(files))
	}
	if _, ok := files["README.md"]; ok {
		t.Fatal("keep refused README.md")
	}
	if _, ok := files["big/c.go"]; ok {
		t.Fatal("a file over maxFile must be left out")
	}
	if _, ok := files["link"]; ok {
		t.Fatal("a symlink is not a regular file")
	}
}

func TestTarballReportsTruncationAtTheTotalCap(t *testing.T) {
	archive := makeTarball(t, map[string]string{"a.go": "0123456789", "b.go": "0123456789"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
	defer srv.Close()
	files, truncated, err := New("", srv.URL, nil).Tarball(context.Background(), "o", "r", "x",
		func(string) bool { return true }, 100, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(files) != 1 {
		t.Fatalf("want one file and truncated, got %d files truncated=%v", len(files), truncated)
	}
}

func TestTarballSurfacesAnAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	_, _, err := New("", srv.URL, nil).Tarball(context.Background(), "o", "r", "x", func(string) bool { return true }, 100, 100)
	if err == nil || !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("want the 404 surfaced, got %v", err)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
