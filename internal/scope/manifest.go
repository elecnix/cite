// Package scope decides what gets reviewed (§7): the changed-file manifest,
// skip classification with named reasons, coverage arithmetic, the unified
// diff parser, risk ranking above 40 flagged files, and the prompt envelope.
//
// The manifest is the only authority on which files exist (§7). It is built
// either from `git diff --name-status -M -C` output or from the GitHub REST
// "List pull request files" response; both constructors converge on
// ManifestEntry so downstream code never sees two shapes.
package scope

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ManifestEntry is one row of the changed-file manifest. Status is one of
// A (added), M (modified), D (deleted), or R###/C### (rename/copy with the
// similarity percentage). For renames and copies OldPath is the source path;
// a path listed as a rename source, or with D, no longer exists (§7).
type ManifestEntry struct {
	Status  string
	Path    string
	OldPath string // rename/copy source; empty otherwise
	Adds    int    // added lines, when known
	Dels    int    // deleted lines, when known
}

// ExistsAtHead reports whether the file still exists after the change.
// Rename sources and deletions do not exist; claiming they do — or that an
// existing file is missing — is the rename hallucination the manifest exists
// to prevent (§7).
func (e ManifestEntry) ExistsAtHead() bool {
	if e.Status == "D" {
		return false
	}
	return true // A, M, R### target, C### target
}

// ParseNameStatus parses `git diff --name-status -M -C` output. Lines look
// like:
//
//	A	path
//	M	path
//	D	path
//	R090	old	new
//	C100	old	new
//
// Two tolerances are accepted so the same parser reads rendered manifests
// too: an "old -> new" single-field rename spelling, and a trailing
// "+N/-M" line-count column (as emitted by numstat-aware renderers). Blank
// lines are ignored. Unparsable lines are ignored rather than fatal — the
// caller pairs this with an API-count assertion via ComputeCoverage, so a
// silently short manifest cannot produce a false green.
func ParseNameStatus(text string) []ManifestEntry {
	var out []ManifestEntry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if e, ok := parseNameStatusLine(line); ok {
			out = append(out, e)
		}
	}
	return out
}

func parseNameStatusLine(line string) (ManifestEntry, bool) {
	var fields []string
	if strings.Contains(line, "\t") {
		fields = strings.Split(line, "\t")
	} else {
		fields = strings.Fields(line)
	}
	if len(fields) < 2 {
		return ManifestEntry{}, false
	}
	status := strings.TrimSpace(fields[0])
	if !isValidStatus(status) {
		return ManifestEntry{}, false
	}
	rest := make([]string, 0, len(fields)-1)
	for _, f := range fields[1:] {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		rest = append(rest, f)
	}

	e := ManifestEntry{Status: status}
	// Optional trailing "+N/-M" counts column.
	if n := len(rest); n > 0 {
		if adds, dels, ok := parseCounts(rest[n-1]); ok {
			e.Adds, e.Dels = adds, dels
			rest = rest[:n-1]
		}
	}

	isCopyRename := strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")
	if isCopyRename {
		// Handle both "old<TAB>new" fields and a rendered "old -> new" field.
		joined := strings.Join(rest, " ")
		if strings.Contains(joined, " -> ") {
			parts := strings.SplitN(joined, " -> ", 2)
			e.OldPath, e.Path = parts[0], parts[1]
		} else if len(rest) >= 2 {
			e.OldPath, e.Path = rest[0], rest[1]
		} else {
			return ManifestEntry{}, false
		}
	} else if len(rest) >= 1 {
		e.Path = rest[0]
	} else {
		return ManifestEntry{}, false
	}
	return e, true
}

func isValidStatus(s string) bool {
	switch s {
	case "A", "M", "D", "T":
		return true
	}
	if len(s) < 2 {
		return false
	}
	if s[0] != 'R' && s[0] != 'C' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func parseCounts(s string) (adds, dels int, ok bool) {
	if !strings.HasPrefix(s, "+") {
		return 0, 0, false
	}
	i := strings.Index(s, "/-")
	if i < 0 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(s[1:i])
	d, err2 := strconv.Atoi(s[i+2:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return a, d, true
}

// ghPRFile mirrors one object of the GitHub REST "List pull request files"
// response. Only the fields the manifest needs are declared.
type ghPRFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"` // added|removed|modified|renamed|changed|copied
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}

// ParseGitHubFilesAPI parses the JSON array returned by the GitHub REST
// endpoint "List pull request files" into manifest entries. This API shape —
// never a filesystem walk — is the authoritative changed-file list (§7).
//
// Status mapping: added→A, removed→D, modified→M, changed→M (submodule
// content change), renamed→R (this endpoint does not expose the similarity
// percentage), copied→C. An unknown status is an error: the manifest is the
// authority on which files exist, and guessing would forge that authority.
func ParseGitHubFilesAPI(data []byte) ([]ManifestEntry, error) {
	var raw []ghPRFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("scope: parsing pull-request files response: %w", err)
	}
	out := make([]ManifestEntry, 0, len(raw))
	for _, f := range raw {
		if f.Filename == "" {
			return nil, fmt.Errorf("scope: pull-request files entry with empty filename")
		}
		e := ManifestEntry{
			Path:    f.Filename,
			OldPath: f.PreviousFilename,
			Adds:    f.Additions,
			Dels:    f.Deletions,
		}
		switch f.Status {
		case "added":
			e.Status = "A"
		case "removed":
			e.Status = "D"
		case "modified", "changed":
			e.Status = "M"
		case "renamed":
			e.Status = "R"
			if e.OldPath == "" {
				e.OldPath = e.Path
			}
		case "copied":
			e.Status = "C"
			if e.OldPath == "" {
				e.OldPath = e.Path
			}
		default:
			return nil, fmt.Errorf("scope: pull-request file %q has unknown status %q", f.Filename, f.Status)
		}
		out = append(out, e)
	}
	return out, nil
}
