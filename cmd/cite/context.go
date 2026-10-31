package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/prcontext"
)

// The context pass reads the pull request description and posts the author
// one comment of questions about facts it leaves unexplained. It is its own
// comment, never the sticky one: `cite listen` rewrites the sticky comment and
// collapses its visible text. The pass never reaches the verdict, the check
// conclusion or the merge decision.

const contextMarker = "<!-- cite-context -->"

// Model prose is capped before it renders: a classifier asks one question per
// failed check, so more than a handful, or a paragraph each, is a model gone
// wrong and not something an author should have to read.
const (
	maxContextQuestions     = 8
	maxContextQuestionRunes = 600
)

// contextMarkerFor is the context-comment marker for this identity, derived the
// way stickyMarkerFor derives its own: the identity goes in before the closing,
// so no marker is a substring of another.
func contextMarkerFor(id string) string { return markerFor(contextMarker, id) }

// contextOutcome is what one pass produced. Err set means the pass failed and
// Verdict is empty. Questions are already cleaned for rendering.
type contextOutcome struct {
	Verdict   prcontext.Verdict
	Questions []string
	Err       string
}

func contextTimeout(cfg *config.Config) time.Duration {
	return cfg.Role(model.RoleContext).Timeout
}

// runContextPass classifies body once. Any failure comes back as Err and is
// never fatal: the review finishes without a context comment and the next run
// tries again.
func runContextPass(ctx context.Context, client model.Client, cfg *config.Config, requireParams bool, body string) contextOutcome {
	res, err := prcontext.Classify(ctx, client, body, prcontext.Options{
		Timeout:           contextTimeout(cfg),
		RequireParameters: cfg.RequireParameters || requireParams,
	})
	if err != nil {
		return contextOutcome{Err: err.Error()}
	}
	return contextOutcome{Verdict: res.Verdict, Questions: cleanContextQuestions(res.Questions)}
}

// applyContextOutcome records the pass on the run record. It writes the three
// context fields and nothing else: Verdict stays what the gate decided.
func applyContextOutcome(rec *model.RunRecord, o contextOutcome) {
	rec.ContextVerdict = string(o.Verdict)
	rec.ContextQuestions = o.Questions
	rec.ContextError = o.Err
}

// cleanContextQuestions makes classifier output fit for a comment. The
// questions are model prose derived from text the pull request author wrote, so
// they pass the sanitizer every rendered finding passes (URLs, link syntax,
// mentions, closing keywords, control characters), collapse to one line, lose
// every "<" so no question can forge an HTML-comment marker that a substring
// lookup would match, and are capped in number and length.
func cleanContextQuestions(qs []string) []string {
	var out []string
	for _, q := range qs {
		q = strings.Join(strings.Fields(model.SanitizeText(q)), " ")
		q = strings.ReplaceAll(q, "<", "&lt;")
		if r := []rune(q); len(r) > maxContextQuestionRunes {
			q = string(r[:maxContextQuestionRunes-1]) + "…"
		}
		if q == "" {
			continue
		}
		out = append(out, q)
		if len(out) == maxContextQuestions {
			break
		}
	}
	return out
}

func renderContextComment(marker string, questions []string) string {
	var sb strings.Builder
	sb.WriteString(marker)
	sb.WriteString("\n**Questions about this pull request's description**\n\n")
	sb.WriteString("The description leaves these facts unexplained:\n\n")
	for i, q := range questions {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, q)
	}
	sb.WriteString("\nCite reads the description again on its next review run, after a push or an `@cite review` request, and updates this comment. A reply here is for human readers: Cite does not read it.\n\n")
	sb.WriteString("> These questions were derived from the pull request description, which its author controls, by a model.\n")
	sb.WriteString(originTag())
	sb.WriteString("\n")
	return sb.String()
}

func resolvedContextComment(marker string) string {
	return marker + "\nThe description now gives the context Cite asked about earlier.\n"
}

// contextComments is the part of the GitHub client the pass uses.
type contextComments interface {
	FindIssueCommentWithNode(ctx context.Context, prNum int, marker string) (githubclient.IssueComment, bool, error)
	CreateIssueComment(ctx context.Context, prNum int, body string) error
	UpdateIssueComment(ctx context.Context, id int64, body string) error
	MinimizeNode(ctx context.Context, nodeID string) error
}

// syncContextComment brings the one marker-keyed comment in line with the
// verdict and says what it did, or would do with dryRun.
//
// MISSING_CONTEXT creates the comment, or edits it in place when the questions
// changed. COMPLETE with no comment posts nothing. COMPLETE over a comment that
// still asks questions minimizes it and then rewrites it to one line, in that
// order: a failed minimize leaves the questions standing and the next run tries
// both steps again, whereas rewriting first would leave a resolved-looking
// comment that no later run minimizes. Minimizing is applied and never
// removed, so a later body that regresses edits the collapsed comment and
// leaves it collapsed.
func syncContextComment(ctx context.Context, c contextComments, prNum int, marker string, verdict prcontext.Verdict, questions []string, dryRun bool) (string, error) {
	existing, found, err := c.FindIssueCommentWithNode(ctx, prNum, marker)
	if err != nil {
		return "", fmt.Errorf("looking up the context comment: %w", err)
	}
	switch verdict {
	case prcontext.MissingContext:
		want := renderContextComment(marker, questions)
		switch {
		case !found:
			if dryRun {
				return "would create the context comment", nil
			}
			return "created the context comment", c.CreateIssueComment(ctx, prNum, want)
		case existing.Body == want:
			return "context comment unchanged", nil
		default:
			if dryRun {
				return "would update the context comment", nil
			}
			return "updated the context comment", c.UpdateIssueComment(ctx, existing.ID, want)
		}
	case prcontext.Complete:
		resolved := resolvedContextComment(marker)
		if !found || existing.Body == resolved {
			return "no context comment to resolve", nil
		}
		if dryRun {
			return "would minimize and resolve the context comment", nil
		}
		if err := c.MinimizeNode(ctx, existing.NodeID); err != nil {
			return "", fmt.Errorf("minimizing the context comment: %w", err)
		}
		return "minimized and resolved the context comment", c.UpdateIssueComment(ctx, existing.ID, resolved)
	}
	return "", fmt.Errorf("unknown context verdict %q", verdict)
}

// newContextClient returns the client for the context role. With no
// roles.context.model it is the review's own client. A qualified reference
// ("provider/id") takes its provider's endpoint, key and headers, and a bare id
// takes the primary endpoint with that model, the same resolution a fallback
// leg gets.
func newContextClient(st *settings, cfg *config.Config, primary model.Client) (model.Client, error) {
	spec, ok := cfg.Roles[model.RoleContext]
	if !ok || spec.Model == "" {
		return primary, nil
	}
	leg, ok := legFor(cfg, st, spec.Model)
	if !ok {
		return nil, fmt.Errorf("roles.context.model %q names no declared provider", spec.Model)
	}
	key, err := leg.apiKey.Resolve()
	if err != nil {
		return nil, fmt.Errorf("roles.context.model %q: %w", spec.Model, err)
	}
	return &model.OpenAICompatClient{
		BaseURL: strings.TrimSuffix(leg.baseURL, "/"), APIKey: string(key), ExtraHeaders: leg.headers, Model: leg.modelID,
	}, nil
}

// logContextOutcome puts the answer in the Actions log, so a comment that fails
// to post still leaves it there.
func logContextOutcome(o contextOutcome) {
	switch {
	case o.Err != "":
		logToStderr("context pass failed, no context comment this run: %s", o.Err)
	case o.Verdict == prcontext.MissingContext:
		logToStderr("context: %s", o.Verdict)
		for i, q := range o.Questions {
			logToStderr("context question %d: %s", i+1, q)
		}
	case o.Verdict != "":
		logToStderr("context: %s", o.Verdict)
	}
}

// postContextComment applies a pass's outcome to the pull request. A pass that
// failed or did not run posts nothing, and a posting failure is logged, never
// returned: the context comment cannot change what the review concludes.
func postContextComment(ctx context.Context, c contextComments, prNum int, reviewerID string, o contextOutcome, dryRun bool) {
	if o.Verdict == "" {
		return
	}
	desc, err := syncContextComment(ctx, c, prNum, contextMarkerFor(reviewerID), o.Verdict, o.Questions, dryRun)
	if err != nil {
		logToStderr("context comment failed: %v", err)
		return
	}
	logToStderr("context: %s", desc)
}
