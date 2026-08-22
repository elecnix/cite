package githubclient

// httptest fakes for the signal-ingestion reads (§14): issue comments,
// review comments, and reactions on either kind.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestIssueComment(t *testing.T) {
	var path string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := mustAuthHeaders(r); got != nil {
			t.Errorf("headers: %v", got)
		}
		path = r.URL.Path
		w.WriteHeader(200)
		fmt.Fprint(w, `{
			"id": 991,
			"body": "  @cite review\n",
			"user": {"login": "octocat"},
			"author_association": "MEMBER"
		}`)
	}))
	body, login, assoc, err := c.IssueComment(context.Background(), 7, 991)
	if err != nil {
		t.Fatal(err)
	}
	// The endpoint is comment-addressed; num is not sent.
	if path != "/repos/octo/hello/issues/comments/991" {
		t.Fatalf("path = %q", path)
	}
	if body != "  @cite review\n" {
		t.Errorf("body = %q", body)
	}
	if login != "octocat" || assoc != "MEMBER" {
		t.Errorf("login = %q assoc = %q", login, assoc)
	}
}

func TestReviewCommentsPaginated(t *testing.T) {
	pages := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		switch pages {
		case 1:
			w.Header().Set("Link",
				fmt.Sprintf(`<http://%s/repos/octo/hello/pulls/7/comments?page=2&per_page=100>; rel="next"`,
					r.Host))
			fmt.Fprint(w, `[{"id":11,"body":"<!-- cite:fingerprint=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -->",
				"user":{"login":"cite-bot"},"author_association":"NONE"}]`)
		case 2:
			fmt.Fprint(w, `[{"id":22,"body":"a human reply","user":{"login":"octocat"},"author_association":"COLLABORATOR"}]`)
		default:
			t.Errorf("unexpected page request %d", pages)
			w.WriteHeader(500)
		}
	}))
	got, err := c.ReviewComments(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want 2", pages)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != 11 || got[0].AuthorLogin != "cite-bot" || got[0].AuthorAssociation != "NONE" {
		t.Errorf("comment[0] = %+v", got[0])
	}
	if got[1].ID != 22 || got[1].Body != "a human reply" || got[1].AuthorAssociation != "COLLABORATOR" {
		t.Errorf("comment[1] = %+v", got[1])
	}
}

func TestCommentReactionsAggregated(t *testing.T) {
	var path string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(200)
		// The issues/comments/{id}/reactions path serves review comments
		// too — one endpoint for both kinds.
		fmt.Fprint(w, `[
			{"content":"+1","user":{"login":"a"}},
			{"content":"+1","user":{"login":"b"}},
			{"content":"-1","user":{"login":"c"}},
			{"content":"heart","user":{"login":"d"}}
		]`)
	}))
	counts, err := c.CommentReactions(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repos/octo/hello/issues/comments/55/reactions" {
		t.Fatalf("path = %q", path)
	}
	if counts["+1"] != 2 || counts["-1"] != 1 || counts["heart"] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestCommentReactionListKeepsIdentity(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `[{"content":"-1","user":{"login":"downvoter"}}]`)
	}))
	reactions, err := c.CommentReactionList(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 1 || reactions[0].Content != "-1" || reactions[0].UserLogin != "downvoter" {
		t.Errorf("reactions = %+v", reactions)
	}
}

func TestIssueCommentNotFoundIsTypedError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	_, _, _, err := c.IssueComment(context.Background(), 7, 12345)
	if err == nil {
		t.Fatal("want error on missing comment")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 404 {
		t.Fatalf("err = %v, want *APIError 404", err)
	}
}
