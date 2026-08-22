package githubclient

// Pull-request enumeration for the scheduled re-review of bypassed merges
// (§11: the bypass buys time, not amnesty), plus issue creation for findings
// that escape.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// MergedPR is one merged pull request with the fields the re-reviewer needs.
type MergedPR struct {
	Number         int
	MergeCommitSHA string
	MergedAt       time.Time
}

// ListRecentlyMerged returns closed pull requests merged within the window
// ending now, newest first. Pagination follows rel="next" like every other
// list endpoint.
func (c *Client) ListRecentlyMerged(ctx context.Context, since time.Time) ([]MergedPR, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=50", url.PathEscape(c.owner), url.PathEscape(c.repo)))
	if err != nil {
		return nil, err
	}
	var out []MergedPR
	for _, r := range raws {
		var p struct {
			Number      int     `json:"number"`
			MergedAt    *string `json:"merged_at"`
			MergeCommitSHA string `json:"merge_commit_sha"`
		}
		if err := json.Unmarshal(r, &p); err != nil {
			return nil, err
		}
		if p.MergedAt == nil {
			continue // closed without merging
		}
		ts, err := time.Parse(time.RFC3339, *p.MergedAt)
		if err != nil {
			continue
		}
		if ts.Before(since) {
			continue
		}
		out = append(out, MergedPR{Number: p.Number, MergeCommitSHA: p.MergeCommitSHA, MergedAt: ts})
	}
	return out, nil
}

// CreateIssue opens an issue and returns its number. Used by the bypassed-
// merge re-reviewer to file one issue per escaped finding.
func (c *Client) CreateIssue(ctx context.Context, title, body string) (int, error) {
	var out struct {
		Number int `json:"number"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/%s/issues", c.owner, c.repo),
		nil, map[string]string{"title": title, "body": body}, &out)
	if err != nil {
		return 0, err
	}
	return out.Number, nil
}
