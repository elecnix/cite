package main

// Prose reply classification (PLAN.md §14): "A prose reply is classified.
// 'Is this reply rejecting the finding?' is exactly the size of task a small
// model does reliably. Never make a human speak robot to be heard."
//
// One cheap model call per unclassified human reply on a Cite thread. A
// rejecting verdict records a dismissal in the ledger — subject to
// CanDismiss's authorisation check against GitHub-reported
// author_association (I2), never anything in the comment body.
// Classification is non-fatal: a provider failure warns and skips, because
// this step must never fail a run that already has its verdicts.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

// ReplyVerdicts are the cached classification outcomes, persisted inside the
// sticky comment's state blob keyed by fingerprint, so each reply is
// classified at most once across runs.
const (
	VerdictRejecting    = "rejecting"
	VerdictNotRejecting = "not-rejecting"
)

// replyPrompt builds the one-question prompt. It is deliberately stripped of
// any judge framing — no severity, no "a reviewer found", no stakes — because
// models asked to argue either way produce verdicts carrying no information.
func replyPrompt(findingTitle, replyAuthor, replyBody string) string {
	var b strings.Builder
	b.WriteString("A human replied to an automated code-review comment.\n\n")
	b.WriteString("Finding title: " + findingTitle + "\n")
	b.WriteString("Reply by @" + replyAuthor + ":\n<<<\n" + replyBody + "\n>>>\n\n")
	b.WriteString("Is this reply rejecting the finding? Answer with JSON only: ")
	b.WriteString(`{"rejecting": true}` + " if the human is saying the finding is wrong or should not have been raised, ")
	b.WriteString(`{"rejecting": false}` + " otherwise (questions, thanks, agreement, discussion). No other keys, no prose.")
	return b.String()
}

// parseReplyVerdict parses the model's answer strictly. Tolerates one fenced
// code block and surrounding whitespace; anything else is an error, and an
// error means skip — never guess a dismissal into existence.
func parseReplyVerdict(raw string) (rejecting bool, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false, false
	}
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	if i := strings.LastIndex(s, "}"); i >= 0 {
		s = s[:i+1]
	}
	var v struct {
		Rejecting *bool `json:"rejecting"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil || v.Rejecting == nil {
		return false, false
	}
	return *v.Rejecting, true
}

// classifyReply makes the one bounded call. Temperature 0: this is a
// classification, not a generation.
func classifyReply(ctx context.Context, mc model.Client, findingTitle, replyAuthor, replyBody string) (bool, error) {
	resp, err := mc.Complete(ctx, model.CompletionRequest{
		System:          "You classify one human reply on an automated code-review comment. You output JSON only.",
		User:            replyPrompt(findingTitle, replyAuthor, replyBody),
		MaxOutputTokens: 200,
		Temperature:     0,
	})
	if err != nil {
		return false, err
	}
	rejecting, ok := parseReplyVerdict(resp.Text)
	if !ok {
		return false, fmt.Errorf("reply classification: unparseable model output")
	}
	return rejecting, nil
}

// replyDismissalAllowed applies §12's dispute protocol to a classified
// reply: authorisation from GitHub-reported association only, never the PR
// author, never a first-timer.
func replyDismissalAllowed(author, association string, isPRAuthor bool) error {
	return publisher.CanDismiss(author, association, isPRAuthor)
}
