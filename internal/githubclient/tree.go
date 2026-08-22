package githubclient

// Tree adapters for internal/instructions: the same interface served either
// from a real checkout (FSTree) or straight from the GitHub API with no
// clone at all (APITree) — §1's no-checkout property means instruction
// discovery must work against a remote ref.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/elecnix/cite/internal/instructions"
)

// compile-time proof both adapters satisfy instructions.Tree exactly.
var (
	_ instructions.Tree = (*FSTree)(nil)
	_ instructions.Tree = (*APITree)(nil)
)

// FSTree serves instructions.Tree from a directory on disk.
type FSTree struct {
	root string
}

// NewFSTree builds a Tree over root. Paths are relative to root; reads
// outside root are refused.
func NewFSTree(root string) *FSTree { return &FSTree{root: root} }

// List walks root and returns regular files beneath dir, relative to root,
// sorted lexically. dir == "" lists the entire tree.
func (t *FSTree) List(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(t.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // skip dirs, symlinks, sockets
		}
		rel, relErr := filepath.Rel(t.root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if dir != "" && !pathUnder(rel, dir) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortStrings(out)
	return out, nil
}

// Read returns the bytes of path, or (nil, false, nil) when absent — a
// missing file is data, not an error, per the Tree contract.
func (t *FSTree) Read(path string) ([]byte, bool, error) {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\x00") {
		return nil, false, fmt.Errorf("fstree: refusing path %q", path)
	}
	full := filepath.Join(t.root, filepath.FromSlash(clean))
	raw, err := os.ReadFile(full)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

// pathUnder reports whether path is dir itself or lies beneath it.
func pathUnder(path, dir string) bool {
	dir = strings.TrimSuffix(dir, "/")
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// APITree serves instructions.Tree from one git tree fetched over the API:
// the listing is fetched once and cached, blobs lazily on first Read. It
// exists so Cite reads base-ref instruction files without any checkout of
// head code (§1, I1).
type APITree struct {
	c           *Client
	owner, repo string
	ref         string
	ctx         context.Context // instructions.Tree carries no ctx; callers may inject one

	mu    sync.Mutex
	list  map[string]string // path -> blob sha for regular files
	dirty bool              // listing not yet loaded
	trunc bool              // server-side listing was truncated
}

// NewAPITree backs instructions.Tree with GET /git/trees/{ref}?recursive=1.
func NewAPITree(c *Client, owner, repo, ref string) *APITree {
	return &APITree{c: c.WithRepo(owner, repo), owner: owner, repo: repo, ref: ref, ctx: context.Background(), dirty: true}
}

// WithContext returns an independent copy whose API calls use ctx.
func (t *APITree) WithContext(ctx context.Context) *APITree {
	t.mu.Lock()
	list := t.list
	trunc := t.trunc
	t.mu.Unlock()
	dirty := list == nil
	return &APITree{c: t.c, owner: t.owner, repo: t.repo, ref: t.ref,
		ctx: ctx, list: list, trunc: trunc, dirty: dirty}
}

// load refreshes the cached listing when needed.
func (t *APITree) load() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.dirty {
		return nil
	}
	var out struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	q := url.Values{"recursive": []string{"1"}}
	if err := t.c.do(t.ctx, http.MethodGet,
		fmt.Sprintf("repos/%s/%s/git/trees/%s", t.owner, t.repo, url.PathEscape(t.ref)), q, nil, &out); err != nil {
		return err
	}
	list := make(map[string]string, len(out.Tree))
	for _, e := range out.Tree {
		if e.Type == "blob" {
			list[e.Path] = e.SHA
		}
	}
	t.list = list
	t.trunc = out.Truncated
	t.dirty = false
	return nil
}

// List returns all blob paths in the tree beneath dir, sorted lexically.
func (t *APITree) List(dir string) ([]string, error) {
	if err := t.load(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.list))
	for p := range t.list {
		if dir != "" && !pathUnder(p, dir) {
			continue
		}
		out = append(out, p)
	}
	sortStrings(out)
	return out, nil
}

// Truncated reports whether GitHub cut the recursive listing short; a
// truncated listing cannot promise full coverage, so callers can treat it
// like any other disclosed incompleteness rather than a silent gap.
func (t *APITree) Truncated() bool {
	if err := t.load(); err != nil {
		return true // unknown state: disclose as truncated
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trunc
}

// Read fetches one blob lazily. The contents API answers first; when its
// payload was truncated (>1 MB), or the blob is known large, the git blobs
// endpoint supplies the whole thing — truncation is routed around, never
// silently accepted (§5 reads whole files).
func (t *APITree) Read(path string) ([]byte, bool, error) {
	if err := t.load(); err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	sha := t.list[path]
	t.mu.Unlock()
	data, found, truncated, err := t.c.blobViaContents(t.ctx, t.owner, t.repo, t.ref, path)
	if err != nil {
		return nil, false, err
	}
	if found && !truncated {
		return data, true, nil
	}
	if sha != "" {
		return t.c.blobBySHA(t.ctx, t.owner, t.repo, sha)
	}
	return data, found, nil
}

// blobViaContents fetches one file via the contents API, also reporting
// whether GitHub truncated the payload.
func (c *Client) blobViaContents(ctx context.Context, owner, repo, ref, path string) (data []byte, found, truncated bool, err error) {
	cf, ok, err := c.getFile(ctx, owner, repo, ref, path)
	if err != nil || !ok {
		return nil, false, false, err
	}
	if cf.Encoding != "base64" {
		return []byte(cf.Content), true, cf.Truncated, nil
	}
	raw, err := decodeContents(cf.Content)
	if err != nil {
		return nil, true, false, fmt.Errorf("decoding content of %s: %w", path, err)
	}
	return raw, true, cf.Truncated, nil
}

// blobBySHA fetches a blob directly from GET /git/blobs/{sha}, which never
// truncates.
func (c *Client) blobBySHA(ctx context.Context, owner, repo, sha string) ([]byte, bool, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	resp, err := c.attempt(ctx, http.MethodGet,
		fmt.Sprintf("repos/%s/%s/git/blobs/%s", owner, repo, url.PathEscape(sha)), nil, nil)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, false, nil
	}
	if err := finish(resp, &out); err != nil {
		return nil, false, err
	}
	if out.Encoding != "base64" {
		return []byte(out.Content), true, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, out.Content))
	if err != nil {
		return nil, false, fmt.Errorf("decoding blob %s: %w", sha, err)
	}
	return raw, true, nil
}
