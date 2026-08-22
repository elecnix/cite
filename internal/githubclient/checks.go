package githubclient

// Check runs (§11), review publishing (§10), review threads and the sticky
// ledger comment. Everything here enforces its invariant at this layer, not
// at call sites: check runs target whatever SHA the caller passes (the PR
// head, never github.sha), reviews are COMMENT-only, and 422 anchor
// failures demote comments into the body instead of dropping them.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Check run states (§11). The gate's first action creates the check as
// queued on the pull request head SHA — never github.sha — so a required
// check is never absent, only pending.
const (
	CheckStatusQueued     = "queued"
	CheckStatusInProgress = "in_progress"
)

// CreateCheckRun creates a check run on headSHA with the given status
// ("queued" or "in_progress") and returns its id. Callers pass the PR head
// SHA read from GetPR; this function never substitutes another SHA.
func (c *Client) CreateCheckRun(ctx context.Context, headSHA, name, title, summary, status string) (int64, error) {
	payload := map[string]any{
		"name":     name,
		"head_sha": headSHA,
		"status":   status,
	}
	if title != "" || summary != "" {
		payload["output"] = map[string]string{"title": title, "summary": summary}
	}
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/%s/check-runs", c.owner, c.repo), nil, payload, &out)
	if err != nil {
		return 0, err
	}
	return out.ID, nil
}

// ConcludeCheckRun completes a check run with a terminal conclusion
// ("success", "failure", "neutral") and final output.
func (c *Client) ConcludeCheckRun(ctx context.Context, id int64, conclusion, title, summary string) error {
	payload := map[string]any{
		"status":     "completed",
		"conclusion": conclusion,
	}
	if title != "" || summary != "" {
		payload["output"] = map[string]string{"title": title, "summary": summary}
	}
	err := c.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/%s/check-runs/%d", c.owner, c.repo, id), nil, payload, nil)
	return err
}

// ReviewComment is one inline review anchor. Side is "RIGHT" for new-code
// lines (or "LEFT" for deleted ones); Line must sit inside the diff or
// GitHub answers 422, which CreateReview handles by bisection.
type ReviewComment struct {
	Path      string
	StartLine int // 0 means no multi-line range
	Line      int
	Side      string
	Body      string
}

// reviewEvent is hard-coded: COMMENT, never REQUEST_CHANGES (§10). A bot
// that requests changes can leave a pull request permanently unmergeable;
// blocking lives in the check run or nowhere. Not configurable on purpose.
const reviewEvent = "COMMENT"

// demotedHeading labels comments GitHub refused to anchor; they are moved
// into the body rather than dropped (§10: never silently dropped).
const demotedHeading = "Not anchored to a diff line"

// CreateReview publishes one atomic review with event COMMENT. On HTTP 422
// (one unanchorable line fails the whole request) it bisects the comment
// list, demoting offenders into a labelled body section, and republishes
// until something lands. It returns an error only if even a body-only
// review is rejected.
func (c *Client) CreateReview(ctx context.Context, prNum int, body string, comments []ReviewComment) error {
	return c.publishReview(ctx, prNum, body, comments, nil)
}

// publishReview carries the accumulated demoted lines through recursion.
func (c *Client) publishReview(ctx context.Context, prNum int, body string, comments []ReviewComment, demoted []string) error {
	fullBody := renderDemoted(body, demoted)
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", c.owner, c.repo, prNum)

	if len(comments) > 0 {
		payload := map[string]any{
			"body":  fullBody,
			"event": reviewEvent,
			"comments": mapSlice(comments, func(cm ReviewComment) map[string]any {
				m := map[string]any{
					"path": cm.Path,
					"line": cm.Line,
					"side": sideOr(cm.Side),
					"body": cm.Body,
				}
				if cm.StartLine > 0 {
					m["start_line"] = cm.StartLine
					m["start_side"] = sideOr(cm.Side)
				}
				return m
			}),
		}
		resp, err := c.attempt(ctx, http.MethodPost, endpoint, nil, payload)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusUnprocessableEntity {
			return finish(resp, nil)
		}
		// Drain the 422 body so the connection is reusable, then bisect:
		// keep the second half anchored, demote the first half into the
		// body, retry. One offending comment poisons the whole request, so
		// halving isolates it in log(len) rounds without dropping it.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		mid := len(comments) / 2
		if mid == 0 {
			// A single comment was rejected: demote it and fall through to
			// the body-only review rather than retrying the same request.
			return c.publishReview(ctx, prNum, body, nil,
				append(demoted, describeComments(comments)...))
		}
		demoted := append(demoted, describeComments(comments[:mid])...)
		return c.publishReview(ctx, prNum, body, comments[mid:], demoted)
	}

	// Body-only review: the last resort. If even this fails there is
	// nothing to publish and the caller must see the error.
	payload := map[string]any{"body": fullBody, "event": reviewEvent}
	resp, err := c.attempt(ctx, http.MethodPost, endpoint, nil, payload)
	if err != nil {
		return err
	}
	return finish(resp, nil)
}

func sideOr(s string) string {
	if s == "" {
		return "RIGHT"
	}
	return s
}

func mapSlice[T any, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// describeComments renders demoted comments as body text lines.
func describeComments(comments []ReviewComment) []string {
	lines := make([]string, 0, len(comments))
	for _, cm := range comments {
		var line string
		switch {
		case cm.StartLine > 0 && cm.StartLine != cm.Line:
			line = fmt.Sprintf("- `%s:%d-%d`", cm.Path, cm.StartLine, cm.Line)
		case cm.Line > 0:
			line = fmt.Sprintf("- `%s:%d`", cm.Path, cm.Line)
		default:
			line = fmt.Sprintf("- `%s`", cm.Path)
		}
		if cm.Body != "" {
			line += " — " + strings.ReplaceAll(cm.Body, "\n", " ")
		}
		lines = append(lines, line)
	}
	return lines
}

// renderDemoted appends the labelled section to the body when anything was
// demoted.
func renderDemoted(body string, demoted []string) string {
	if len(demoted) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n### " + demotedHeading + "\n\n")
	for _, d := range demoted {
		b.WriteString(d + "\n")
	}
	return b.String()
}

// Thread is one review thread from GraphQL. IsOutdated marks threads whose
// diff hunk moved; IsResolved whether an actor resolved them.
type Thread struct {
	ID         string
	IsOutdated bool
	IsResolved bool
	Comments   []ThreadComment
}

// ThreadComment is one comment inside a thread. Line/StartLine are 0 when
// GitHub reports null (a file-level or unanchored comment).
type ThreadComment struct {
	ID          int64 // databaseId, the minimizeComment subject ID
	Body        string
	Path        string
	Line        int
	StartLine   int
	AuthorLogin string
}

const reviewThreadsQuery = `query($owner:String!,$repo:String!,$number:Int!){
repository(owner:$owner,name:$repo){
pullRequest(number:$number){
reviewThreads(first:100){
nodes{
id
isOutdated
isResolved
comments(first:10){
nodes{
id
databaseId
body
path
line
startLine
author{ login }
}
}
}
}
}
}
}`

// ListReviewThreads fetches all review threads of a pull request via
// GraphQL. first:100 covers every thread Cite itself created.
func (c *Client) ListReviewThreads(ctx context.Context, prNum int) ([]Thread, error) {
	var result struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						IsOutdated bool   `json:"isOutdated"`
						IsResolved bool   `json:"isResolved"`
						Comments   struct {
							Nodes []struct {
								DatabaseID int64  `json:"databaseId"`
								Body       string `json:"body"`
								Path       string `json:"path"`
								Line       *int   `json:"line"`
								StartLine  *int   `json:"startLine"`
								Author     *struct {
									Login string `json:"login"`
								} `json:"author"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": c.owner, "repo": c.repo, "number": prNum}
	if err := c.graphql(ctx, reviewThreadsQuery, vars, &result); err != nil {
		return nil, err
	}
	nodes := result.Repository.PullRequest.ReviewThreads.Nodes
	threads := make([]Thread, 0, len(nodes))
	for _, n := range nodes {
		t := Thread{ID: n.ID, IsOutdated: n.IsOutdated, IsResolved: n.IsResolved}
		for _, cn := range n.Comments.Nodes {
			tc := ThreadComment{
				ID:        cn.DatabaseID,
				Body:      cn.Body,
				Path:      cn.Path,
				Line:      deref(cn.Line),
				StartLine: deref(cn.StartLine),
			}
			if cn.Author != nil {
				tc.AuthorLogin = cn.Author.Login
			}
			t.Comments = append(t.Comments, tc)
		}
		threads = append(threads, t)
	}
	return threads, nil
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

const resolveThreadMutation = `mutation($threadId:ID!){
resolveReviewThread(input:{threadId:$threadId}){
thread{ id isResolved }
}
}`

// ResolveReviewThread resolves one review thread via GraphQL. Individually
// idempotent, per §10's publish ordering.
func (c *Client) ResolveReviewThread(ctx context.Context, threadID string) error {
	vars := map[string]any{"threadId": threadID}
	var out map[string]any
	return c.graphql(ctx, resolveThreadMutation, vars, &out)
}

const minimizeCommentMutation = `mutation($subjectId:ID!){
minimizeComment(input:{subjectId:$subjectId,classifier:OUTDATED}){
minimizedComment{ isMinimized }
}
}`

// MinimizeComment minimizes a comment by database ID with classifier
// OUTDATED — the only classifier Cite uses, because stale findings are
// outdated by definition, not abuse or spam.
func (c *Client) MinimizeComment(ctx context.Context, subjectID int64) error {
	vars := map[string]any{"subjectId": fmt.Sprintf("%d", subjectID)}
	var out map[string]any
	return c.graphql(ctx, minimizeCommentMutation, vars, &out)
}

// graphql posts one query/mutation to the GraphQL endpoint and decodes the
// response, mapping the top-level errors array to APIError.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	payload := map[string]any{"query": query}
	if vars != nil {
		payload["variables"] = vars
	}
	resp, err := c.attempt(ctx, http.MethodPost, "graphql", nil, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("reading github response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return graphqlAPIError(resp.StatusCode, raw)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decoding github response: %w", err)
	}
	if len(env.Errors) > 0 && len(env.Data) == 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, sanitizeMessage(e.Message))
		}
		return &APIError{StatusCode: resp.StatusCode, Message: "graphql: " + strings.Join(msgs, "; ")}
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func graphqlAPIError(status int, raw []byte) error {
	var eb struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &eb)
	msg := eb.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{StatusCode: status, Message: sanitizeMessage(msg)}
}

// UpsertIssueComment finds the existing issue comment containing marker on
// the pull request and PATCHes it, or POSTs a new one when no match
// exists. The sticky comment is where the ledger blob lives inside an HTML
// comment (§10); marker must be unique enough that only Cite's own comment
// matches.
func (c *Client) UpsertIssueComment(ctx context.Context, prNum int, marker, body string) error {
	id, found, err := c.findStickyComment(ctx, prNum, marker)
	if err != nil {
		return err
	}
	payload := map[string]string{"body": body}
	if found {
		return c.do(ctx, http.MethodPatch,
			fmt.Sprintf("repos/%s/%s/issues/comments/%d", c.owner, c.repo, id), nil, payload, nil)
	}
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("repos/%s/%s/issues/%d/comments", c.owner, c.repo, prNum), nil, payload, nil)
}

// FindIssueComment returns the id and body of the issue comment containing
// marker, or found=false. The CLI uses it to read the ledger blob back from
// the sticky comment before reconciling.
func (c *Client) FindIssueComment(ctx context.Context, prNum int, marker string) (id int64, body string, found bool, err error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/issues/%d/comments", c.owner, c.repo, prNum))
	if err != nil {
		return 0, "", false, err
	}
	for i := len(raws) - 1; i >= 0; i-- {
		var cm struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal(raws[i], &cm); err != nil {
			return 0, "", false, fmt.Errorf("decoding github response: %w", err)
		}
		if marker != "" && strings.Contains(cm.Body, marker) {
			return cm.ID, cm.Body, true, nil
		}
	}
	return 0, "", false, nil
}

// findStickyComment scans the issue comments for one whose body contains
// marker, following pagination; later pages win so the newest duplicate is
// updated.
func (c *Client) findStickyComment(ctx context.Context, prNum int, marker string) (int64, bool, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/issues/%d/comments", c.owner, c.repo, prNum))
	if err != nil {
		return 0, false, err
	}
	for i := len(raws) - 1; i >= 0; i-- {
		var cm struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User *struct {
				Login string    `json:"login"`
				Type  string    `json:"type"`
				Bot   *struct{} `json:"bot"`
			} `json:"user"`
		}
		if err := json.Unmarshal(raws[i], &cm); err != nil {
			return 0, false, fmt.Errorf("decoding github response: %w", err)
		}
		if marker != "" && strings.Contains(cm.Body, marker) {
			return cm.ID, true, nil
		}
	}
	return 0, false, nil
}
