package scope

import (
	"testing"

	"github.com/elecnix/cite/internal/glob"
)

// The glob dialect — the grammar, the normalisation, the root-pattern rule and
// the empty-name cases — is pinned once, in internal/glob. It used to be
// duplicated here, with internal/instructions cross-checking its own answers
// against this package's. What has to stay here is the narrower claim this
// package actually owns: that paths_ignore reaches that one matcher, rather
// than carrying a second implementation that could drift from it.

// TestPathsIgnoreUsesTheSharedDialect proves the paths_ignore entry point is
// glob.Match, by asserting the user-visible skip decision for the spellings
// the two surfaces once disagreed on.
//
// SkipReason is the only place extraIgnores is consulted, so a normalisation
// rule that internal/glob has and scope.SkipReason did not would show up here
// as a missing skip.
func TestPathsIgnoreUsesTheSharedDialect(t *testing.T) {
	const changed = "docs/guide/page.md"
	cases := []struct {
		pattern string
		want    bool // is changed ignored by this pattern?
	}{
		{"docs/**", true},
		{"/docs/**", true},
		{"./docs/**", true},
		{"docs//**", true},
		{"docs/**/*.md", true},
		{"./docs/**/*.md", true},
		{"docs/guide/*.md", true},
		{"docs/[a-z]*/page.md", true},
		// 'docs/' names the directory itself, not its contents.
		{"docs/", false},
		// '*' never crosses a '/', so this is two segments against three.
		{"docs/*.md", false},
		{"docs/*.go", false},
		{"src/**", false},
		{"", false},
		{".", false},
		{"/", false},
	}
	for _, c := range cases {
		reason, skipped := SkipReason(changed, nil, []string{c.pattern})
		if skipped != c.want {
			t.Errorf("paths_ignore %q against %q: skipped = %v, want %v", c.pattern, changed, skipped, c.want)
			continue
		}
		// The decision must be the shared dialect's, not a coincidence.
		if want := glob.Match(c.pattern, changed); want != c.want {
			t.Errorf("paths_ignore %q and glob.Match disagree: skip = %v, glob.Match = %v", c.pattern, skipped, want)
		}
		if skipped && reason != SkipReasonIgnored {
			t.Errorf("paths_ignore %q: reason = %q, want %q", c.pattern, reason, SkipReasonIgnored)
		}
	}
}

// TestPathsIgnoreRootPatternDoesNotIgnoreEverything guards the rule that is
// easiest to get wrong in this package: an empty or root-shaped paths_ignore
// entry must not swallow the repository. It matches nothing, so every file
// stays in scope.
func TestPathsIgnoreRootPatternDoesNotIgnoreEverything(t *testing.T) {
	for _, pattern := range []string{"", "/", "//", "///", "./", ".", ".//", "/./"} {
		if reason, skipped := SkipReason("README.md", nil, []string{pattern}); skipped {
			t.Errorf("paths_ignore %q ignored README.md (%s); a pattern naming no segment must ignore nothing", pattern, reason)
		}
	}
}
