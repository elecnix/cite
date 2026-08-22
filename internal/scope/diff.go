package scope

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LineKind classifies one line of a unified diff.
type LineKind string

const (
	LineAdded   LineKind = "added"
	LineRemoved LineKind = "removed"
	LineContext LineKind = "context"
)

// Diff is a parsed multi-file unified diff.
type Diff struct {
	Files []*DiffFile
}

// DiffFile is one file's slice of a unified diff. For renames OldPath is the
// pre-change path; for added files OldPath is empty; for deleted files Path
// is empty and the content appears only as removed lines with old numbers.
type DiffFile struct {
	Path    string
	OldPath string
	Hunks   []*Hunk
}

// Hunk is one @@ -old,count +new,count @@ region.
type Hunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []Line
}

// Line is a single diff line. OldNo/NewNo are 0 when the line does not exist
// on that side. Deleted lines carry only OldNo: they have no post-change
// anchor and can never be commented on (§7).
type Line struct {
	Kind    LineKind
	OldNo   int
	NewNo   int
	Content string
}

// AddedLines returns the post-change line numbers of added lines, ascending.
func (f *DiffFile) AddedLines() []int {
	var out []int
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == LineAdded {
				out = append(out, l.NewNo)
			}
		}
	}
	return out
}

// AnchorableLines is the set of post-change line numbers present in the
// hunks — added or context. Findings may only anchor there: a line outside
// the hunks was neither seen nor touched by this change.
func (f *DiffFile) AnchorableLines() map[int]bool {
	out := make(map[int]bool)
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == LineAdded || l.Kind == LineContext {
				out[l.NewNo] = true
			}
		}
	}
	return out
}

// RemovedLines returns the removed lines with their OLD line numbers, for the
// <removed_lines> envelope block.
func (f *DiffFile) RemovedLines() []Line {
	var out []Line
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == LineRemoved {
				out = append(out, l)
			}
		}
	}
	return out
}

// FileByPath returns the file whose post-change path (or, for renames and
// deletions, pre-change path) equals p, or nil.
func (d *Diff) FileByPath(p string) *DiffFile {
	for _, f := range d.Files {
		if f.Path == p || f.OldPath == p {
			return f
		}
	}
	return nil
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseUnifiedDiff parses a unified diff (git or GNU style) into a Diff.
// It tolerates extended git headers (index, mode, rename from/to, "Binary
// files ... differ") and "\ No newline at end of file" markers. A hunk line
// appearing before any @@ header is an error: the parse is structural, and a
// truncated diff must not silently become a shorter one.
func ParseUnifiedDiff(text string) (*Diff, error) {
	d := &Diff{}
	lines := strings.Split(text, "\n")
	var cur *DiffFile
	var hunk *Hunk
	oldNo, newNo := 0, 0

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")

		switch {
		case strings.HasPrefix(line, "diff --git "):
			a, b := parseGitDiffPaths(line)
			cur = &DiffFile{Path: b, OldPath: a}
			d.Files = append(d.Files, cur)
			hunk = nil

		case strings.HasPrefix(line, "--- "):
			if cur != nil {
				if p := parseDiffPath(line[4:], "a/"); p != "" {
					cur.OldPath = p
				} else if cur.Path != "" && cur.OldPath == cur.Path {
					cur.OldPath = ""
				}
			}

		case strings.HasPrefix(line, "+++ "):
			if cur != nil {
				if p := parseDiffPath(line[4:], "b/"); p != "" {
					cur.Path = p
				}
			}

		case strings.HasPrefix(line, "@@ "):
			m := hunkHeaderRe.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("scope: malformed hunk header at line %d: %q", i+1, line)
			}
			var err error
			h := &Hunk{}
			if h.OldStart, err = strconv.Atoi(m[1]); err != nil {
				return nil, fmt.Errorf("scope: bad old start in hunk header %q", line)
			}
			if m[2] == "" {
				h.OldLines = 1
			} else if h.OldLines, err = strconv.Atoi(m[2]); err != nil {
				return nil, fmt.Errorf("scope: bad old count in hunk header %q", line)
			}
			if h.NewStart, err = strconv.Atoi(m[3]); err != nil {
				return nil, fmt.Errorf("scope: bad new start in hunk header %q", line)
			}
			if m[4] == "" {
				h.NewLines = 1
			} else if h.NewLines, err = strconv.Atoi(m[4]); err != nil {
				return nil, fmt.Errorf("scope: bad new count in hunk header %q", line)
			}
			if cur == nil {
				return nil, fmt.Errorf("scope: hunk header before any file header at line %d", i+1)
			}
			cur.Hunks = append(cur.Hunks, h)
			hunk = h
			oldNo, newNo = h.OldStart, h.NewStart

		case hunk != nil && strings.HasPrefix(line, "+"):
			hunk.Lines = append(hunk.Lines, Line{Kind: LineAdded, NewNo: newNo, Content: line[1:]})
			newNo++

		case hunk != nil && strings.HasPrefix(line, "-"):
			hunk.Lines = append(hunk.Lines, Line{Kind: LineRemoved, OldNo: oldNo, Content: line[1:]})
			oldNo++

		case hunk != nil && strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file": no line number advances.

		case hunk != nil:
			// Context line, including the empty-line case (a context line
			// whose single leading space was stripped by tooling).
			content := strings.TrimPrefix(line, " ")
			hunk.Lines = append(hunk.Lines, Line{Kind: LineContext, OldNo: oldNo, NewNo: newNo, Content: content})
			oldNo++
			newNo++

		default:
			// Extended headers and prose between files: ignored.
		}
	}
	return d, nil
}

// parseGitDiffPaths extracts the two paths from "diff --git a/x b/y",
// handling paths that contain " b/" by splitting on the last possible
// boundary: it tries the longest prefix for a/ first.
func parseGitDiffPaths(line string) (a, b string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	rest = strings.TrimSpace(rest)
	// Try each occurrence of " b/" from the left; the a-side must start
	// with "a/".
	for i := 0; i+3 <= len(rest); i++ {
		if rest[i:i+3] != " b/" {
			continue
		}
		cand := rest[:i]
		if strings.HasPrefix(cand, "a/") && len(cand) > 2 {
			return strings.TrimPrefix(cand, "a/"), rest[i+3:]
		}
	}
	return rest, rest
}

// parseDiffPath strips an optional timestamp (after a tab) and the a/ or b/
// prefix. "/dev/null" yields "".
func parseDiffPath(s, prefix string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, prefix)
}
