package main

// cite bypass — the break-glass handler (PLAN §11).
//
// Fail-closed with an administrator bypass is how required checks get
// deleted: the administrator is asked five times in an hour and on the
// sixth removes the check. So the bypass is self-service (a label any
// author can apply), loud (the check summary says BYPASSED in caps), and
// enumerable (every use appends one line to the bypass log).
//
//	cite bypass --pr owner/repo#N --run-url <actions run url> [--label cite-bypass] [--dry-run]
//
// Trust invariant (§12): the label's presence is re-verified against the
// authenticated API. The workflow trigger (github.event.label.name) and
// the --label flag are attacker-writable channels; trust is never derived
// from them.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/model"
)

func init() {
	registerCommand("bypass", runBypass)
}

// bypassLogMarker delimits the sticky bypass-log comment. "Every pull
// request merged unreviewed on this date" is a one-line query over it.
const bypassLogMarker = "<!-- cite-bypass-log -->"

func runBypass(args []string) error {
	fs := flag.NewFlagSet("bypass", flag.ContinueOnError)
	prSpec := fs.String("pr", "", "pull request as owner/repo#N")
	label := fs.String("label", gate.BypassLabel, "bypass label to verify")
	runURL := fs.String("run-url", "", "URL of the workflow run applying the bypass")
	dryRun := fs.Bool("dry-run", false, "print what would happen, post nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *prSpec == "" {
		fs.Usage()
		return fmt.Errorf("bypass: --pr owner/repo#N is required")
	}
	owner, repo, num, err := parsePRSpec(*prSpec)
	if err != nil {
		return err
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required for bypass")
	}
	c := githubclient.New(token, "", nil).WithRepo(owner, repo)
	ctx := context.Background()

	pr, err := c.GetPR(ctx, num)
	if err != nil {
		return err
	}

	// Verify the label against the API. Never trust the flag or the event
	// payload: an attacker can write both, but not the authenticated label
	// list without repo write access.
	labels, err := c.PRLabels(ctx, num)
	if err != nil {
		return fmt.Errorf("listing labels: %w", err)
	}
	labeled := false
	for _, l := range labels {
		if l == *label {
			labeled = true
			break
		}
	}
	if !labeled {
		return fmt.Errorf("bypass label %q is not present on %s#%d (verified via API)", *label, owner+"/"+repo, num)
	}

	summary := gate.BypassSummary(model.VerdictCouldNotEvaluate, pr.AuthorLogin, *runURL)

	// The bypass concludes the pending Cite check on the head SHA — the
	// same check that owns the required-status slot — as success, loudly.
	checkID, found, err := c.PendingCiteCheck(ctx, pr.HeadSHA)
	if err != nil {
		return fmt.Errorf("finding pending cite check: %w", err)
	}

	if *dryRun {
		if found {
			fmt.Printf("dry-run: would conclude check run %d as success: Cite bypassed\n%s\n", checkID, summary)
		} else {
			fmt.Println("dry-run: no pending cite check on head; nothing to conclude")
		}
		fmt.Printf("dry-run: would append to bypass log: %s\n", bypassLogLine(num, pr.AuthorLogin, *runURL))
		return nil
	}

	if found {
		if err := c.ConcludeCheckRun(ctx, checkID, "success", "Cite bypassed", summary); err != nil {
			return fmt.Errorf("concluding bypass check run: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: no pending cite check on head %s; nothing concluded\n", pr.HeadSHA)
	}

	// Enumerable log: one line per use, appended to the sticky comment.
	if err := appendBypassLog(ctx, c, num, bypassLogLine(num, pr.AuthorLogin, *runURL)); err != nil {
		return err
	}

	fmt.Printf("bypass applied: %s#%d label=%s\n%s\n", owner+"/"+repo, num, *label, summary)
	if found {
		fmt.Printf("check run %d concluded success: Cite bypassed\n", checkID)
	}
	fmt.Printf("bypass log updated (marker %s)\n", bypassLogMarker)
	return nil
}

// bypassLogLine renders one enumerable entry. Format:
// YYYY-MM-DDTHH:MMZ pr=#N label-applied-by=<login> run=<url>
func bypassLogLine(num int, login, runURL string) string {
	ts := time.Now().UTC().Format("2006-01-02T15:04Z")
	return fmt.Sprintf("%s pr=#%d label-applied-by=%s run=%s", ts, num, login, runURL)
}

// appendBypassLog reads the sticky bypass-log comment, appends one line,
// and upserts it back.
func appendBypassLog(ctx context.Context, c *githubclient.Client, num int, line string) error {
	_, body, found, err := c.FindIssueComment(ctx, num, bypassLogMarker)
	if err != nil {
		return fmt.Errorf("finding bypass log comment: %w", err)
	}
	var newBody string
	if found {
		newBody = body + "\n" + line + "\n"
	} else {
		newBody = "## Cite bypass log\n\n" + bypassLogMarker + "\n\n" + line + "\n"
	}
	if err := c.UpsertIssueComment(ctx, num, bypassLogMarker, newBody); err != nil {
		return fmt.Errorf("upserting bypass log comment: %w", err)
	}
	return nil
}
