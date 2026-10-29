package githubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The context comment is minimized through GraphQL, which takes the comment's
// node id. The database id the REST listing also carries is not a GraphQL id:
// the API answers "Could not resolve to a node with the global id" for it.
func TestFindIssueCommentWithNodeCarriesBothIDs(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"id":1,"node_id":"IC_one","body":"unrelated"},
			{"id":2,"node_id":"IC_two","body":"<!-- cite-context -->\nold"},
			{"id":3,"node_id":"IC_three","body":"<!-- cite-context -->\nnewer duplicate"},
			{"id":4,"node_id":"IC_four","body":"> <!-- cite-context -->\nquote-reply by a person"}
		]`)
	}))
	got, found, err := c.FindIssueCommentWithNode(context.Background(), 7, "<!-- cite-context -->")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.ID != 3 || got.NodeID != "IC_three" || !strings.Contains(got.Body, "newer duplicate") {
		t.Fatalf("the newest comment that opens with the marker wins, not a quote-reply, got %+v", got)
	}
	if _, found, _ := c.FindIssueCommentWithNode(context.Background(), 7, "<!-- absent -->"); found {
		t.Fatal("an absent marker must report not found")
	}
}

func TestCreateAndUpdateIssueComment(t *testing.T) {
	var calls []string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := readBody(r)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+raw)
		fmt.Fprint(w, `{"id":9,"node_id":"IC_nine"}`)
	}))
	if err := c.CreateIssueComment(context.Background(), 7, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateIssueComment(context.Background(), 9, "again"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /repos/octo/hello/issues/7/comments {"body":"hello"}`,
		`PATCH /repos/octo/hello/issues/comments/9 {"body":"again"}`,
	}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls = %q", calls)
	}
}

func TestMinimizeNodeSendsTheNodeID(t *testing.T) {
	var body string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = readBody(r)
		fmt.Fprint(w, `{"data":{"minimizeComment":{"minimizedComment":{"isMinimized":true}}}}`)
	}))
	if err := c.MinimizeNode(context.Background(), "IC_three"); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Query, "minimizeComment") || !strings.Contains(req.Query, "classifier:OUTDATED") {
		t.Errorf("query = %s", req.Query)
	}
	if req.Variables["subjectId"] != "IC_three" {
		t.Errorf("vars = %v", req.Variables)
	}
}
