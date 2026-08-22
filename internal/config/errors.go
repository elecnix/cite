package config

import (
	"fmt"
	"strings"
)

// Problem is one validation failure, located by a dotted path into the
// configuration ("providers.gateway.models[0].cost.input").
type Problem struct {
	Path    string
	Message string
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
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
