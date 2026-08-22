package main

// cite metrics fix-or-argue — the weekly proxy (PLAN.md §15), fully
// derivable from the GitHub API:
//
//	a published finding counts as actioned when a later head SHA changed
//	the normalised content of its anchored span ("fixed"), or when a human
// posted a reply over 40 characters that is not a dismissal ("argued").
//
// It reads Cite's own posted review comments for the finding set, the live
// threads for replies, and the head file contents for span comparison. It
// posts nothing anywhere.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/metrics"
)

func init() {
	registerCommand("metrics", runMetrics)
}

func runMetrics(args []string) error {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	prSpec := fs.String("pr", "", "pull request as owner/repo#N")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || fs.Arg(0) != "fix-or-argue" {
		fmt.Fprintln(os.Stderr, "usage: cite metrics fix-or-argue --pr owner/repo#N")
		return fmt.Errorf("metrics: exactly one instrument is supported: fix-or-argue")
	}
	if *prSpec == "" {
		fs.Usage()
		return fmt.Errorf("metrics: --pr is required")
	}
	owner, repo, num, err := parsePRSpec(*prSpec)
	if err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required for metrics")
	}
	c := githubclient.New(token, "", nil).WithRepo(owner, repo)
	ctx := context.Background()

	pr, err := c.GetPR(ctx, num)
	if err != nil {
		return fmt.Errorf("fetching PR #%d: %w", num, err)
	}

	// The finding set: Cite's own review comments carry the fingerprint and
	// the evidence they were published under. No RunRecord needed — the
	// metric must work long after the run artifact expired.
	findings := map[string]metrics.Finding{}
	var order []string
	comments, err := c.ReviewComments(ctx, num)
	if err != nil {
		return err
	}
	for _, cm := range comments {
		fp, tf, ok := parseCiteCommentBody(cm.Body)
		if !ok || tf == nil || len(tf.Evidence) == 0 {
			continue
		}
		if _, dup := findings[fp]; !dup {
			order = append(order, fp)
		}
		findings[fp] = metrics.Finding{Fingerprint: fp, Path: tf.Path, Evidence: tf.Evidence}
	}
	if len(findings) == 0 {
		fmt.Printf("%s#%d: no published findings; nothing to measure\n", owner+"/"+repo, num)
		return nil
	}

	// Replies: every non-first comment on a Cite thread, authored by someone
	// other than Cite.
	replies := map[string][]metrics.Reply{}
	botAuthor := func(login string) bool { return login == "" || strings.HasSuffix(login, "[bot]") }
	threads, err := c.ListReviewThreads(ctx, num)
	if err != nil {
		return fmt.Errorf("fetching threads: %w", err)
	}
	for _, t := range threads {
		if len(t.Comments) < 2 {
			continue
		}
		fp, _, ok := parseCiteCommentBody(t.Comments[0].Body)
		if !ok {
			continue
		}
		for _, rc := range t.Comments[1:] {
			if botAuthor(rc.AuthorLogin) {
				continue
			}
			replies[fp] = append(replies[fp], metrics.Reply{Author: rc.AuthorLogin, Body: rc.Body})
		}
	}

	// Head content per distinct path — one GET per file, never a tree dump.
	heads := map[string][]byte{}
	for _, f := range findings {
		if _, seen := heads[f.Path]; seen {
			continue
		}
		content, found, err := c.GetFileContent(ctx, pr.HeadSHA, f.Path)
		heads[f.Path] = content // absent ⇒ SpanChanged reports gone
		if err != nil && found {
			return fmt.Errorf("fetching %s at head: %w", f.Path, err)
		}
	}

	in := make([]metrics.Finding, 0, len(order))
	for _, fp := range order {
		in = append(in, findings[fp])
	}
	rep := metrics.Evaluate(in, heads, replies)

	fmt.Printf("fix_or_argue for %s#%d at head %.12s\n", owner+"/"+repo, num, pr.HeadSHA)
	fmt.Printf("published_findings: %d\n", rep.Published)
	fmt.Printf("fixed:  %d\n", rep.Fixed)
	fmt.Printf("argued: %d\n", rep.Argued)
	fmt.Printf("actioned: %d\n", rep.Actioned)
	fmt.Printf("fix_or_argue: %.1f%%\n", 100*rep.Rate())
	fmt.Println("(weekly proxy; gates nothing until validated against a gold campaign, r ≥ 0.5)")
	return nil
}
