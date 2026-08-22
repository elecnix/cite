package githubclient

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestListRecentlyMergedFiltersUnmergedAndOld covers the merge-window filter:
// closed-without-merging is skipped, merges older than the window are
// skipped, and in-window merges survive with their merge commit SHA.
func TestListRecentlyMergedFiltersUnmergedAndOld(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-2 * time.Hour).Format(time.RFC3339)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/hello/pulls" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("state"); got != "closed" {
			t.Errorf("state = %q", got)
		}
		if got := r.URL.Query().Get("sort"); got != "updated" {
			t.Errorf("sort = %q", got)
		}
		if got := r.URL.Query().Get("direction"); got != "desc" {
			t.Errorf("direction = %q", got)
		}
		if err := mustAuthHeaders(r); err != nil {
			t.Error(err)
		}
		// Two pages: the first carries an old merge so pagination must
		// continue; the second carries the recent one and no Link header.
		switch r.URL.Query().Get("page") {
		case "2":
			w.Write([]byte(`[
				{"number": 12, "merged_at": "` + recent + `", "merge_commit_sha": "cafe1234567890"},
				{"number": 13, "merged_at": null}
			]`))
		default:
			next := *r.URL
			q := next.Query()
			q.Set("page", "2")
			next.RawQuery = q.Encode()
			w.Header().Set("Link", "<"+next.String()+`>; rel="next"`)
			w.Write([]byte(`[
				{"number": 11, "merged_at": "` + old + `", "merge_commit_sha": "beef0000000000"}
			]`))
		}
	}))
	prs, err := c.ListRecentlyMerged(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d merged PRs (%v), want 1", len(prs), prs)
	}
	p := prs[0]
	if p.Number != 12 || p.MergeCommitSHA != "cafe1234567890" {
		t.Errorf("merged PR = %+v", p)
	}
	if _, err := time.Parse(time.RFC3339, recent); p.MergedAt.IsZero() || err != nil {
		t.Errorf("MergedAt not parsed: %v", p.MergedAt)
	}
}

// TestListRecentlyMergedNoNextStops verifies a page without a Link rel=next
// header ends pagination instead of looping forever.
func TestListRecentlyMergedNoNextStops(t *testing.T) {
	now := time.Now().UTC()
	requests := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write([]byte(`[{"number": 9, "merged_at": null}]`))
	}))
	prs, err := c.ListRecentlyMerged(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 (pagination must stop without Link)", requests)
	}
	if len(prs) != 0 {
		t.Errorf("unmerged PR leaked into results: %v", prs)
	}
}

func TestCreateIssue(t *testing.T) {
	var path, body string
	var sawAuth error
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = mustAuthHeaders(r)
		path = r.URL.Path
		body = readBody(r)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"number": 555}`)
	}))
	num, err := c.CreateIssue(context.Background(), "[cite] escaped finding", "body text")
	if err != nil {
		t.Fatal(err)
	}
	if num != 555 {
		t.Errorf("number = %d", num)
	}
	if path != "/repos/octo/hello/issues" {
		t.Errorf("path = %q", path)
	}
	m := decodeBody(t, body)
	if m["title"] != "[cite] escaped finding" || m["body"] != "body text" {
		t.Errorf("payload = %v", m)
	}
	if sawAuth != nil {
		t.Error(sawAuth)
	}
}
