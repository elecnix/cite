package main

// Tests for `cite signals`' mechanical thread-resolution disambiguation
// (§14): a resolved Cite thread whose quoted span changed in a later push is
// accepted-and-fixed; one whose span is still identical after the one
// documented normaliser is dismissed. No evidence data ⇒ unverifiable ⇒
// neither. The decision primitive is metrics.SpanChanged; here it is proven
// against the exact comment-body and contents-API shapes the walk sees.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/metrics"
)

const (
	fpChanged   = "11111111111111111111111111111111"
	fpIdentical = "22222222222222222222222222222222"
)

func citeBody(fp string, path string, quote string) string {
	ev := ""
	if quote != "" {
		d, _ := json.Marshal(map[string]any{
			"path":     path,
			"category": "crash",
			"title":    "t",
			"evidence": []map[string]any{{"line": 2, "quote": quote}},
		})
		ev = "<!-- cite:evidence=" + string(d) + " -->"
	}
	return "Cite finding cite:fingerprint=" + fp + " " + ev
}

// fakeGitHubForSignals serves just the endpoints the signals walk touches,
// so the wiring (GetPR head SHA → thread list → per-path content fetch) is
// exercised against the real client.
func fakeGitHubForSignals(t *testing.T, fileAtHead map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"number": 1, "head": map[string]any{"sha": "headsha"}})
	})
	mux.HandleFunc("GET /repos/o/r/pulls/1/comments", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("GET /repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					"pullRequest": map[string]any{
						"reviewThreads": map[string]any{
							"nodes": []any{
								map[string]any{
									"id":         "T1",
									"isResolved": true,
									"comments": map[string]any{"nodes": []any{
										map[string]any{"databaseId": 1, "body": citeBody(fpChanged, "a.go", "old line"), "path": "a.go"},
									}},
								},
								map[string]any{
									"id":         "T2",
									"isResolved": true,
									"comments": map[string]any{"nodes": []any{
										map[string]any{"databaseId": 2, "body": citeBody(fpIdentical, "b.go", "stable line"), "path": "b.go"},
									}},
								},
							},
						},
					},
				},
			},
		})
	})
	for path, content := range fileAtHead {
		body := content
		mux.HandleFunc("GET /repos/o/r/contents/"+path, func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"content":  base64.StdEncoding.EncodeToString([]byte(body)),
				"encoding": "base64",
			})
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSignalsResolutionDisambiguation(t *testing.T) {
	srv := fakeGitHubForSignals(t, map[string]string{
		"a.go": "new line",     // span changed → accepted-and-fixed
		"b.go": "stable\tline", // whitespace-only drift → identical → dismissed
	})
	c := githubclient.New("tok", srv.URL, srv.Client()).WithRepo("o", "r")
	ctx := context.Background()

	pr, err := c.GetPR(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pr.HeadSHA != "headsha" {
		t.Fatalf("head sha = %q", pr.HeadSHA)
	}

	_, tfA, okA := parseCiteCommentBody(citeBody(fpChanged, "a.go", "old line"))
	_, tfB, okB := parseCiteCommentBody(citeBody(fpIdentical, "b.go", "stable line"))
	if !okA || !okB || tfA == nil || tfB == nil ||
		len(tfA.Evidence) == 0 || len(tfB.Evidence) == 0 {
		t.Fatalf("cite body parsing failed: %v %v %+v %+v", okA, okB, tfA, tfB)
	}
	if tfA.Path != "a.go" || tfB.Path != "b.go" {
		t.Fatalf("evidence paths = %q %q", tfA.Path, tfB.Path)
	}

	contentA, found, err := c.GetFileContent(ctx, pr.HeadSHA, tfA.Path)
	if err != nil || !found {
		t.Fatalf("a.go fetch: found=%v err=%v", found, err)
	}
	contentB, foundB, _ := c.GetFileContent(ctx, pr.HeadSHA, tfB.Path)
	if !foundB {
		t.Fatal("b.go missing from fake")
	}

	if !metrics.SpanChanged(tfA.Evidence, contentA) {
		t.Fatal("rewritten span must read as changed → accepted-and-fixed")
	}
	if metrics.SpanChanged(tfB.Evidence, contentB) {
		t.Fatal("whitespace-only drift must not read as changed → dismissed")
	}

	// No evidence data ⇒ unverifiable ⇒ record neither disposition.
	if metrics.SpanChanged(nil, contentA) {
		t.Fatal("no evidence must never claim change")
	}
}
