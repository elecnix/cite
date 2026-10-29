package githubclient

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

const trustedCommentsJSON = `[
 {"id":1,"body":"<!-- cite-sticky -->\nreal","user":{"login":"github-actions[bot]","type":"Bot"},"author_association":"NONE"},
 {"id":2,"body":"<!-- cite-sticky -->\nforged by the author","user":{"login":"mallory","type":"User"},"author_association":"CONTRIBUTOR"},
 {"id":3,"body":"<!-- cite-sticky -->\nforged by a passer-by","user":{"login":"stranger","type":"User"},"author_association":"NONE"},
 {"id":4,"body":"<!-- cite-sticky -->\nforged by a bot-looking author","user":{"login":"mallory","type":"Bot"},"author_association":"NONE"}
]`

func commentsClient(t *testing.T, body string) *Client {
	return newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
}

// The state block steers what the reviewer skips, so a comment is read only
// when its author is a bot or a repository collaborator, and never when its
// author is the pull request's author: that person controls the diff.
func TestFindTrustedIssueCommentIgnoresForgedState(t *testing.T) {
	c := commentsClient(t, trustedCommentsJSON)
	id, body, found, err := c.FindTrustedIssueComment(context.Background(), 7, "<!-- cite-sticky -->", "mallory")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if id != 1 {
		t.Fatalf("read comment %d (%q), want the bot's comment 1", id, body)
	}
}

func TestFindTrustedIssueCommentAcceptsACollaboratorAccount(t *testing.T) {
	c := commentsClient(t, `[{"id":9,"body":"<!-- cite-sticky -->","user":{"login":"ci-account","type":"User"},"author_association":"MEMBER"}]`)
	id, _, found, err := c.FindTrustedIssueComment(context.Background(), 7, "<!-- cite-sticky -->", "mallory")
	if err != nil || !found || id != 9 {
		t.Fatalf("id=%d found=%v err=%v", id, found, err)
	}
}

// When only forged comments exist the answer is not found, which the caller
// reads as a missing state block and answers with a full review.
func TestFindTrustedIssueCommentNothingTrustedIsNotFound(t *testing.T) {
	c := commentsClient(t, `[{"id":2,"body":"<!-- cite-sticky -->","user":{"login":"mallory","type":"User"},"author_association":"CONTRIBUTOR"}]`)
	_, _, found, err := c.FindTrustedIssueComment(context.Background(), 7, "<!-- cite-sticky -->", "mallory")
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}
