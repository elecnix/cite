package instructions

import "strings"

// frontmatter is the subset of YAML frontmatter Cite honours (PLAN.md §5).
// applyTo is documented as comma-separated globs in ONE string; the
// YAML-array form is accepted as an alias (the dialect fork §5). paths is
// the .claude/rules equivalent.
type frontmatter struct {
	ApplyTo      []string
	Paths        []string
	Description  string
	Name         string
	ExcludeAgent string
	Present      bool
}

// parseFrontmatter extracts a leading `---` delimited block. A missing or
// malformed block is not an error: the file simply has no frontmatter.
func parseFrontmatter(content []byte) frontmatter {
	var fm frontmatter
	s := string(content)
	if !strings.HasPrefix(s, "---\n") && s != "---" {
		return fm
	}
	rest := s[4:]
	start, _ := findDelimiter(rest)
	if start < 0 {
		return fm
	}
	fm.Present = true
	cur := ""
	for _, raw := range strings.Split(rest[:start], "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "- ") && cur != "" {
			item := stripQuotes(strings.TrimSpace(line[2:]))
			switch cur {
			case "applyTo":
				fm.ApplyTo = append(fm.ApplyTo, splitCSV(item)...)
			case "paths":
				fm.Paths = append(fm.Paths, splitCSV(item)...)
			}
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		cur = key
		switch key {
		case "applyTo":
			fm.ApplyTo = parseGlobValue(val)
		case "paths":
			fm.Paths = parseGlobValue(val)
		case "description":
			fm.Description = stripQuotes(val)
		case "name":
			fm.Name = stripQuotes(val)
		case "excludeAgent":
			fm.ExcludeAgent = stripQuotes(val)
		}
	}
	return fm
}

// parseGlobValue accepts the comma-separated single-string form and the
// YAML flow-array alias (`[a, b]`); both are split on commas.
func parseGlobValue(val string) []string {
	val = strings.TrimSpace(val)
	if val == "" || val == "[]" {
		return nil
	}
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
		val = val[1 : len(val)-1]
	}
	return splitCSV(val)
}

func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		p := stripQuotes(strings.TrimSpace(part))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stripQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func findDelimiter(s string) (int, int) {
	for i := 0; i+4 <= len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		j := i + 1
		k := j
		for k < len(s) && s[k] == '-' {
			k++
		}
		if k > j && k-j >= 3 && (k == len(s) || s[k] == '\n' || s[k] == '\r') {
			return i, k // start of the newline, end just past the dashes
		}
	}
	return -1, -1
}

// stripFrontmatter removes a leading frontmatter block so section splitting
// never mistakes it for body text.
func stripFrontmatter(content []byte) []byte {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") && s != "---" {
		return content
	}
	rest := s[4:]
	_, dashEnd := findDelimiter(rest)
	if dashEnd < 0 {
		return content
	}
	return []byte(rest[dashEnd:])
}

// isExcludedAgent reports whether the file opted out of Cite via
// `excludeAgent: code-review` (§5): a repository that excluded the incumbent
// reviewer from a file meant it.
func isExcludedAgent(agent string) bool {
	return strings.EqualFold(strings.TrimSpace(agent), "code-review")
}
