// Package prcontext asks one question of a pull request description: does it
// give a reader enough to understand what the change is about and what
// changed, without opening the diff. It reproduces the request that
// elecnix/pr-context-classifier sends, so the answer comes from the prompt that
// project ships and the reply parses the way its parser reads it.
//
// The package calls a model and nothing else. Whether to post, and where, is
// the caller's business, and the answer never reaches the gate.
package prcontext

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/model"
)

// Verdict is the classifier's two-valued answer.
type Verdict string

const (
	Complete       Verdict = "COMPLETE"
	MissingContext Verdict = "MISSING_CONTEXT"
)

// UserQuestion is the classifier's DEFAULT_USER_QUESTION (src/prompt.ts),
// verbatim. It is part of the measured input, so it is not reworded.
const UserQuestion = "Does this pull-request body give a reader complete context to understand what the PR is about and exactly what changed, without reading code or the diff?"

// EmptyBodyQuestion is the one question posted for an empty description. The
// classifier treats an empty body as a usage error and exits, so Cite answers
// it without a model call: an empty description is the strongest case for the
// feature.
const EmptyBodyQuestion = "The pull request description is empty. What is this change about, and what does it change?"

// classifierPrompt is prompt/classifier-prompt.md from
// elecnix/pr-context-classifier at c867c9c, the text inside its fence. That
// revision removed the special-calibration block (pr-context-classifier#2), so
// the prompt carries no measurement.
//
//go:embed classifier-prompt.txt
var classifierPrompt string

// SystemPrompt returns the system message, trimmed the way the classifier
// trims its fence.
func SystemPrompt() string { return strings.TrimSpace(classifierPrompt) }

// Result is a parsed classifier reply.
type Result struct {
	Verdict   Verdict
	Questions []string
}

// Options carries the two things the caller decides.
type Options struct {
	// Timeout bounds the call. Zero means no bound beyond the context's.
	Timeout time.Duration
	// RequireParameters is the repository's top-level setting, passed on as
	// the other calls pass it.
	RequireParameters bool
}

// Classify asks the model about body. It makes at most one call and never
// retries: a failed call is retried by the next review run.
func Classify(ctx context.Context, client model.Client, body string, opts Options) (Result, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Result{Verdict: MissingContext, Questions: []string{EmptyBodyQuestion}}, nil
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	// Temperature 0 and no max_tokens, reasoning_effort, response format or
	// session id: the classifier's request has model, messages and temperature
	// and nothing else.
	resp, err := client.Complete(ctx, model.CompletionRequest{
		System:            SystemPrompt(),
		User:              UserQuestion + "\n\nPR BODY:\n" + body,
		RequireParameters: opts.RequireParameters,
	})
	if err != nil {
		return Result{}, err
	}
	return Parse(resp.Text)
}

var (
	verdictRe    = regexp.MustCompile(`(?i)\bVERDICT:\s*(COMPLETE|MISSING_CONTEXT)\b`)
	headingRe    = regexp.MustCompile(`(?i)^CLARIFYING_QUESTIONS:`)
	bulletRe     = regexp.MustCompile(`^[-*] `)
	labelRe      = regexp.MustCompile(`(?i)^[A-Z_]+:`)
	errNoVerdict = errors.New("the reply carries no VERDICT: COMPLETE or MISSING_CONTEXT line")
)

// Parse reads the labeled text the classifier returns. It follows the
// classifier's parser (src/classify.ts) except where that parser defaults: a
// reply with no verdict reads as COMPLETE there, and here it is an error,
// because a silent COMPLETE would collapse questions the author has not
// answered. A MISSING_CONTEXT reply with no question is an error for the same
// reason: there is nothing to post.
func Parse(text string) (Result, error) {
	m := verdictRe.FindStringSubmatch(text)
	if m == nil {
		return Result{}, errNoVerdict
	}
	res := Result{Verdict: Verdict(strings.ToUpper(m[1]))}
	inList := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if headingRe.MatchString(t) {
			inList = true
			continue
		}
		if !inList || t == "" {
			continue
		}
		switch {
		case bulletRe.MatchString(t):
			res.Questions = append(res.Questions, strings.TrimSpace(t[2:]))
		case len(res.Questions) > 0 && !labelRe.MatchString(t):
			res.Questions = append(res.Questions, t)
		}
	}
	if res.Verdict == Complete {
		kept := res.Questions[:0]
		for _, q := range res.Questions {
			if q != "none" && q != "" {
				kept = append(kept, q)
			}
		}
		res.Questions = kept
		return res, nil
	}
	if len(res.Questions) == 0 {
		return Result{}, fmt.Errorf("the reply says %s and lists no question", MissingContext)
	}
	return res, nil
}
