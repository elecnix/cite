// Package githubclient is Cite's stdlib-only GitHub REST and GraphQL
// client. It exists so the publisher (§10) and the gate (§11) never need
// go-github: every request is built by hand, every response parsed into a
// local struct, and every provider failure mapped to a typed error before
// it can reach a rendered surface (I4 — never verbatim provider text).
//
// Two invariants are enforced here rather than at call sites:
//
//   - Check runs always target the pull request head SHA passed by the
//     caller, never github.sha (§11: on a pull_request event github.sha is
//     the synthetic merge commit and satisfies nothing).
//   - Reviews are published with event COMMENT, never REQUEST_CHANGES
//     (§10: blocking lives in the check run or nowhere). The value is
//     hard-coded; there is no parameter to get it wrong.
//
// Rate limiting is honoured cooperatively: every response updates the last
// observed X-RateLimit state, and a 403 that is a primary rate limit sleeps
// until the reset and retries once.
package githubclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elecnix/cite/internal/scope"
)

const defaultBaseURL = "https://api.github.com/"

// apiVersion is the GitHub REST API version requested on every call.
const apiVersion = "2022-11-28"

// maxErrorMessage caps sanitised provider text (I4), mirroring
// internal/model's typedError truncation.
const maxErrorMessage = 200

// RateLimit is the last observed rate-limit state from response headers.
type RateLimit struct {
	Remaining int       // X-RateLimit-Remaining
	Limit     int       // X-RateLimit-Limit
	Reset     time.Time // X-RateLimit-Reset, as UTC time
}

// APIError is the typed error for every non-2xx GitHub response. Message is
// sanitised (whitespace collapsed) and truncated — it is never verbatim
// long provider text, which could carry echoed headers (I4).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github api: HTTP %d: %s", e.StatusCode, e.Message)
}

// sanitizeMessage collapses whitespace and truncates so no raw provider
// blob (or header echo) survives rendering.
func sanitizeMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxErrorMessage {
		msg = msg[:maxErrorMessage]
	}
	return msg
}

// Client talks to one GitHub installation. It is safe for concurrent use.
// The zero value is not usable; construct with New.
type Client struct {
	token   string
	baseURL string // always ends in "/"
	http    *http.Client

	owner string
	repo  string

	mu         sync.Mutex
	lastRate   RateLimit
	lastRateOK bool
}

// New builds a Client. baseURL defaults to https://api.github.com/ when
// empty and may point at a test server. hc may be nil (http.DefaultClient
// is then used); its Timeout is deliberately ignored in favour of ctx
// deadlines set by callers.
func New(token, baseURL string, hc *http.Client) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{token: token, baseURL: baseURL, http: hc}
}

// WithRepo returns a copy of the client scoped to owner/repo. The original
// client is untouched, so one authenticated client can serve several repos.
func (c *Client) WithRepo(owner, repo string) *Client {
	cp := New(c.token, c.baseURL, c.http)
	cp.owner, cp.repo = owner, repo
	// Rate-limit state is per-HTTP identity; share it so a copy observes
	// what the parent has already seen.
	c.mu.Lock()
	cp.lastRate, cp.lastRateOK = c.lastRate, c.lastRateOK
	c.mu.Unlock()
	return cp
}

// LastRateLimit reports the most recent rate-limit headers seen on any
// response. ok is false before the first response.
func (c *Client) LastRateLimit() (rl RateLimit, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRate, c.lastRateOK
}

func (c *Client) observeRateLimit(h http.Header) {
	rem, _ := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	lim, _ := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	resetUnix, _ := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastRate = RateLimit{Remaining: rem, Limit: lim, Reset: time.Unix(resetUnix, 0).UTC()}
	c.lastRateOK = true
}

// do performs one JSON request against the REST API with retry-once on a
// primary rate-limit 403. out may be nil for responses whose body is not
// needed.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	resp, err := c.attempt(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	// A 403 carrying an exhausted primary rate limit is transient: sleep to
	// the reset window once and retry. Secondary limits and permission 403s
	// fail immediately.
	if resp != nil && resp.StatusCode == http.StatusForbidden && c.rateLimited() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if wait := c.retryAfter(); wait > 0 {
			select {
			case <-ctx.Done():
				return &APIError{StatusCode: resp.StatusCode, Message: "rate limited: " + ctx.Err().Error()}
			case <-time.After(wait):
			}
			resp, err = c.attempt(ctx, method, path, query, body)
			if err != nil {
				return err
			}
		} else {
			resp, err = c.attempt(ctx, method, path, query, body)
			if err != nil {
				return err
			}
		}
	}
	return finish(resp, out)
}

// attempt issues one request and returns the response for status handling.
// A nil response with a nil error is impossible; transport errors return an
// error directly.
func (c *Client) attempt(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := c.baseURL + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api unreachable: %w", err)
	}
	c.observeRateLimit(resp.Header)
	return resp, nil
}

func (c *Client) rateLimited() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRateOK && c.lastRate.Remaining == 0
}

// retryAfter returns how long to sleep before retrying after a rate limit,
// capped so a bad clock cannot hang the run; 0 means do not bother.
func (c *Client) retryAfter() time.Duration {
	c.mu.Lock()
	reset := c.lastRate.Reset
	c.mu.Unlock()
	d := time.Until(reset) + time.Second
	if d <= 0 || d > 2*time.Minute { // reset too far away: fail now, don't stall CI
		return 0
	}
	return d
}

func finish(resp *http.Response, out any) error {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("reading github response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var eb struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &eb)
		msg := eb.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &APIError{StatusCode: resp.StatusCode, Message: sanitizeMessage(msg)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decoding github response: %w", err)
		}
	}
	return nil
}

// PR is the slice of GET /repos/{o}/{r}/pulls/{n} that Cite needs.
// HeadSHA is what check runs must target (§11) and BaseSHA anchors the
// diff; AuthorAssociation feeds adjudicator identity checks (§12, I2) — it
// comes only from this authenticated metadata, never from comment text.
type PR struct {
	Number            int
	HeadSHA           string
	BaseRef           string
	BaseSHA           string
	Body              string
	AuthorLogin       string
	AuthorAssociation string
}

// GetPR fetches one pull request via GET /repos/{o}/{r}/pulls/{n}.
func (c *Client) GetPR(ctx context.Context, num int) (*PR, error) {
	var raw struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		AuthorAssociation string `json:"author_association"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/%s/pulls/%d", c.owner, c.repo, num), nil, nil, &raw); err != nil {
		return nil, err
	}
	return &PR{
		Number:            raw.Number,
		HeadSHA:           raw.Head.SHA,
		BaseRef:           raw.Base.Ref,
		BaseSHA:           raw.Base.SHA,
		Body:              raw.Body,
		AuthorLogin:       raw.User.Login,
		AuthorAssociation: raw.AuthorAssociation,
	}, nil
}

// ghFileEntry mirrors one element of the List pull request files response.
type ghFileEntry struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// listPaginated walks a paginated array endpoint (per_page=100, Link
// rel="next"), accumulating every page's elements as raw JSON.
func (c *Client) listPaginated(ctx context.Context, path string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	query := url.Values{"per_page": []string{"100"}}
	for {
		var page []json.RawMessage
		resp, err := c.attempt(ctx, http.MethodGet, path, query, nil)
		if err != nil {
			return nil, err
		}
		if err := finish(resp, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		next := linkNext(resp.Header.Get("Link"))
		if next == "" {
			return all, nil
		}
		// The rel="next" target is always the same endpoint with a bumped
		// page parameter; adopt its query rather than re-deriving the path.
		u, err := url.Parse(next)
		if err != nil {
			return nil, fmt.Errorf("bad Link header: %w", err)
		}
		query = url.Values{}
		for k, v := range u.Query() {
			query[k] = v
		}
	}
}

// linkNext extracts the rel="next" target from an RFC 5988 Link header.
func linkNext(header string) string {
	for _, part := range strings.Split(header, ",") {
		seg := strings.Split(part, ";")
		var u string
		var next bool
		for _, p := range seg {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "<") && strings.HasSuffix(p, ">") {
				u = strings.Trim(p, "<>")
			}
			if p == `rel="next"` {
				next = true
			}
		}
		if next && u != "" {
			return u
		}
	}
	return ""
}

// ListPRFiles returns the changed-file manifest from GET
// /repos/{o}/{r}/pulls/{n}/files, following pagination, and converges on
// scope.ManifestEntry like every other manifest constructor.
func (c *Client) ListPRFiles(ctx context.Context, num int) ([]scope.ManifestEntry, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d/files", c.owner, c.repo, num))
	if err != nil {
		return nil, err
	}
	blob, err := json.Marshal(raws)
	if err != nil {
		return nil, err
	}
	return scope.ParseGitHubFilesAPI(blob)
}

// FileExtra is per-file metadata the plain manifest does not carry: the
// unified-diff patch text and the head blob SHA used for incremental
// re-review keyed on content (§10).
type FileExtra struct {
	Filename string
	Patch    string
	BlobSHA  string
}

// ListPRFileExtras returns filename → {patch, blob sha} for a pull request.
func (c *Client) ListPRFileExtras(ctx context.Context, num int) (map[string]FileExtra, error) {
	raws, err := c.listPaginated(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d/files", c.owner, c.repo, num))
	if err != nil {
		return nil, err
	}
	out := map[string]FileExtra{}
	for _, r := range raws {
		var f struct {
			Filename string `json:"filename"`
			Patch    string `json:"patch"`
			SHA      string `json:"sha"`
		}
		if err := json.Unmarshal(r, &f); err != nil {
			return nil, err
		}
		out[f.Filename] = FileExtra{Filename: f.Filename, Patch: f.Patch, BlobSHA: f.SHA}
	}
	return out, nil
}

// contentsFile is the subset of the contents API response Cite reads.
type contentsFile struct {
	SHA       string `json:"sha"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	Size      int    `json:"size"`
	Truncated bool   `json:"truncated"`
}

// decodeContents decodes the base64 payload of a contents/blob response.
// GitHub wraps base64 in newlines; strip them before decoding.
func decodeContents(encoded string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, encoded)
	return base64.StdEncoding.DecodeString(clean)
}

// getFile fetches one file's metadata+content via the contents API.
func (c *Client) getFile(ctx context.Context, owner, repo, ref, path string) (*contentsFile, bool, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	var cf contentsFile
	resp, err := c.attempt(ctx, http.MethodGet, fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, escapePath(path)), q, nil)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, false, nil
	}
	if err := finish(resp, &cf); err != nil {
		return nil, false, err
	}
	return &cf, true, nil
}

// escapePath encodes each path segment but keeps "/" separators, so paths
// with spaces or "#" reach the right resource.
func escapePath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// GetFileContent returns the bytes of path at ref via the contents API.
// A missing file is (nil, false, nil) — absence is data, not an error.
func (c *Client) GetFileContent(ctx context.Context, ref, path string) ([]byte, bool, error) {
	cf, ok, err := c.getFile(ctx, c.owner, c.repo, ref, path)
	if err != nil || !ok {
		return nil, false, err
	}
	if cf.Encoding != "base64" {
		return []byte(cf.Content), true, nil
	}
	raw, err := decodeContents(cf.Content)
	if err != nil {
		return nil, false, fmt.Errorf("decoding content of %s: %w", path, err)
	}
	return raw, true, nil
}

// GetMergeBase returns the merge base commit SHA of base...head via the
// compare API. It keys incremental re-review state (§10).
func (c *Client) GetMergeBase(ctx context.Context, base, head string) (string, error) {
	var raw struct {
		MergeBaseCommit struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("repos/%s/%s/compare/%s...%s", c.owner, c.repo, url.PathEscape(base), url.PathEscape(head)),
		nil, nil, &raw)
	if err != nil {
		return "", err
	}
	return raw.MergeBaseCommit.SHA, nil
}

// sortStrings sorts in place; used by the tree adapters to keep List output
// lexically ordered as the Tree contract requires.
func sortStrings(s []string) { sort.Strings(s) }

// SearchCode runs a code-search query and decodes the response. Used by the
// external-claims verifier for symbol_exists; callers treat API failure as
// "unverified", which drops the finding rather than blocking on a guess.
func (c *Client) SearchCode(ctx context.Context, query string, out any) error {
	q := url.Values{"q": []string{query}, "per_page": []string{"1"}}
	return c.do(ctx, http.MethodGet, "search/code", q, nil, out)
}
