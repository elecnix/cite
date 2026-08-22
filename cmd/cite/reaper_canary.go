package main

// cite reaper / cite canary — the two cron jobs §11 runs outside Actions.

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

// runReaper closes the last availability hole (§11): any open pull request
// whose head SHA has had no terminal Cite check for twenty minutes gets a
// terminal failure with "run never reported". A stuck check must self-heal
// into something a human can act on.
func init() {
	registerCommand("reaper", runReaper)
	registerCommand("canary", runCanary)
}

func runReaper(args []string) error {
	fs := flag.NewFlagSet("reaper", flag.ContinueOnError)
	repo := fs.String("repo", "", "owner/repo to sweep")
	stale := fs.Int("stale-minutes", 20, "minutes without a terminal check before failing")
	dryRun := fs.Bool("dry-run", false, "report without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return fmt.Errorf("reaper: --repo owner/repo is required")
	}
	owner, name, err := splitRepo(*repo)
	if err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required")
	}
	c := githubclient.New(token, "", nil).WithRepo(owner, name)
	ctx := context.Background()

	prs, err := c.ListOpenPRs(ctx)
	if err != nil {
		return err
	}
	var states []gate.PRState
	for _, pr := range prs {
		has, checkID, err := c.HasTerminalCiteCheck(ctx, pr.HeadSHA)
		if err != nil {
			return err
		}
		states = append(states, gate.PRState{
			Number:           pr.Number,
			HeadSHA:          pr.HeadSHA,
			HasTerminalCheck: has,
			CheckID:          checkID,
			AgeMinutes:       int(time.Since(pr.UpdatedAt).Minutes()),
		})
	}
	stuck := gate.NeedsReaper(states, *stale)
	for _, s := range stuck {
		fmt.Printf("PR #%d (%s): no terminal check for %dm — concluding failure\n",
			s.Number, shortSHA(s.HeadSHA), s.AgeMinutes)
		if !*dryRun && s.CheckID != 0 {
			if err := c.ConcludeCheckRun(ctx, s.CheckID, model.VerdictCouldNotEvaluate.Conclusion(),
				"Cite never reported", "run never reported"); err != nil {
				return err
			}
		}
	}
	if len(stuck) == 0 {
		fmt.Println("no stuck checks")
	}
	return nil
}

// runCanary exercises every leg of the fallback chain on a schedule (§6):
// an untested fallback is not a fallback but a second outage that begins at
// the same moment as the first.
func runCanary(args []string) error {
	fs := flag.NewFlagSet("canary", flag.ContinueOnError)
	cfgPath := fs.String("config", ".github/cite.yml", "config file with providers/fallback")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := loadConfig(*cfgPath)
	if len(cfg.Fallback) == 0 {
		fmt.Println("canary: no fallback chain configured — nothing to exercise")
		return nil
	}
	client, err := model.NewOpenAICompatClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Complete(ctx, model.CompletionRequest{
		System: "ping", User: "reply pong", MaxOutputTokens: 8, Temperature: 0,
	})
	status := "ok"
	if err != nil {
		status = "FAIL: " + err.Error()
	} else if resp == nil {
		status = "FAIL: empty response"
	}
	fmt.Printf("canary primary leg (%s): %s\n", client.ModelID(), status)
	for _, leg := range cfg.Fallback {
		fmt.Printf("canary fallback leg %s: not exercised (single-endpoint client)\n", leg)
	}
	if err != nil {
		return fmt.Errorf("canary: primary leg failed")
	}
	return nil
}

func splitRepo(s string) (owner, name string, err error) {
	parts := splitSlash(s)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected owner/repo, got %q", s)
	}
	return parts[0], parts[1], nil
}

func splitSlash(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
