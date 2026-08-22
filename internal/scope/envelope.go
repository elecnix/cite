package scope

import (
	"fmt"
	"strings"
)

// The envelope (§7): tags, never markdown headings — file content contains
// '##' and fences and will close your sections for you. Untrusted blocks
// carry a per-run nonce generated at run time, so nothing committed to the
// tree can forge a close tag. Exactly one code artifact per call: the
// post-change file, line-numbered, with '+' on added lines. Deleted lines go
// in a separate <removed_lines> block with their OLD numbers, because they
// have no post-change anchor and can never be commented on.

// EnvelopeLine is one line of the post-change file under review.
type EnvelopeLine struct {
	No      int    // post-change line number, 1-based
	Content string // without trailing newline; leading content verbatim
	Added   bool   // marked '+' when this change added or modified the line
}

// RemovedLine is one deleted line, anchored by its OLD number only.
type RemovedLine struct {
	OldNo   int
	Content string
}

// EnvelopeFile is the single code artifact of one envelope call.
// Context says how much of the file is present: "complete" or "partial".
type EnvelopeFile struct {
	Path    string
	OldPath string // rename source, if any
	Status  string // A / M / D / R###
	Context string // defaults to "complete"
	Lines   []EnvelopeLine
	Removed []RemovedLine
}

// BuildEnvelope renders the full prompt envelope for one review call.
//
// Sections, in order:
//
//	<manifest>            authoritative changed-file list (§7)
//	<pr_description ...>  untrusted, every line prefixed "| "
//	<file_under_review>   the one code artifact (omitted when file == nil)
//	<removed_lines>       deleted content with old numbers (only when present)
//
// nonce must be unique per run and is embedded in the pr_description open
// tag; it is sanitised so it cannot terminate the tag early.
func BuildEnvelope(manifest []ManifestEntry, prDescription string, nonce string, file *EnvelopeFile) string {
	var sections []string

	sections = append(sections, renderManifest(manifest))
	sections = append(sections, renderPRDescription(prDescription, sanitizeNonce(nonce)))
	if file != nil {
		sections = append(sections, renderFileUnderReview(file))
		if len(file.Removed) > 0 {
			sections = append(sections, renderRemovedLines(file.Path, file.Removed))
		}
	}
	return strings.Join(sections, "\n\n") + "\n"
}

func renderManifest(manifest []ManifestEntry) string {
	var b strings.Builder
	b.WriteString("<manifest>\n")
	b.WriteString(fmt.Sprintf("files_changed=%d truncated=false\n", len(manifest)))

	labels := make([]string, len(manifest))
	pathw := 0
	for i, e := range manifest {
		pathLabel := e.Path
		if e.OldPath != "" {
			pathLabel = e.OldPath + " -> " + e.Path
		}
		if len(pathLabel) > pathw {
			pathw = len(pathLabel)
		}
		labels[i] = pathLabel
	}
	for i, e := range manifest {
		statusField := e.Status
		for len(statusField) < 4 {
			statusField += " "
		}
		fmt.Fprintf(&b, "%s %-*s +%d/-%d\n", statusField, pathw, labels[i], e.Adds, e.Dels)
	}
	b.WriteString("</manifest>")
	return b.String()
}

func renderPRDescription(desc, nonce string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<pr_description trust=\"untrusted\" nonce=%q>\n", nonce)
	if desc != "" {
		lines := strings.Split(desc, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		for _, l := range lines {
			l = strings.TrimSuffix(l, "\r")
			// Prefix every line, including empty ones: a forged close tag
			// inside the description stays quoted data ("| </pr_description>
			//"), never a real close.
			b.WriteString("| ")
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	b.WriteString("</pr_description>")
	return b.String()
}

func renderFileUnderReview(f *EnvelopeFile) string {
	var b strings.Builder
	b.WriteString("<file_under_review")
	fmt.Fprintf(&b, " path=%q", f.Path)
	if f.OldPath != "" {
		fmt.Fprintf(&b, " old_path=%q", f.OldPath)
	}
	fmt.Fprintf(&b, " status=%q context=%q>\n", f.Status, contextOrDefault(f.Context))
	for _, l := range f.Lines {
		marker := " "
		if l.Added {
			marker = "+"
		}
		fmt.Fprintf(&b, "%04d %s|%s\n", l.No, marker, l.Content)
	}
	b.WriteString("</file_under_review>")
	return b.String()
}

func renderRemovedLines(path string, removed []RemovedLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<removed_lines path=%q>\n", path)
	for _, l := range removed {
		fmt.Fprintf(&b, "%04d  |%s\n", l.OldNo, l.Content)
	}
	b.WriteString("</removed_lines>")
	return b.String()
}

func contextOrDefault(c string) string {
	if c == "" {
		return "complete"
	}
	return c
}

// sanitizeNonce strips anything that could let a nonce value break out of its
// tag: quotes, angle brackets, and newlines.
func sanitizeNonce(n string) string {
	r := strings.NewReplacer("\"", "", "'", "", "<", "", ">", "", "\n", "", "\r", "")
	return r.Replace(n)
}
