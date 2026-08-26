package scope

import (
	"errors"
	"fmt"
	"strings"
)

// ParseFilePatch parses one per-file patch in GitHub's PR-files API shape —
// bare @@ hunks with no "diff --git" / "---" / "+++" lines — into a DiffFile,
// synthesizing the missing headers from the manifest's known path and status.
// status is the GitHub file status ("A", "M", "D", "R…"); for additions the
// old side is /dev/null by convention. An empty or structurally invalid patch
// returns an error so callers can fail loudly instead of reviewing without
// hunk data (which silently drops every anchored finding).
func ParseFilePatch(path, status, patch string) (*DiffFile, error) {
	if strings.TrimSpace(patch) == "" {
		return nil, fmt.Errorf("scope: empty patch for %s", path)
	}
	oldPath := path
	if status == "A" || strings.HasPrefix(status, "A") {
		oldPath = "/dev/null"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", path, path)
	fmt.Fprintf(&sb, "--- a/%s\n", oldPath)
	fmt.Fprintf(&sb, "+++ b/%s\n", path)
	sb.WriteString(strings.TrimRight(patch, "\n"))
	d, err := ParseUnifiedDiff(sb.String())
	if err != nil {
		return nil, err
	}
	if len(d.Files) == 0 {
		return nil, errors.New("scope: patch parsed to no files")
	}
	return d.Files[0], nil
}
