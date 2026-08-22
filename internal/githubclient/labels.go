package githubclient

// Labels and pending-check lookup for the break-glass bypass (§11).
//
// Trust invariant (§12): the label's presence is verified against the
// authenticated API — never derived from event payload text or flag
// arguments, which are attacker-writable. PRLabels is the only source of
// truth for "is the bypass label actually on this pull request".

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// PRLabels returns the label names on a pull request via GET
// /repos/{o}/{r}/issues/{n}/labels, following pagination (per_page=100).
// Pull request labels live on the issue endpoint; that is not a mistake.
func (c *Client) PRLabels(ctx context.Context, num int) ([]string, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/issues/%d/labels", c.owner, c.repo, num))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range raws {
		var l struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r, &l); err != nil {
			return nil, fmt.Errorf("decoding github response: %w", err)
		}
		out = append(out, l.Name)
	}
	return out, nil
}

// PendingCiteCheck returns the id of the Cite check run on headSHA whose
// status is not yet completed, found=true when one exists. Like
// HasTerminalCiteCheck it looks up by head SHA only — never by event
// metadata (§11) — so the caller concludes exactly the check that owns the
// required-status slot.
func (c *Client) PendingCiteCheck(ctx context.Context, headSHA string) (checkID int64, found bool, err error) {
	var out struct {
		CheckRuns []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"check_runs"`
	}
	err = c.do(ctx, http.MethodGet,
		fmt.Sprintf("repos/%s/%s/commits/%s/check-runs", c.owner, c.repo, url.PathEscape(headSHA)),
		url.Values{"per_page": []string{"100"}}, nil, &out)
	if err != nil {
		return 0, false, err
	}
	for _, cr := range out.CheckRuns {
		if cr.Name == "cite" && cr.Status != "completed" {
			return cr.ID, true, nil
		}
	}
	return 0, false, nil
}
