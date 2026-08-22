package main

// Subcommands that ingest the signals people already emit (§14):
//
//	listen  — the one keyword: "@cite review" on an issue comment re-runs
//	          the review, authorised from authenticated API metadata.
//	signals — walk review comments for 👎 reactions on Cite findings and
//	          record dismissals in the sticky-comment ledger; disambiguate
//	          resolved threads mechanically: quoted span changed since →
//	          accepted-and-fixed, span byte-identical → dismissed (§14).
//
// Both are read-mostly reconcilers: they mutate only the ledger blob inside
// Cite's own sticky comment and never touch reviews or check runs.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/metrics"
	"github.com/elecnix/cite/internal/publisher"
)

func init() {
	registerCommand("listen", runListen)
	registerCommand("signals", runSignals)
}

// --- cite listen -------------------------------------------------------------

// allowedRequestAssociations are the author_association values that may
// trigger a re-run. FIRST_TIME_CONTRIBUTOR / FIRST_TIMER / NONE are rejected
// — except the pull request author, who MAY request a re-run of their own
// pull request: a re-run is not a dismissal, so §12's "the author can never
// self-dismiss" rule does not apply.
var allowedRequestAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
	"CONTRIBUTOR":  true,
}

// keyword is the exactly-one-keyword command (§14). Matched trimmed and
// case-insensitively.
const keyword = "@cite review"

func runListen(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	prSpec := fs.String("pr", "", "pull request as owner/repo#N")
	commentID := fs.Int64("comment-id", 0, "id of the issue comment that triggered this run")
	dryRun := fs.Bool("dry-run", false, "print the decision, do not run the review")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *commentID == 0 || *prSpec == "" {
		fs.Usage()
		return fmt.Errorf("listen: --pr and --comment-id are required")
	}
	owner, repo, num, err := parsePRSpec(*prSpec)
	if err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required for listen")
	}
	c := githubclient.New(token, "", nil).WithRepo(owner, repo)
	ctx := context.Background()

	body, author, assoc, err := c.IssueComment(ctx, num, *commentID)
	if err != nil {
		return fmt.Errorf("fetching comment %d: %w", *commentID, err)
	}
	trimmed := strings.TrimSpace(body)
	if !strings.EqualFold(trimmed, keyword) {
		return fmt.Errorf("comment %d is not %q (got %.40q): nothing to do", *commentID, keyword, trimmed)
	}

	// Authorisation comes ONLY from GitHub-reported metadata (I2), never
	// from anything in the comment body.
	if !allowedRequestAssociations[assoc] {
		// The PR author MAY request a re-run even with a weak association:
		// a re-run is not a dismissal.
		pr, perr := c.GetPR(ctx, num)
		if perr != nil {
			return fmt.Errorf("checking authorisation: %w", perr)
		}
		if author != pr.AuthorLogin {
			fmt.Fprintf(os.Stderr, "cite listen: unauthorised: %q has author_association %s; "+
				"only OWNER/MEMBER/COLLABORATOR/CONTRIBUTOR (or the PR author) may request a re-run\n",
				author, assoc)
			return fmt.Errorf("unauthorised @%s (%s)", author, assoc)
		}
	}

	if *dryRun {
		fmt.Printf("dry-run: would trigger a fresh review of %s#%d on behalf of @%s (%s)\n",
			owner+"/"+repo, num, author, assoc)
		return nil
	}
	fmt.Printf("@cite review by @%s (%s): triggering a fresh review of %s#%d\n",
		author, assoc, owner+"/"+repo, num)

	// runReview is an unexported sibling; exec-ing this binary keeps the
	// re-run on the exact code path CI uses, including exit status.
	cmd := exec.Command(os.Args[0], "review", "--pr", *prSpec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// --- cite signals ------------------------------------------------------------

func runSignals(args []string) error {
	fs := flag.NewFlagSet("signals", flag.ContinueOnError)
	prSpec := fs.String("pr", "", "pull request as owner/repo#N")
	dryRun := fs.Bool("dry-run", false, "report what would be recorded, persist nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *prSpec == "" {
		fs.Usage()
		return fmt.Errorf("signals: --pr is required")
	}
	owner, repo, num, err := parsePRSpec(*prSpec)
	if err != nil {
		return err
	}
	repoFull := owner + "/" + repo
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required for signals")
	}
	c := githubclient.New(token, "", nil).WithRepo(owner, repo)
	ctx := context.Background()

	// The head SHA for span-change comparisons comes only from
	// authenticated API metadata (I2), never from thread or comment text.
	pr, err := c.GetPR(ctx, num)
	if err != nil {
		return fmt.Errorf("fetching PR #%d: %w", num, err)
	}
	headSHA := pr.HeadSHA

	comments, err := c.ReviewComments(ctx, num)
	if err != nil {
		return err
	}

	prevState := readSticky(ctx, c, num)
	ledger := publisher.DismissalLedger{}
	if prevState.Ledger != "" {
		if l, err := publisher.UnmarshalBlob(prevState.Ledger); err == nil {
			ledger = l
		} else {
			fmt.Fprintln(os.Stderr, "warning: ledger corrupt; rebuilding published marks from live threads")
		}
	}

	now := time.Now()
	dismissals := 0
	for _, cm := range comments {
		fp, _, ok := parseCiteCommentBody(cm.Body)
		if !ok {
			continue // not a Cite-authored finding comment
		}
		counts, err := c.CommentReactions(ctx, cm.ID)
		if err != nil {
			return fmt.Errorf("reactions on comment %d: %w", cm.ID, err)
		}
		if counts["-1"] == 0 {
			continue
		}
		// Log every reactor's identity, then record one dismissal per
		// downvoted fingerprint. Association is recorded UNKNOWN: the
		// reactions list does not carry author_association, and guessing
		// MEMBER would overstate authority. CanDismiss rules (which reject
		// only FIRST_TIME_*) are enforced at read time against whatever
		// identity evidence exists then; ledger.Add itself validates
		// nothing, by design.
		reactions, _ := c.CommentReactionList(ctx, cm.ID)
		var reactors []string
		for _, r := range reactions {
			if r.Content == "-1" && r.UserLogin != "" {
				reactors = append(reactors, r.UserLogin)
			}
		}
		dismissals++
		fmt.Printf("👎 on %s (comment %d) by %s → dismissal recorded\n",
			fp, cm.ID, strings.Join(reactors, ", "))
		ledger.Add(fp, repoFull, strings.Join(reactors, ","), "UNKNOWN", now)
	}

	threads, err := c.ListReviewThreads(ctx, num)
	if err != nil {
		return err
	}
	citeThreads, resolvedCount, accepted, byResolution := 0, 0, 0, 0
	for _, t := range threads {
		if len(t.Comments) == 0 {
			continue
		}
		fp, tf, ok := parseCiteCommentBody(t.Comments[0].Body)
		if !ok {
			continue
		}
		citeThreads++
		if !t.IsResolved {
			continue
		}
		resolvedCount++
		// §14: a human resolving a thread means handled, disambiguated
		// mechanically. If the quoted span changed in a later push it was
		// accepted-and-fixed; if it is still identical, the human read it
		// and said no — a dismissal. No evidence data means unverifiable:
		// record neither.
		if tf == nil || len(tf.Evidence) == 0 {
			continue
		}
		content, _, err := c.GetFileContent(ctx, headSHA, tf.Path)
		if err != nil {
			return fmt.Errorf("fetching %s at head for resolution check: %w", tf.Path, err)
		}
		if metrics.SpanChanged(tf.Evidence, content) {
			accepted++
			ledger.AddAcceptedFixed(fp, repoFull, now)
			fmt.Printf("resolved %s with the span changed → accepted-and-fixed recorded\n", fp)
		} else {
			byResolution++
			ledger.Add(fp, repoFull, "thread-resolution", "UNKNOWN", now)
			fmt.Printf("resolved %s with the span identical → dismissal recorded\n", fp)
		}
	}

	fmt.Printf("%s#%d: %d Cite thread(s), %d resolved / %d open; "+
		"%d 👎 dismissal(s), %d accepted-and-fixed, %d dismissed-by-resolution\n",
		repoFull, num, citeThreads, resolvedCount, citeThreads-resolvedCount, dismissals, accepted, byResolution)

	if *dryRun {
		fmt.Println("dry-run: ledger not persisted")
		return nil
	}
	if dismissals > 0 || accepted > 0 || byResolution > 0 {
		if err := updateStickyLedger(ctx, c, num, ledger); err != nil {
			return err
		}
		fmt.Printf("ledger persisted: %d entr(y/ies)\n", len(ledger.Entries))
	}
	return nil
}

// updateStickyLedger rewrites ONLY the Ledger field of the sticky comment's
// state blob, preserving BlobSHAs and Findings written by the last review
// run. writeSticky cannot be reused here: it rebuilds the whole state from
// a RunRecord, which signals does not have.
func updateStickyLedger(ctx context.Context, c *githubclient.Client, prNum int, ledger publisher.DismissalLedger) error {
	blob, err := ledger.MarshalBlob()
	if err != nil {
		return fmt.Errorf("marshalling ledger: %w", err)
	}
	st := readSticky(ctx, c, prNum)
	st.Ledger = blob
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	body := stickyMarker + "\n" +
		"<!-- cite-state=" + base64.StdEncoding.EncodeToString(raw) + " -->\n" +
		fmt.Sprintf("Cite state for PR #%d. Ledger entries: %d.\n", prNum, len(ledger.Entries))
	return c.UpsertIssueComment(ctx, prNum, stickyMarker, body)
}
