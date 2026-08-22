package instructions

import "strings"

// section is the internal split unit; forced marks sections inside a
// `## Review` span, which wins wholesale and skips triage (PLAN.md §5:
// "An override that costs nothing to create").
type section struct {
	Section
	forced bool
}

// splitSections splits markdown content on ATX headings. A heading whose
// text is exactly "Review" opens a wholesale-override span that extends
// until the next heading of the same or higher level; every section inside
// the span bypasses triage.
func splitSections(content []byte) []section {
	var out []section
	var cur strings.Builder
	curHeading := ""
	curForced := false
	opened := false // a section has been started (preamble counts)
	reviewLevel := -1

	flush := func() {
		if !opened {
			return
		}
		text := strings.TrimRight(cur.String(), " \t\n\r")
		if text == "" && curHeading == "" {
			return // empty preamble: not a section
		}
		if text == "" {
			return // heading with no body carries no guidance
		}
		out = append(out, section{Section: Section{Heading: curHeading, Text: text}, forced: curForced})
	}

	lines := strings.Split(string(stripFrontmatter(content)), "\n")
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if lvl, txt, ok := headingOf(line); ok {
			if reviewLevel >= 0 && lvl <= reviewLevel {
				reviewLevel = -1
			}
			flush()
			cur.Reset()
			curHeading = txt
			curForced = reviewLevel >= 0
			opened = true
			if strings.EqualFold(txt, "Review") {
				reviewLevel = lvl
				curForced = true
			}
			continue
		}
		if opened || strings.TrimSpace(line) != "" {
			cur.WriteString(line)
			cur.WriteByte('\n')
			opened = true
		}
	}
	flush()
	return out
}

// headingOf recognises ATX headings: 1-6 '#' followed by a space or tab.
func headingOf(line string) (level int, text string, ok bool) {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return 0, "", false
	}
	if i < len(line) && line[i] != ' ' && line[i] != '\t' {
		return 0, "", false
	}
	return i, strings.TrimSpace(strings.TrimSpace(line[i:])), true
}
