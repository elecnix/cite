package githubclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient builds a Client against an httptest server.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New("tok", srv.URL, srv.Client()).WithRepo("octo", "hello")
}

func mustAuthHeaders(r *http.Request) error {
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		return fmt.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
		return fmt.Errorf("Accept = %q", got)
	}
	if got := r.Header.Get("X-GitHub-Api-Version"); got != apiVersion {
		return fmt.Errorf("X-GitHub-Api-Version = %q", got)
	}
	return nil
}

func TestGetPR(t *testing.T) {
	var sawHeaders error
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeaders = mustAuthHeaders(r)
		if r.URL.Path != "/repos/octo/hello/pulls/7" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("X-RateLimit-Remaining", "4990")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
		w.WriteHeader(200)
		fmt.Fprint(w, `{"number":7,"body":"fixes the bug",
			"head":{"sha":"abc123"},
			"base":{"ref":"main","sha":"def456"},
			"user":{"login":"octocat"},
			"author_association":"CONTRIBUTOR"}`)
	}))
	pr, err := c.GetPR(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if sawHeaders != nil {
		t.Error(sawHeaders)
	}
	if pr.Number != 7 || pr.HeadSHA != "abc123" || pr.BaseRef != "main" ||
		pr.BaseSHA != "def456" || pr.Body != "fixes the bug" ||
		pr.AuthorLogin != "octocat" || pr.AuthorAssociation != "CONTRIBUTOR" {
		t.Fatalf("got %+v", pr)
	}
	rl, ok := c.LastRateLimit()
	if !ok || rl.Remaining != 4990 || rl.Limit != 5000 || rl.Reset.Before(time.Now()) {
		t.Fatalf("LastRateLimit = %+v ok=%v", rl, ok)
	}
}

func TestListPRFilesPagination(t *testing.T) {
	page := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1:
			w.Header().Set("Link",
				fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, "http://"+r.Host+r.URL.Path+"?per_page=100&page=2", "http://"+r.Host+r.URL.Path))
			fmt.Fprint(w, `[{"filename":"a.go","status":"modified","additions":1,"deletions":2},
				{"filename":"b.go","status":"added","additions":10}]`)
		case 2:
			fmt.Fprint(w, `[{"filename":"old.go","status":"removed","additions":0,"deletions":5},
				{"filename":"n.go","status":"renamed","previous_filename":"o.go"}]`)
		default:
			t.Fatal("too many page requests")
		}
	}))
	files, err := c.ListPRFiles(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if page != 2 {
		t.Fatalf("pages fetched = %d", page)
	}
	if len(files) != 4 {
		t.Fatalf("got %d entries: %+v", len(files), files)
	}
	want := []struct{ status, path string }{
		{"M", "a.go"}, {"A", "b.go"}, {"D", "old.go"},
	}
	for i, w := range want {
		if files[i].Status != w.status || files[i].Path != w.path {
			t.Errorf("files[%d] = %s %s, want %s %s", i, files[i].Status, files[i].Path, w.status, w.path)
		}
	}
	if files[3].Status[0] != 'R' || files[3].OldPath != "o.go" {
		t.Errorf("rename not carried through: %+v", files[3])
	}
}

func TestRateLimitRetryOn403(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Second).Unix()))
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, `{"number":1,"head":{"sha":"x"},"base":{"ref":"m","sha":"y"}}`)
	}))
	pr, err := c.GetPR(context.Background(), 1)
	if err != nil {
		t.Fatalf("retry after rate limit failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if pr.HeadSHA != "x" {
		t.Fatalf("pr = %+v", pr)
	}
}

func TestPermission403NotRetried(t *testing.T) {
	var calls int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("X-RateLimit-Remaining", "400")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
	}))
	_, err := c.GetPR(context.Background(), 1)
	if err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("permission 403 retried: calls = %d", calls)
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 403 {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(apiErr.Message, "\n") || len(apiErr.Message) > maxErrorMessage {
		t.Errorf("message not sanitised: %q", apiErr.Message)
	}
}

func TestAPIErrorSanitisesLongMessage(t *testing.T) {
	long := strings.Repeat("boom\t", 300)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprintf(w, `{"message":%q}`, long+"\nsecond line\nwith\theaders")
	}))
	_, _, err := c.GetFileContent(context.Background(), "main", "x.txt")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want APIError, got %#v", err)
	}
	msg := apiErr.Message
	if len(msg) > maxErrorMessage {
		t.Errorf("message too long: %d", len(msg))
	}
	if strings.ContainsAny(msg, "\n\r\t") {
		t.Errorf("message carries whitespace: %q", msg)
	}
	if apiErr.StatusCode != 500 {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestGetFileContentBase64And404(t *testing.T) {
	content := "hello cite\nline two\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	// Re-wrap with newlines like GitHub does every ~60 chars.
	wrapped := ""
	for i := 0; i < len(b64); i += 30 {
		wrapped += b64[i:min(i+30, len(b64))] + "\n"
	}
	notFound := false
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/hello/contents/docs/guide.md" && r.URL.Query().Get("ref") == "main" {
			fmt.Fprintf(w, `{"sha":"s","content":%q,"encoding":"base64","size":%d}`,
				wrapped, len(content))
			return
		}
		notFound = true
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"No commit found for the ref main"}`)
	}))
	got, ok, err := c.GetFileContent(context.Background(), "main", "docs/guide.md")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if string(got) != content {
		t.Fatalf("decoded = %q, want %q", got, content)
	}
	got, ok, err = c.GetFileContent(context.Background(), "main", "missing.txt")
	if err != nil || ok || got != nil {
		t.Fatalf("404 should be (nil,false,nil), got (%v,%v,%v)", got, ok, err)
	}
	if !notFound {
		t.Error("404 path never exercised")
	}
}

func TestGetMergeBase(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/hello/compare/main...feature" {
			t.Errorf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"merge_base_commit":{"sha":"mb0099"}}`)
	}))
	mb, err := c.GetMergeBase(context.Background(), "main", "feature")
	if err != nil || mb != "mb0099" {
		t.Fatalf("merge base = %q err=%v", mb, err)
	}
}

func min(a, b int) int {
	if b < a {
		return b
	}
	return a
}
