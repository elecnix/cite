package main

// cite re-review — scheduled re-review of bypassed merge commits (PLAN §11).
//
// The break-glass bypass buys time, not amnesty: a label concludes the Cite
// check immediately, and every use appends one line to the bypass log. This
// command is the other half of the bargain — a scheduled job that walks the
// pull requests merged unreviewed in the look-back window and runs the
// standard review pipeline over them again, offline of any posting surface.
//
//	cite re-review --repo owner/name [--since 720h] [--dry-run]
//
// For each recently merged pull request carrying a bypass-log sticky comment
// (marker <!-- cite-bypass-log -->), entries within the window are picked up;
// each such PR is fetched via the API (no checkout — §12 I1), reviewed by the
// same reviewer.New + gate.Decide pipeline as `cite review --pr`, and every
// blocking finding becomes ONE issue titled
//
//	[cite] escaped finding on bypassed merge <short-sha> (#N)
//
// so the escape stays countable and accountable after the fact.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/reviewer"
	"github.com/elecnix/cite/internal/scope"
)

func init() {
	registerCommand("re-review", runReReview)
}

// rereviewLogMarker is the sticky-comment marker `cite bypass` writes its
// enumerable log into. Declared here (not imported from bypass.go) so this
// command stands alone.
const rereviewBypassLogMarker = "<!-- cite-bypass-log -->"

// bypassLineRe matches one bypass-log entry:
// YYYY-MM-DDTHH:MMZ pr=#N label-applied-by=<login> run=<url>
var bypassLineRe = regexp.MustCompile(
	`^(\S+)\s+pr=#(\d+)\s+label-applied-by=(\S+)\s+run=(\S+)\s*$`)

type bypassEntry struct {
	At        time.Time
	PR        int
	AppliedBy string
	RunURL    string
}

func runReReview(args []string) error {
	fs := flag.NewFlagSet("re-review", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "repository as owner/name")
	since := fs.Duration("since", 24*time.Hour, "look-back window for merges and bypass-log entries (e.g. 720h)")
	dryRun := fs.Bool("dry-run", false, "print findings instead of creating issues")
	cfgPath := fs.String("config", ".github/cite.yml", "config file (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repoFlag == "" {
		fs.Usage()
		return fmt.Errorf("re-review: --repo owner/name is required")
	}
	parts := strings.Split(*repoFlag, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("re-review: expected owner/name, got %q", *repoFlag)
	}
	owner, repo := parts[0], parts[1]

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required for re-review")
	}
	cfg := loadConfig(*cfgPath)
	c := githubclient.New(token, "", nil).WithRepo(owner, repo)
	ctx := context.Background()
	cutoff := time.Now().Add(-*since)

	merged, err := c.ListRecentlyMerged(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("listing merged pull requests: %w", err)
	}

	var reviewed, filed, failed int
	for _, mp := range merged {
		_, body, found, err := c.FindIssueComment(ctx, mp.Number, rereviewBypassLogMarker)
		if err != nil {
			logToStderr("warning: reading comments on #%d: %v", mp.Number, err)
			failed++
			continue
		}
		if !found {
			continue // merged without a bypass — normal review path
		}
		entries := parseBypassLog(body, cutoff)
		if len(entries) == 0 {
			continue // bypass log exists but nothing inside the window
		}
		findings, verdict, reason, err := rereviewMerged(ctx, c, cfg, owner, repo, mp)
		if err != nil {
			logToStderr("warning: re-reviewing #%d failed: %v", mp.Number, err)
			failed++
			continue
		}
		reviewed++
		fmt.Printf("#%d (merge %.7s, bypassed %d time(s)): %s — %s, %d blocking finding(s)\n",
			mp.Number, shortSHA(mp.MergeCommitSHA), len(entries), verdict, reason, len(findings))
		for _, f := range findings {
			num, err := fileEscapedFinding(ctx, c, mp, f, *dryRun)
			if err != nil {
				logToStderr("warning: filing issue for #%d finding %s: %v", mp.Number, f.Fingerprint[:8], err)
				failed++
				continue
			}
			if num > 0 {
				filed++
			}
		}
	}

	fmt.Printf("re-review %s/%s: %d merged PR(s) scanned, %d bypassed merge(s) re-reviewed, %d issue(s) %s\n",
		owner, repo, len(merged), reviewed, filed,
		map[bool]string{true: "would be filed", false: "filed"}[*dryRun])
	if failed > 0 {
		fmt.Printf("re-review: %d failure(s); see warnings above\n", failed)
		return fmt.Errorf("re-review: %d failure(s)", failed)
	}
	return nil
}

// parseBypassLog extracts the log lines from a sticky bypass-log comment,
// keeping only entries whose timestamp falls within the window ending now.
// Timestamps are parsed as RFC3339 first; the minute-resolution format
// `cite bypass` writes (YYYY-MM-DDTHH:MMZ) is accepted as a fallback.
func parseBypassLog(body string, cutoff time.Time) []bypassEntry {
	var out []bypassEntry
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		m := bypassLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, m[1])
		if err != nil {
			at, err = time.Parse("2006-01-02T15:04Z", m[1])
		}
		if err != nil {
			continue // unparseable line — skip, never guess
		}
		var num int
		if _, err := fmt.Sscanf(m[2], "%d", &num); err != nil {
			continue
		}
		e := bypassEntry{At: at, PR: num, AppliedBy: m[3], RunURL: m[4]}
		if e.At.Before(cutoff) {
			continue // outside the look-back window
		}
		out = append(out, e)
	}
	return out
}

// rereviewMerged runs the standard review pipeline over one bypassed merge:
// manifest + patches from the API, post-image at the head SHA, instructions
// from the base ref, reviewer.New + gate.Decide — exactly like
// `cite review --pr`, but with no check run, no review posting, no sticky
// state: GitHub surfaces are untouched. Only blocking findings come back.
func rereviewMerged(ctx context.Context, c *githubclient.Client, cfg *config.Config, owner, repo string, mp githubclient.MergedPR) ([]model.ValidatedFinding, model.Verdict, string, error) {
	pr, err := c.GetPR(ctx, mp.Number)
	if err != nil {
		return nil, "", "", fmt.Errorf("fetching #%d: %w", mp.Number, err)
	}
	entries, err := c.ListPRFiles(ctx, mp.Number)
	if err != nil {
		return nil, "", "", err
	}
	extras, err := c.ListPRFileExtras(ctx, mp.Number)
	if err != nil {
		return nil, "", "", err
	}

	// Instructions from the BASE ref, never the head (§5 divergence 1).
	baseTree := githubclient.NewAPITree(c, owner, repo, pr.BaseRef).WithContext(ctx)
	var changed []string
	for _, e := range entries {
		if e.Status != "D" {
			changed = append(changed, e.Path)
		}
	}
	instr, warnings, err := instructions.Resolve(baseTree, changed, nil)
	if err != nil {
		return nil, "", "", err
	}
	printInstructionWarnings(warnings)

	// Post-image of every non-deleted file at the head SHA.
	post := map[string][]byte{}
	for _, e := range entries {
		if e.Status == "D" {
			continue
		}
		if b, ok, err := c.GetFileContent(ctx, pr.HeadSHA, e.Path); err == nil && ok {
			post[e.Path] = b
		}
	}

	// Per-file patches → one parsed diff set for anchor validation.
	diffs := map[string]*scope.DiffFile{}
	var sb strings.Builder
	for _, e := range entries {
		if x, ok := extras[e.Path]; ok && x.Patch != "" {
			sb.WriteString(x.Patch)
			sb.WriteString("\n")
		}
	}
	if sb.Len() > 0 {
		if d, perr := scope.ParseUnifiedDiff(sb.String()); perr == nil {
			for _, df := range d.Files {
				diffs[df.Path] = df
			}
		}
	}

	modelClient, err := model.NewOpenAICompatClient()
	if err != nil {
		return nil, model.VerdictCouldNotEvaluate, err.Error(), err
	}
	r := reviewer.New(reviewer.Options{
		Cfg:      cfg,
		Client:   modelClient,
		Instr:    instr,
		Verifier: &apiVerifier{c: c, owner: owner, repo: repo, ref: pr.BaseRef, tree: baseTree},
		Logger:   logToStderr,
	})
	rec, err := r.Run(ctx, reviewer.Inputs{
		Manifest:      entries,
		Diffs:         diffs,
		PostImage:     post,
		PRDescription: pr.Body,
		Nonce:         newNonce(),
	})
	if err != nil && rec == nil {
		return nil, model.VerdictCouldNotEvaluate, err.Error(), err
	}
	rec.Repository = owner + "/" + repo
	rec.PRNumber = mp.Number
	rec.HeadSHA = pr.HeadSHA
	rec.BaseRef = pr.BaseRef
	rec.BaseSHA = pr.BaseSHA
	rec.Coverage = scope.ComputeCoverage(rec.Files, len(entries))

	verdict, reason := gate.Decide(rec, cfg, gate.Options{})
	rec.Verdict, rec.VerdictReason = verdict, reason

	var blocking []model.ValidatedFinding
	for _, f := range rec.Findings {
		if f.Blocks {
			blocking = append(blocking, f)
		}
	}
	return blocking, verdict, reason, nil
}

// fileEscapedFinding opens ONE issue per blocking finding — the escaped
// finding surfaces where humans read, never back onto the merged pull
// request. Dry-run prints instead of creating (returns 0).
func fileEscapedFinding(ctx context.Context, c *githubclient.Client, mp githubclient.MergedPR, f model.ValidatedFinding, dryRun bool) (int, error) {
	title := fmt.Sprintf("[cite] escaped finding on bypassed merge %s (#%d)", shortSHA(mp.MergeCommitSHA), mp.Number)
	body := fmt.Sprintf(
		"This finding was produced by re-reviewing a **bypassed** merge: #%d landed on the default branch as %.7s without a completed review (break-glass label applied). The bypass bought time, not amnesty — verify and triage here.\n\n---\n\n%s",
		mp.Number, shortSHA(mp.MergeCommitSHA), renderComment(f))
	if dryRun {
		fmt.Printf("dry-run: would create issue %q\n\n%s\n", title, body)
		return 0, nil
	}
	return c.CreateIssue(ctx, title, body)
}
