package config

import (
	"fmt"
	"strings"
)

// Problem is one validation failure, located by a dotted path into the
// configuration ("providers.gateway.models[0].cost.input") and, when the load
// knows them, by the file and line the offending key sits on.
type Problem struct {
	Path    string
	File    string
	Line    int
	Message string
}

func (p Problem) String() string {
	var sb strings.Builder
	if p.File != "" {
		sb.WriteString(p.File)
		if p.Line > 0 {
			fmt.Fprintf(&sb, ":%d", p.Line)
		}
		sb.WriteString(": ")
	}
	if p.Path != "" {
		sb.WriteString(p.Path)
		sb.WriteString(": ")
	}
	sb.WriteString(p.Message)
	return sb.String()
}

// ValidationError aggregates every problem found in a configuration file.
// Cite reports all of them at once: the user fixes everything in one pass
// instead of discovering problems one CI run at a time.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "invalid cite configuration (%d problem%s):", len(e.Problems), plural(len(e.Problems)))
	for _, p := range e.Problems {
		sb.WriteString("\n  - ")
		sb.WriteString(p.String())
	}
	return sb.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// addf appends a problem at path.
func addf(probs *[]Problem, path, format string, args ...interface{}) {
	*probs = append(*probs, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}
