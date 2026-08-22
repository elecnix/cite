// Comment and reaction reads for the human-signal ingestion path (§14
// "Read the signals people already emit"): @cite review command comments,
// review comments carrying Cite fingerprints, and the 👎 reactions on them.
//
// Everything here is read-only authenticated API metadata. Association and
// authorship come only from these responses — never from comment text (I2).

package githubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// IssueComment fetches one issue comment via GET
// /repos/{o}/{r}/issues/comments/{id}. num is accepted for call-site
// symmetry with the rest of the PR-scoped API but the endpoint is
// comment-addressed, so it is not sent. The returned association is
// GitHub's computed author_association — the only admissible input to an
// authorisation decision.
func (c *Client) IssueComment(ctx context.Context, num int, commentID int64) (body string, authorLogin string, authorAssociation string, err error) {
	_ = num
	var raw struct {
		Body string `json:"body"`
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
		AuthorAssociation string `json:"author_association"`
	}
	if err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("repos/%s/%s/issues/comments/%d", c.owner, c.repo, commentID),
		nil, nil, &raw); err != nil {
		return "", "", "", err
	}
	login := ""
	if raw.User != nil {
		login = raw.User.Login
	}
	return raw.Body, login, raw.AuthorAssociation, nil
}

// ReviewCommentFull is one pull request review comment with the fields the
// signal walk needs: its database ID (for reaction lookups), its body (for
// the cite:fingerprint marker) and who wrote it.
type ReviewCommentFull struct {
	ID                int64
	Body              string
	AuthorLogin       string
	AuthorAssociation string
}

// ReviewComments lists a pull request's review comments via paginated GET
// /repos/{o}/{r}/pulls/{n}/comments.
func (c *Client) ReviewComments(ctx context.Context, prNum int) ([]ReviewCommentFull, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d/comments", c.owner, c.repo, prNum))
	if err != nil {
		return nil, err
	}
	out := make([]ReviewCommentFull, 0, len(raws))
	for _, r := range raws {
		var cm struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User *struct {
				Login string `json:"login"`
			} `json:"user"`
			AuthorAssociation string `json:"author_association"`
		}
		if err := json.Unmarshal(r, &cm); err != nil {
			return nil, fmt.Errorf("decoding github response: %w", err)
		}
		rc := ReviewCommentFull{ID: cm.ID, Body: cm.Body, AuthorAssociation: cm.AuthorAssociation}
		if cm.User != nil {
			rc.AuthorLogin = cm.User.Login
		}
		out = append(out, rc)
	}
	return out, nil
}

// Reaction is one reaction on a comment. Content is GitHub's reaction key:
// "+1" (👍), "-1" (👎), "laugh", "hooray", "confused", "heart",
// "rocket", "eyes".
type Reaction struct {
	Content   string
	UserLogin string
}

// CommentReactionList returns the individual reactions on one comment via
// paginated GET /repos/{o}/{r}/issues/comments/{id}/reactions. This path
// serves both issue comments and review comments, so one method covers the
// whole signal surface. Individual entries are needed when identity matters
// (who dismissed what); use CommentReactions for counts only.
func (c *Client) CommentReactionList(ctx context.Context, commentID int64) ([]Reaction, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/issues/comments/%d/reactions", c.owner, c.repo, commentID))
	if err != nil {
		return nil, err
	}
	out := make([]Reaction, 0, len(raws))
	for _, r := range raws {
		var re struct {
			Content string `json:"content"`
			User    *struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(r, &re); err != nil {
			return nil, fmt.Errorf("decoding github response: %w", err)
		}
		reaction := Reaction{Content: re.Content}
		if re.User != nil {
			reaction.UserLogin = re.User.Login
		}
		out = append(out, reaction)
	}
	return out, nil
}

// CommentReactions returns aggregated reaction content → count for one
// comment, summing every content kind including "+1" and "-1".
func (c *Client) CommentReactions(ctx context.Context, commentID int64) (map[string]int, error) {
	reactions, err := c.CommentReactionList(ctx, commentID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(reactions))
	for _, r := range reactions {
		counts[r.Content]++
	}
	return counts, nil
}
