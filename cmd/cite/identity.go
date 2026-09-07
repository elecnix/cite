// Reviewer identity: which Cite instance a run speaks to GitHub as.
//
// Two parallel reviewers — a released binary and the pull-request-head build
// — must post under different names or they fight over one check run and one
// sticky comment: each would conclude the other's check, each would overwrite
// the other's incremental state. An identity is the reviewer's namespace for
// everything GitHub-facing: the check run name and the sticky comment marker.
// The default identity keeps both byte-for-byte identical to the
// single-reviewer days, so pre-identity stickies and check runs keep working.

package main

import (
	"fmt"
	"os"
	"regexp"
)

const (
	// defaultReviewerID is the canonical reviewer identity: the check run
	// named "cite" and the legacy sticky marker.
	defaultReviewerID = "cite"

	// reviewerIDEnv selects a non-default identity for parallel reviewers.
	reviewerIDEnv = "CITE_REVIEWER_ID"
)

// reviewerIDRe bounds the identity: it becomes a check run name and part of a
// hidden HTML comment marker, so it gets a strict lowercase charset and a
// length a GitHub check name and a comment body can carry comfortably.
var reviewerIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// resolveReviewerID reads the reviewer identity from reviewerIDEnv, defaulting
// to defaultReviewerID when unset. An explicitly invalid identity is an error,
// never a silent fallback: a run that thinks it is "cite" while configured as
// something else would trample the canonical reviewer's state.
func resolveReviewerID() (string, error) {
	id := os.Getenv(reviewerIDEnv)
	if id == "" {
		return defaultReviewerID, nil
	}
	if !reviewerIDRe.MatchString(id) {
		return "", fmt.Errorf("%s must match %s, got %q", reviewerIDEnv, reviewerIDRe, id)
	}
	return id, nil
}

// checkNameFor is the check run name a reviewer with this identity publishes.
func checkNameFor(id string) string { return id }

// stickyMarkerFor is the sticky-comment marker for this identity. Marker
// searches are substring matches (githubclient.FindIssueComment), so the
// markers must never overlap: the legacy marker ends in " -->", and every
// derived marker inserts the identity before the closing, which makes each
// marker unmatchable by the others. The default identity returns the legacy
// marker unchanged.
func stickyMarkerFor(id string) string {
	if id == defaultReviewerID {
		return stickyMarker
	}
	return stickyMarker[:len(stickyMarker)-len(" -->")] + " " + id + " -->"
}
