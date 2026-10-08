package githubclient

// The head tree as one archive: the related-code pass (internal/xref)
// searches the whole repository for definitions, and one tarball request
// costs one API call where reading file by file would cost one per file.
// Nothing in the archive is executed or written to disk (§12, I1): it is
// read into memory, filtered, and searched as text.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Tarball returns the regular files of owner/repo at ref whose path keep
// accepts, read into memory. A file larger than maxFile is left out, and
// reading stops adding files once maxTotal bytes are held; Tarball reports
// that in truncated rather than failing, because a partial snapshot still
// answers most lookups.
func (c *Client) Tarball(ctx context.Context, owner, repo, ref string, keep func(path string) bool, maxFile, maxTotal int64) (files map[string][]byte, truncated bool, err error) {
	u := c.baseURL + fmt.Sprintf("repos/%s/%s/tarball/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	c.observeRateLimit(resp.Header)
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, false, &APIError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
	}
	return readTarball(resp.Body, keep, maxFile, maxTotal)
}

// readTarball reads a gzipped tar whose entries sit under one top-level
// directory, as GitHub's archives do, and returns them keyed by the path
// below it.
func readTarball(r io.Reader, keep func(string) bool, maxFile, maxTotal int64) (map[string][]byte, bool, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, false, fmt.Errorf("tarball: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	var total int64
	truncated := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, truncated, nil
		}
		if err != nil {
			return out, true, fmt.Errorf("tarball: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := h.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || !keep(name) {
			continue
		}
		if h.Size > maxFile {
			continue
		}
		if total+h.Size > maxTotal {
			truncated = true
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxFile+1))
		if err != nil {
			return out, true, fmt.Errorf("tarball: %s: %w", name, err)
		}
		total += int64(len(b))
		out[name] = b
	}
}
