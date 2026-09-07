package main

import (
	"strings"
	"testing"
)

// The reviewer identity selects the namespace a run publishes under. It ends
// up in GitHub-facing names, so an explicitly invalid value must fail loudly
// instead of silently trampling the canonical reviewer's state.
func TestResolveReviewerID(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    string
		wantErr bool
	}{
		{"unset defaults to cite", "", defaultReviewerID, false},
		{"head build identity", "cite-head", "cite-head", false},
		{"single char", "a", "a", false},
		{"digits and dashes", "cite-2-fast", "cite-2-fast", false},
		{"uppercase rejected", "Cite", "", true},
		{"underscore rejected", "cite_head", "", true},
		{"spaces rejected", "cite head", "", true},
		{"leading dash rejected", "-cite", "", true},
		{"empty string falls back to default", "", defaultReviewerID, false},
		{"too long rejected", strings.Repeat("a", 41), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(reviewerIDEnv, tc.env)
			got, err := resolveReviewerID()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveReviewerID(%q) = %q, want error", tc.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveReviewerID(%q): %v", tc.env, err)
			}
			if got != tc.want {
				t.Errorf("resolveReviewerID(%q) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

// The default identity must keep the legacy marker byte-for-byte: stickies
// written before identities existed have to stay readable.
func TestStickyMarkerForDefaultIsLegacy(t *testing.T) {
	if got := stickyMarkerFor(defaultReviewerID); got != stickyMarker {
		t.Errorf("stickyMarkerFor(%q) = %q, want the legacy %q", defaultReviewerID, got, stickyMarker)
	}
	if got := checkNameFor(defaultReviewerID); got != "cite" {
		t.Errorf("checkNameFor(%q) = %q, want %q", defaultReviewerID, got, "cite")
	}
}

// Markers are found by substring search, so no identity's marker may appear
// inside another's — otherwise two parallel reviewers would read and
// overwrite each other's sticky state.
func TestStickyMarkersNeverOverlap(t *testing.T) {
	ids := []string{defaultReviewerID, "cite-head", "cite2", "reviewer-b"}
	for _, a := range ids {
		for _, b := range ids {
			if a == b {
				continue
			}
			if strings.Contains(stickyMarkerFor(a), stickyMarkerFor(b)) {
				t.Errorf("marker for %q contains marker for %q: %q", a, b, stickyMarkerFor(a))
			}
		}
	}
}

func TestStickyMarkerForDerived(t *testing.T) {
	got := stickyMarkerFor("cite-head")
	want := "<!-- cite-sticky cite-head -->"
	if got != want {
		t.Errorf("stickyMarkerFor(cite-head) = %q, want %q", got, want)
	}
}
