package reviewer

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// The verifier pass (§8): one call per finding that survived validation,
// asking a fresh model turn to trace the claim through the code and refute
// it if a line it can quote prevents it. The review call is an advocate for
// its findings; this call is asked for a trace, not an opinion.
//
// Across the user's repositories about half of the findings people replied
// to were wrong, and the commonest shapes were checkable inside the file: a
// guard read the wrong way round, a claim its own quoted evidence
// contradicts ("oneLine is not applied to the category", quoting
// oneLine(t.Category)), a branch called swapped that is not. A refutation
// only counts when its quoted line is really in the file or in the related
// code it was shown, so a verifier that invents a guard cannot drop a real
// finding: it answers needs_context instead.

//go:embed prompts/verify.system.md
var embeddedVerifyPrompt string

const (

	toolNameVerdict        = "report_verdict"
	toolVerdictDescription = "Report whether the claim holds. The argument must match this function's parameter schema exactly."

	// verifyMaxTokens bounds one verification: a trace and a verdict, never
	// a review. A role pinned lower keeps its own number.
	verifyMaxTokens = 16384
)

// VerifyInput is everything a verifier sees about one finding.
type VerifyInput struct {
	Path    string
	Finding model.Finding
	Lines   []string     // post-change file
	Added   map[int]bool // its changed lines
	Related []scope.RelatedSnippet
}

type verdictAnswer struct {
	Verdict       string `json:"verdict"`
	RefutingPath  string `json:"refuting_path"`
	RefutingLine  int    `json:"refuting_line"`
	RefutingQuote string `json:"refuting_quote"`
	Reason        string `json:"reason"`
}

func verdictSchema() json.RawMessage {
	return canonicalJSON(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"verdict", "refuting_path", "refuting_line", "refuting_quote", "reason"},
		"properties": map[string]any{
			"verdict":        map[string]any{"type": "string", "enum": []any{"supported", "unsupported", "needs_context"}},
			"refuting_path":  map[string]any{"type": "string"},
			"refuting_line":  map[string]any{"type": "integer"},
			"refuting_quote": map[string]any{"type": "string"},
			"reason":         map[string]any{"type": "string"},
		},
	})
}

// modelVerifier is the production DiscriminativeVerifier: the reviewer's
// own client, with its retry, failover and call accounting.
type modelVerifier struct{ r *Reviewer }

// Verify implements DiscriminativeVerifier.
func (v *modelVerifier) Verify(in VerifyInput) (VerifierVerdict, string, error) {
	r := v.r
	timeout, _, maxTokens := r.roleSettings(model.RoleReview, 0, config.DefaultReviewConcurrency, defaultReviewMaxTokens)
	if maxTokens <= 0 || maxTokens > verifyMaxTokens {
		maxTokens = verifyMaxTokens
	}
	req := model.CompletionRequest{
		System:            embeddedVerifyPrompt,
		User:              renderVerifyRequest(in),
		MaxOutputTokens:   maxTokens,
		Temperature:       pinnedTemperature,
		RequireParameters: requireParameters(r.o.Cfg) || r.o.RequireParameters,
		ReasoningEffort:   r.o.ReasoningEffort,
	}
	r.applyStructuredOutput(&req, verdictSchema(), toolNameVerdict, toolVerdictDescription)
	resp, err := r.completeWithRetry(r.runCtx, unitVerify, req, timeout)
	if err != nil {
		return VerifierError, "", err
	}
	var a verdictAnswer
	if err := json.Unmarshal([]byte(strings.TrimSpace(stripFence(resp.Text))), &a); err != nil {
		return VerifierError, "", fmt.Errorf("verdict did not parse: %w", err)
	}
	switch a.Verdict {
	case "supported":
		return VerifierSupported, a.Reason, nil
	case "needs_context":
		return VerifierNeedsContextNotProvided, a.Reason, nil
	case "unsupported":
		if !refutationQuoted(in, a) {
			// A refutation whose line is not where it says cannot drop a
			// finding: that would be the verifier hallucinating instead of
			// the reviewer.
			return VerifierNeedsContextNotProvided, "refutation quote not found: " + a.Reason, nil
		}
		return VerifierUnsupported, fmt.Sprintf("%s (line %d: %s)", a.Reason, a.RefutingLine, strings.TrimSpace(a.RefutingQuote)), nil
	}
	return VerifierError, "", errors.New("verdict outside the closed set: " + a.Verdict)
}

// refutationQuoted reports whether the answer's refuting quote is the text
// of the line it names, in the file under review or in a related excerpt.
// Whitespace at either end is forgiven; anything else is not.
func refutationQuoted(in VerifyInput, a verdictAnswer) bool {
	q := strings.TrimSpace(a.RefutingQuote)
	if q == "" || a.RefutingLine <= 0 {
		return false
	}
	if a.RefutingPath == "" || a.RefutingPath == in.Path {
		return a.RefutingLine <= len(in.Lines) && strings.Contains(in.Lines[a.RefutingLine-1], q)
	}
	for _, s := range in.Related {
		if s.Path != a.RefutingPath {
			continue
		}
		i := a.RefutingLine - s.StartLine
		if i >= 0 && i < len(s.Lines) && strings.Contains(s.Lines[i], q) {
			return true
		}
	}
	return false
}

// renderVerifyRequest lays out the file, the related excerpts and the claim
// in the shapes the review call uses, so the verifier reads the same bytes
// the reviewer did.
func renderVerifyRequest(in VerifyInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<file_under_review path=%q>\n", in.Path)
	for i, l := range in.Lines {
		marker := " "
		if in.Added[i+1] {
			marker = "+"
		}
		fmt.Fprintf(&b, "%04d %s|%s\n", i+1, marker, l)
	}
	b.WriteString("</file_under_review>\n\n")
	if len(in.Related) > 0 {
		env := scope.BuildEnvelope(nil, "", "", &scope.EnvelopeFile{Path: in.Path, Related: in.Related})
		if i := strings.Index(env, "<related_code"); i >= 0 {
			b.WriteString(strings.TrimRight(env[i:], "\n"))
			b.WriteString("\n\n")
		}
	}
	f := in.Finding
	b.WriteString("<claim trust=\"untrusted\">\n")
	fmt.Fprintf(&b, "| category: %s\n| confidence: %s\n| anchored lines: %d-%d\n", oneLineText(string(f.Category)), oneLineText(string(f.Confidence)), f.Anchor.StartLine, f.Anchor.EndLine)
	fmt.Fprintf(&b, "| title: %s\n| body: %s\n| impact: %s\n", oneLineText(f.Title), oneLineText(f.Body), oneLineText(f.Impact))
	for _, e := range f.Evidence {
		fmt.Fprintf(&b, "| evidence line %d: %s\n", e.Line, oneLineText(e.Quote))
	}
	b.WriteString("</claim>")
	return b.String()
}

func oneLineText(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// stripFence removes one markdown fence a model may wrap JSON in.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	return s
}
