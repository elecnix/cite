package githubclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPRLabels(t *testing.T) {
	var path, query string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		query = r.URL.RawQuery
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"name":"zeta"}]`)
			return
		}
		next := "http://" + r.Host + r.URL.Path + "?per_page=100&page=2"
		w.Header().Set("Link", "<"+next+">; rel=\"next\"")
		w.Write([]byte(`[{"name":"cite-bypass"},{"name":"bug"}]`))
	}))
	labels, err := c.PRLabels(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repos/octo/hello/issues/7/labels" {
		t.Errorf("path = %q", path)
	}
	if !strings.Contains(query, "per_page=100") {
		t.Errorf("query = %q, want per_page=100 pagination", query)
	}
	got := strings.Join(labels, ",")
	want := "cite-bypass,bug,zeta"
	if got != want {
		t.Errorf("labels = %v, want %v", labels, want)
	}
}

func TestPRLabelsEmpty(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	labels, err := c.PRLabels(context.Background(), 7)
	if err != nil || len(labels) != 0 {
		t.Fatalf("labels = %v, err = %v", labels, err)
	}
}

func TestPendingCiteCheckFound(t *testing.T) {
	var path string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"total_count":2,"check_runs":[
			{"id":11,"name":"other","status":"completed"},
			{"id":42,"name":"cite","status":"in_progress"}
		]}`))
	}))
	id, found, err := c.PendingCiteCheck(context.Background(), "headsha9")
	if err != nil {
		t.Fatal(err)
	}
	if !found || id != 42 {
		t.Errorf("(id, found) = (%d, %t), want (42, true)", id, found)
	}
	if path != "/repos/octo/hello/commits/headsha9/check-runs" {
		// §11: lookup is by head SHA, never event metadata.
		t.Errorf("path = %q", path)
	}
}

func TestPendingCiteCheckIgnoresCompletedAndMissing(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"completed cite check", `{"check_runs":[{"id":5,"name":"cite","status":"completed"}]}`},
		{"no cite check", `{"check_runs":[{"id":6,"name":"other","status":"queued"}]}`},
		{"empty", `{"check_runs":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			id, found, err := c.PendingCiteCheck(context.Background(), "sha")
			if err != nil || found || id != 0 {
				t.Errorf("(id, found, err) = (%d, %t, %v), want (0, false, nil)", id, found, err)
			}
		})
	}
}
