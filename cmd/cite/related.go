package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/reviewer"
	"github.com/elecnix/cite/internal/xref"
)

// Related code (internal/xref): each review call also carries the
// definitions its changed lines use and the call sites of what they change,
// from the rest of the repository at the head. CITE_RELATED_CODE=0 turns it
// off, which is how an evaluation measures what it buys.

const (
	relatedMaxFileBytes  = 256 << 10
	relatedMaxTotalBytes = 64 << 20
	relatedFetchTimeout  = 60 * time.Second
)

func relatedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CITE_RELATED_CODE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// relatedFromDir indexes the working tree, for local mode.
func relatedFromDir(dir string) reviewer.RelatedFinder {
	if !relatedEnabled() {
		return nil
	}
	snap, err := xref.LoadDir(dir, relatedMaxTotalBytes)
	if err != nil {
		logToStderr("warning: related code unavailable; files are reviewed alone: %v", err)
		return nil
	}
	return xref.NewIndex(snap, xref.Limits{})
}

// relatedFromAPI indexes the head tree fetched as one tarball. A failed fetch
// costs only the related code, so it warns and the review goes on without.
func relatedFromAPI(ctx context.Context, c *githubclient.Client, owner, repo, sha string) reviewer.RelatedFinder {
	if !relatedEnabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, relatedFetchTimeout)
	defer cancel()
	files, truncated, err := c.Tarball(ctx, owner, repo, sha, xref.Searchable, relatedMaxFileBytes, relatedMaxTotalBytes)
	if err != nil {
		logToStderr("warning: head tree unavailable for related code; files are reviewed alone: %v", err)
		return nil
	}
	if truncated {
		logToStderr("related code: the head tree passed %d MiB; definitions past that are not searched", relatedMaxTotalBytes>>20)
	}
	return xref.NewIndex(xref.MapSnapshot(files), xref.Limits{})
}
