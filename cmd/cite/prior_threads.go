package main

import (
	"strings"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/scope"
)

// priorReplyMaxRunes caps one reply in the review prompt. Every review call
// of the file repeats the reply, and its author is untrusted (issue #168).
const priorReplyMaxRunes = 2000

const priorReplyTruncated = " [reply truncated]"

// priorThreadsFrom turns the pull request's review threads into the prior
// threads of each file (issue #168). Only threads whose first comment is a
// Cite finding count. Replies by bots are left out, Cite's own included.
func priorThreadsFrom(threads []githubclient.Thread) map[string][]scope.PriorThread {
	out := map[string][]scope.PriorThread{}
	for _, gt := range threads {
		if len(gt.Comments) == 0 {
			continue
		}
		first := gt.Comments[0]
		_, tf, ok := parseCiteCommentBody(first.Body)
		if !ok {
			continue
		}
		pt := scope.PriorThread{
			ID:        first.ID,
			Category:  string(tf.Category),
			Title:     tf.Title,
			StartLine: first.StartLine,
			EndLine:   first.Line,
			Resolved:  gt.IsResolved,
		}
		if pt.StartLine == 0 {
			pt.StartLine = pt.EndLine
		}
		for _, rc := range gt.Comments[1:] {
			if rc.AuthorLogin == "" || strings.HasSuffix(rc.AuthorLogin, "[bot]") {
				continue
			}
			pt.Replies = append(pt.Replies, scope.PriorReply{Author: rc.AuthorLogin, Body: capReply(rc.Body)})
		}
		out[first.Path] = append(out[first.Path], pt)
	}
	return out
}

func capReply(s string) string {
	r := []rune(s)
	if len(r) <= priorReplyMaxRunes {
		return s
	}
	return string(r[:priorReplyMaxRunes]) + priorReplyTruncated
}
