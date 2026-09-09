package main

// cite review — the review pass. Two modes:
//
//	cite review --diff <file>            local; reads post-image from cwd
//	cite review --pr owner/repo#N        API mode; no checkout, ever (§12 I1)
//	                                     (--dry-run prints, posts nothing)

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/gate"
	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
	"github.com/elecnix/cite/internal/reviewer"
	"github.com/elecnix/cite/internal/scope"
)

func runReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	diffPath := fs.String("diff", "", "path to a unified diff file (local mode)")
	prSpec := fs.String("pr", "", "pull request as owner/repo#N (API mode)")
	dryRun := fs.Bool("dry-run", false, "print results, post nothing")
	cfgPath := fs.String("config", ".github/cite.yml", "config file (optional)")
	disabled := fs.Bool("disabled", false, "kill switch: conclude disabled-by-configuration")
	reportFmt := fs.String("report", "", "write a full report instead of publishing to GitHub: json or markdown")
	outPath := fs.String("out", "", "report destination file (default stdout; requires --report)")
	recordOut := fs.String("record-out", os.Getenv("CITE_RECORD_OUT"), "write the raw run record JSON to this path, even when the run fails (forensics; defaults to $CITE_RECORD_OUT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var sink publisher.Sink
	switch *reportFmt {
	case "":
		if *outPath != "" {
			return fmt.Errorf("review: --out requires --report")
		}
	case "json":
		w, closer, err := reportWriter(*outPath)
		if err != nil {
			return err
		}
		if closer != nil {
			defer closer()
		}
		sink = publisher.JSONReportSink(w)
	case "markdown":
		w, closer, err := reportWriter(*outPath)
		if err != nil {
			return err
		}
		if closer != nil {
			defer closer()
		}
		sink = publisher.MarkdownReportSink(w)
	default:
		return fmt.Errorf("review: --report must be json or markdown")
	}
	switch {
	case *diffPath != "" && *prSpec != "":
		return fmt.Errorf("use --diff or --pr, not both")
	case *diffPath != "":
		return reviewLocal(*diffPath, *cfgPath, sink)
	case *prSpec != "":
		reviewerID, err := resolveReviewerID()
		if err != nil {
			return fmt.Errorf("review: %w", err)
		}
		return reviewPR(*prSpec, *cfgPath, *dryRun, *disabled, sink, *recordOut, reviewerID)
	default:
		fs.Usage()
		return fmt.Errorf("review: one of --diff or --pr is required")
	}
}

// reportWriter resolves the report destination: a file when one is given,
// stdout otherwise. The returned closer is non-nil only for files.
func reportWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, nil, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("creating report file: %w", err)
	}
	return f, func() { _ = f.Close() }, nil
}

// writeRecordOut dumps the run record to recordOut for forensics, best-effort:
// a failed archive write must never mask the real failure. A run killed
// mid-flight still returns a partial record, so the call log, per-file
// outcomes and token usage all survive a failure — that is the point.
func writeRecordOut(rec *model.RunRecord, runErr error, recordOut string) {
	if recordOut == "" {
		return
	}
	payload := struct {
		*model.RunRecord
		RunError string `json:"run_error,omitempty"`
	}{RunRecord: rec, RunError: ""}
	if runErr != nil {
		payload.RunError = runErr.Error()
	}
	if payload.RunRecord == nil {
		payload.RunRecord = &model.RunRecord{SchemaVersion: model.SchemaVersion}
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cite: forensics record marshal failed: %v\n", err)
		return
	}
	if err := os.WriteFile(recordOut, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "cite: forensics record write to %s failed: %v\n", recordOut, err)
	} else {
		fmt.Fprintf(os.Stderr, "cite: run record archived to %s for forensics\n", recordOut)
	}
}

// loadConfig reads the Cite configuration, failing closed on an invalid file
// (issue #59): a config that fails to parse or validate must never degrade to
// defaults, because the fallback silently discards the entire roles block —
// including the explicit per-call timeouts an operator tuned (a configured
// roles.review.timeout was never in force for exactly this reason). A missing
// file is the documented "no configuration" case and still yields defaults,
// but it is logged so runs are honest about which dial was in force.
func loadConfig(path string) (*config.Config, error) {
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		logToStderr("config %s not found; using built-in defaults", path)
		return config.Default(), nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("config %s is invalid; refusing to fall back to defaults (the whole roles block, including explicit timeouts, would be silently discarded): %w", path, err)
	}
	return cfg, nil
}

func newNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func logToStderr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// printRecord renders a run record for humans on stdout.
func printRecord(rec *model.RunRecord) {
	fmt.Printf("model=%s temperature=%.1f samples=%d\n", rec.Model, rec.Temperature, rec.Samples)
	fmt.Printf("coverage: %d/%d api files complete=%t\n", rec.Coverage.Reviewed+rec.Coverage.ApprovedSkip, rec.Coverage.APIFiles, rec.Coverage.Complete)
	fmt.Printf("cost: $%.4f (in %s out %s)\n", rec.CostUSD, humanTokens(rec.Usage.InputTokens), humanTokens(rec.Usage.OutputTokens))
	if rec.Usage.InputTokens > 0 {
		fmt.Printf("cache: %.0f%% of prompt tokens on reads (§7 floor %.0f%%)\n",
			100*rec.Usage.CacheHitRate(), 100*model.MinCacheHitRate)
	}
	for _, f := range rec.Files {
		line := fmt.Sprintf("  %-3s %s", f.Status, f.Path)
		if f.Reason != "" {
			line += " (" + f.Reason + ")"
		}
		fmt.Println(line)
	}
	for _, f := range rec.Findings {
		fmt.Printf("finding [%s] %s %s:%d-%d blocks=%t evidence=%s%s\n",
			f.Category, f.Title, f.Path, f.Anchor.StartLine, f.Anchor.EndLine,
			f.Blocks, f.EvidenceLevel, verifierSuffix(f.VerifierResult))
	}
	if len(rec.Drops) > 0 {
		fmt.Printf("drops: %d\n", len(rec.Drops))
		for _, d := range rec.Drops {
			fmt.Printf("  drop [%s] reason=%s %s\n", d.Category, d.Reason, d.Title)
		}
	}
}

func verifierSuffix(v string) string {
	if v == "" {
		return ""
	}
	return " verifier=" + v
}

// --- local mode -----------------------------------------------------------

func reviewLocal(diffPath, cfgPath string, sink publisher.Sink) error {
	raw, err := os.ReadFile(diffPath)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	manifest := scope.ParseNameStatus(string(raw))
	diff, err := scope.ParseUnifiedDiff(string(raw))
	if err != nil {
		return fmt.Errorf("parsing diff: %w", err)
	}
	diffs := map[string]*scope.DiffFile{}
	for _, df := range diff.Files {
		diffs[df.Path] = df
	}

	// Post-image comes from the working tree relative to cwd.
	post := map[string][]byte{}
	var changed []string
	for _, e := range manifest {
		if e.Status == "D" {
			continue
		}
		b, err := os.ReadFile(e.Path)
		if err != nil {
			continue // unreadable ⇒ reviewer records the error state
		}
		post[e.Path] = b
		changed = append(changed, e.Path)
	}

	instr, warnings, err := instructions.Resolve(githubclient.NewFSTree("."), changed, nil)
	if err != nil {
		return err
	}
	printInstructionWarnings(warnings)

	modelClient, err := model.NewOpenAICompatClient()
	if err != nil {
		return err
	}
	r := reviewer.New(reviewer.Options{
		Cfg:      cfg,
		Client:   modelClient,
		Instr:    instr,
		Verifier: &gitVerifier{dir: "."},
		Logger:   logToStderr,
	})
	rec, err := r.Run(context.Background(), reviewer.Inputs{
		Manifest:      manifest,
		Diffs:         diffs,
		PostImage:     post,
		PRDescription: "",
		Nonce:         newNonce(),
	})
	if err != nil {
		return err
	}
	rec.Coverage = scope.ComputeCoverage(rec.Files, len(manifest))
	applyCost(rec, cfg)
	verdict, reason := gate.Decide(rec, cfg, gate.Options{})
	rec.Verdict, rec.VerdictReason = verdict, reason
	if sink != nil {
		if err := sink.Publish(rec, publisher.ReportPayload{}); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
	}
	printRecord(rec)
	fmt.Printf("\n%s — %s\n", verdict, reason)
	fmt.Println(gate.CheckRunPayload(rec, verdict, reason))
	if verdict == model.VerdictPass {
		return nil
	}
	return fmt.Errorf("gate: %s", verdict)
}

func printInstructionWarnings(ws []instructions.Warning) {
	for _, w := range ws {
		logToStderr("instructions warning (%s): %s", w.File, w.Message)
	}
}

// --- API mode --------------------------------------------------------------

type stickyState struct {
	Ledger        string            `json:"ledger,omitempty"` // base64 blob
	BlobSHAs      map[string]string `json:"blob_shas,omitempty"`
	Findings      []threadFinding   `json:"findings,omitempty"`
	ReplyVerdicts map[string]string `json:"reply_verdicts,omitempty"` // fingerprint → reply classification cache
}

const stickyMarker = "<!-- cite-sticky -->"

// threadFinding is what we can reconstruct about a previously posted finding:
// enough to match live threads and verify spans gone.
type threadFinding struct {
	Fingerprint string           `json:"fingerprint"`
	Path        string           `json:"path"`
	Category    model.Category   `json:"category"`
	Title       string           `json:"title"`
	Evidence    []model.Evidence `json:"evidence"`
}

func reviewPR(spec, cfgPath string, dryRun, disabled bool, sink publisher.Sink, recordOut, reviewerID string) error {
	// Report mode: a full run against the real pull request whose outcome goes
	// to a local sink instead of GitHub. It is not a dry-run — nothing is
	// simulated — but every mutation (check run, review, thread resolution,
	// sticky comment) is skipped, and so is incremental state: all manifest
	// files are reviewed fresh and findings are chosen by budget alone.
	reportMode := sink != nil
	owner, repo, num, err := parsePRSpec(spec)
	if err != nil {
		return err
	}
	repoFull := owner + "/" + repo
	// Issue #59: load before the run commits to anything review-shaped, but
	// conclude only once the check run exists, so an invalid config surfaces
	// as COULD_NOT_EVALUATE on the check run instead of a silent default run.
	cfg, cfgErr := loadConfig(cfgPath)

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is required for --pr mode")
	}
	c := githubclient.New(token, "", nil).WithRepo(owner, repo)
	ctx := context.Background()

	pr, err := c.GetPR(ctx, num)
	if err != nil {
		return err
	}

	// The check run targets the PR head SHA — never github.sha, which is a
	// synthetic merge commit on pull_request events (§11).
	var checkID int64
	if !dryRun && !disabled && !reportMode {
		checkID, err = c.CreateCheckRun(ctx, pr.HeadSHA, checkNameFor(reviewerID), "Cite is reviewing", "queued", "queued")
		if err != nil {
			return fmt.Errorf("creating check run: %w", err)
		}
	}
	if cfgErr != nil {
		// Fail closed (issue #59): an invalid config must never degrade the
		// run to defaults — conclude COULD_NOT_EVALUATE, never green.
		return concludeFailure(ctx, c, checkID, dryRun, model.VerdictCouldNotEvaluate, cfgErr.Error())
	}
	if disabled {
		v, reason := gate.DecideDisabled(cfg)
		title, summary := gate.CheckRunPayload(&model.RunRecord{Samples: 0}, v, reason)
		fmt.Printf("%s — %s\n%s\n%s\n", v, reason, title, summary)
		if !dryRun && checkID != 0 {
			if err := c.ConcludeCheckRun(ctx, checkID, v.Conclusion(), title, summary); err != nil {
				return err
			}
		}
		return nil
	}

	entries, err := c.ListPRFiles(ctx, num)
	if err != nil {
		return err
	}
	extras, err := c.ListPRFileExtras(ctx, num)
	if err != nil {
		return err
	}
	mergeBase, err := c.GetMergeBase(ctx, pr.BaseRef, pr.HeadSHA)
	if err != nil {
		return err
	}

	// Instructions come from the BASE ref, never the head (§5 divergence 1).
	baseTree := githubclient.NewAPITree(c, owner, repo, pr.BaseRef).WithContext(ctx)
	var changed []string
	for _, e := range entries {
		if e.Status != "D" {
			changed = append(changed, e.Path)
		}
	}
	instr, warnings, err := instructions.Resolve(baseTree, changed, nil)
	if err != nil {
		return err
	}
	printInstructionWarnings(warnings)

	// Post-image of every non-deleted file at the head SHA.
	post := map[string][]byte{}
	for _, e := range entries {
		if e.Status == "D" {
			continue
		}
		b, ok, err := c.GetFileContent(ctx, pr.HeadSHA, e.Path)
		if err == nil && ok {
			post[e.Path] = b
		}
	}

	// Per-file patches → one parsed diff set for anchor validation. GitHub's
	// files API returns patches as bare @@ hunks without git file headers, so
	// each patch is parsed with its manifest-known path via ParseFilePatch.
	// A patch that fails to parse is logged loudly and skipped: findings for
	// that file will be dropped anchor_invalid (fail-closed), but one bad
	// file must not silently neuter anchor validation for the whole run —
	// which is exactly what swallowing a whole-batch parse error did before
	// (every PR-mode review between Aug 21 and this fix dropped all findings).
	diffs := map[string]*scope.DiffFile{}
	for _, e := range entries {
		x, ok := extras[e.Path]
		if !ok || x.Patch == "" {
			continue // binary, too large, or deleted: no textual hunks to validate against
		}
		df, perr := scope.ParseFilePatch(e.Path, e.Status, x.Patch)
		if perr != nil {
			logToStderr("WARNING: diff parse failed for %s; anchors in this file cannot validate: %v", e.Path, perr)
			continue
		}
		diffs[e.Path] = df
	}

	modelClient, err := model.NewOpenAICompatClient()
	if err != nil {
		// Fail-closed: conclude COULD_NOT_EVALUATE, never green.
		return concludeFailure(ctx, c, checkID, dryRun, model.VerdictCouldNotEvaluate, err.Error())
	}
	verifier := &apiVerifier{c: c, owner: owner, repo: repo, ref: pr.BaseRef, tree: baseTree}
	r := reviewer.New(reviewer.Options{
		Cfg:      cfg,
		Client:   modelClient,
		Instr:    instr,
		Verifier: verifier,
		Logger:   logToStderr,
	})

	rec, err := r.Run(ctx, reviewer.Inputs{
		Manifest:      entries,
		Diffs:         diffs,
		PostImage:     post,
		PRDescription: pr.Body,
		Nonce:         newNonce(),
	})
	// Forensics first, whatever the run's fate: a failed or killed run still
	// carries partial results, the call log and usage — exactly what an
	// operator needs to answer "why did this take so long".
	writeRecordOut(rec, err, recordOut)
	if err != nil && rec == nil {
		return concludeFailure(ctx, c, checkID, dryRun, model.VerdictCouldNotEvaluate, err.Error())
	}
	rec.Repository = repoFull
	rec.PRNumber = num
	rec.HeadSHA = pr.HeadSHA
	rec.BaseRef = pr.BaseRef
	rec.BaseSHA = pr.BaseSHA
	rec.MergeBaseSHA = mergeBase
	rec.Coverage = scope.ComputeCoverage(rec.Files, len(entries))

	// Budget first; reconciliation against live threads follows unless in
	// report mode. Report mode has no sticky state and no live threads: all
	// manifest files are reviewed fresh and the budgeted findings alone form
	// the plan.
	changedLines := 0
	for _, e := range entries {
		changedLines += e.Adds + e.Dels
	}
	total := publisher.CommentBudget(changedLines, cfg.MaxComments)
	ranked := rankForBudget(rec.Findings)
	chosen, drops := publisher.AllocateBudget(ranked, total, 2)
	for _, d := range drops {
		rec.Drops = append(rec.Drops, d.Entry)
	}
	var plan publisher.ReconciliationPlan
	var live []publisher.LiveThread
	var threadNodeIDs map[int64]string
	var ledger publisher.DismissalLedger
	var threadData map[int64]*threadFinding
	curSHAs := map[string]string{}
	for path, x := range extras {
		curSHAs[path] = x.BlobSHA
	}
	if reportMode {
		plan = publisher.ReconciliationPlan{CommentsToPost: chosen}
	} else {
		// Sticky comment: ledger + incremental state. Incremental re-review is
		// keyed on content (§10): only files whose blob SHA changed are reviewed
		// fresh; findings on untouched files carry forward. Fails toward
		// re-review: carried findings re-enter the plan so their threads stay
		// alive.
		prevState := readSticky(ctx, c, num, stickyMarkerFor(reviewerID))
		toReview := publisher.FilesToReview(prevState.BlobSHAs, curSHAs)
		if len(prevState.BlobSHAs) > 0 && len(toReview) < len(entries) {
			logToStderr("incremental: %d of %d files changed content since last review", len(toReview), len(entries))
			manifestSet := make(map[string]bool, len(entries))
			for _, e := range entries {
				manifestSet[e.Path] = true
			}
			carryIntoRecord(rec, prevState, toReview, manifestSet, diffs)
		}

		var err error
		live, threadData, threadNodeIDs, err = threadsFromGitHub(ctx, c, num)
		if err != nil {
			return concludeFailure(ctx, c, checkID, dryRun, model.VerdictCouldNotEvaluate, "fetching review threads: "+err.Error())
		}
		if prevState.Ledger != "" {
			if l, err := publisher.UnmarshalBlob(prevState.Ledger); err == nil {
				ledger = l
			} else {
				logToStderr("warning: ledger corrupt, rebuilding from threads")
				ledger = publisher.RebuildFromThreads(repoFull, live, nowClock())
			}
		}
		registerThreadText(live, threadData)

		// A thread resolves on a verified basis only: its quoted span is
		// verified gone from the new content (§10), or the file was re-reviewed
		// fresh this run and the finding was not re-raised — a new review
		// adjudicating the old one. Threads on files that errored or were
		// skipped this run have no such basis and stay open (fail toward
		// keeping the thread, never toward clearing it).
		reviewedOK := map[string]bool{}
		for _, fo := range rec.Files {
			if fo.State == model.FileReviewed {
				reviewedOK[fo.Path] = true
			}
		}
		spanGone := func(t publisher.LiveThread) bool {
			return spanGoneFor(threadData[t.ID], post)(t)
		}
		plan = publisher.Reconcile(rec.Findings, live, ledger, publisher.ReconcileOptions{
			Repository:      repoFull,
			Now:             time.Now(),
			SpanGone:        spanGone,
			ReReviewedFresh: func(t publisher.LiveThread) bool { return reviewedOK[t.Path] },
			BlobSHAs:        curSHAs,
		})
	}

	applyCost(rec, cfg)
	verdict, reason := gate.Decide(rec, cfg, gate.Options{})
	rec.Verdict, rec.VerdictReason = verdict, reason

	// Surface anchor_invalid drops on the Actions run page (issue #42): the
	// raw CI log is truncated, ANSI-mangled, and not where a reviewer looks.
	// This is a safety net alongside the review-body section; it survives a
	// posting failure and costs nothing outside GitHub Actions.
	emitDropsToStepSummary(rec.Drops)

	// Publish, ordered so every step is safe to redo (§10).
	body, comments, unanchorable := buildReviewPayload(diffs, plan, instr, rec)
	if reportMode {
		payload := publisher.ReportPayload{Body: body, Unanchorable: unanchorable}
		for _, c := range comments {
			payload.Comments = append(payload.Comments, publisher.InlineComment{
				Path: c.Path, StartLine: c.StartLine, Line: c.Line, Side: c.Side, Body: c.Body,
			})
		}
		if err := sink.Publish(rec, payload); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
	} else if !dryRun {
		if body != "" || len(comments) > 0 {
			if err := c.CreateReview(ctx, num, body, comments); err != nil {
				logToStderr("review posting failed: %v", err)
				return concludeFailure(ctx, c, checkID, dryRun, verdict, "publish failed: "+err.Error())
			}
		} else {
			logToStderr("nothing to say: no review posted (§10)")
		}
		for _, id := range plan.ThreadsToResolve {
			nodeID, ok := threadNodeIDs[id]
			if !ok {
				continue
			}
			// Resolve first, reply second. The reply asserts the thread was
			// resolved, so it may only post once the resolve succeeded: a
			// failed resolve leaves the thread open and silent, and a later
			// run retries — never open under a comment claiming it was closed.
			if err := c.ResolveReviewThread(ctx, nodeID); err != nil {
				logToStderr("resolve thread %d failed: %v", id, err)
				continue
			}
			if reply := resolutionReply(threadData, id, post, pr.HeadSHA); reply != "" {
				if err := c.ReplyToReviewComment(ctx, int64(num), id, reply); err != nil {
					logToStderr("reply to thread %d failed: %v", id, err)
				}
			}
		}
		for _, t := range live {
			if containsID(plan.ThreadsToMinimise, t.ID) {
				_ = c.MinimizeComment(ctx, t.ID)
			}
		}
		writeSticky(ctx, c, num, stickyMarkerFor(reviewerID), rec, ledger, curSHAs, plan.CommentsToPost)
	} else {
		fmt.Printf("dry-run: would post %d comment(s), resolve %d thread(s), minimise %d\n",
			len(comments), len(plan.ThreadsToResolve), len(plan.ThreadsToMinimise))
	}

	title, summary := gate.CheckRunPayload(rec, verdict, reason)
	fmt.Printf("%s — %s\n%s\n%s\n", verdict, reason, title, summary)
	printRecord(rec)
	if unanchorable > 0 {
		fmt.Printf("(moved to review body): %d finding(s)\n", unanchorable)
	}
	if !dryRun && checkID != 0 {
		if err := c.ConcludeCheckRun(ctx, checkID, verdict.Conclusion(), title, summary); err != nil {
			return err
		}
	}
	if verdict == model.VerdictPass {
		return nil
	}
	return fmt.Errorf("gate: %s", verdict)
}

func concludeFailure(ctx context.Context, c *githubclient.Client, checkID int64, dryRun bool, v model.Verdict, reason string) error {
	fmt.Printf("%s — %s\n", v, reason)
	if !dryRun && checkID != 0 {
		if err := c.ConcludeCheckRun(ctx, checkID, v.Conclusion(), "Cite could not evaluate", reason); err != nil {
			return err
		}
	}
	return fmt.Errorf("gate: %s", v)
}

func containsID(ids []int64, id int64) bool {
	for _, i := range ids {
		if i == id {
			return true
		}
	}
	return false
}

// resolutionReply renders the one-line reason Cite posts on a thread before
// resolving it, so the trail shows why the stale thread was cleared. The
// basis is verified span-gone when the evidence can be checked, otherwise
// the re-review-adjudicated basis Reconcile resolved on. Threads without a
// parsed evidence entry (missing map key, nil entry) fall back to the
// re-review basis rather than dereferencing anything.
func resolutionReply(threadData map[int64]*threadFinding, id int64, post map[string][]byte, headSHA string) string {
	if data, ok := threadData[id]; ok && data != nil && len(data.Evidence) > 0 &&
		spanGoneFor(data, post)(publisher.LiveThread{}) {
		return "Cite resolved this thread: the quoted span is verified gone from the current file content."
	}
	return fmt.Sprintf("Cite resolved this thread: the file was re-reviewed at %.7s and this finding was no longer detected.", headSHA)
}

// rankForBudget orders findings for the assembly cap: blocking first, then
// confidence certain > likely > question, then category weight.
func rankForBudget(fs []model.ValidatedFinding) []model.ValidatedFinding {
	out := append([]model.ValidatedFinding(nil), fs...)
	confRank := map[model.Confidence]int{
		model.ConfidenceCertain: 0, model.ConfidenceLikely: 1, model.ConfidenceQuestion: 2,
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Blocks != b.Blocks {
			return a.Blocks
		}
		if confRank[a.Confidence] != confRank[b.Confidence] {
			return confRank[a.Confidence] < confRank[b.Confidence]
		}
		if a.Category.MayBlock() != b.Category.MayBlock() {
			return a.Category.MayBlock()
		}
		return a.Path < b.Path
	})
	return out
}

// carryIntoRecord re-adds previous findings whose files were not re-reviewed
// this run. They carry forward as candidates requiring verification — never
// silently dropped (§10: incremental fails toward re-review) — but a carried
// finding is re-anchored against the CURRENT diff before it may block
// (issue #47): the documented contract is that every finding intersects an
// added or modified line of this change (docs/noise.md Rule 1), and a
// finding whose file left the PR diff (force-push, revert, base change)
// intersects nothing. Re-arming it with Blocks = Category.MayBlock() — the
// old behaviour — let a stale, out-of-diff finding conclude the gate as
// FOUND on a file with no diff, and could even promote a previous run's
// non-blocking note to a blocker.
func carryIntoRecord(rec *model.RunRecord, prev *stickyState, toReview []string, manifest map[string]bool, diffs map[string]*scope.DiffFile) {
	reviewing := map[string]bool{}
	for _, p := range toReview {
		reviewing[p] = true
	}
	for _, tf := range prev.Findings {
		if reviewing[tf.Path] {
			continue
		}
		// The file is no longer part of this pull request's change: the
		// finding cannot intersect an added or modified line of this diff,
		// so it is dropped with a logged reason (§8 drop log), never re-armed.
		if !manifest[tf.Path] {
			rec.Drops = append(rec.Drops, model.DropEntry{
				Path:     tf.Path,
				Category: tf.Category,
				Title:    tf.Title,
				Reason:   model.DropAnchorNotAddedLine,
				Detail:   "carried finding's file is no longer in the pull request diff; its anchor intersects no added line",
			})
			continue
		}
		dup := false
		for _, f := range rec.Findings {
			if f.Fingerprint == tf.Fingerprint {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		// Re-anchor against the current parsed diff: a carried finding blocks
		// only if one of its quoted evidence lines is an ADDED line of this
		// change (the same bar validateFindings applies to fresh findings).
		// Without a parsed diff for the file — patch missing or unparsable —
		// the intersection cannot be checked, so the finding fails closed to
		// a note: an unverifiable anchor never grounds a block (§8).
		blocks := false
		if df := diffs[tf.Path]; df != nil && tf.Category.MayBlock() {
			added := map[int]bool{}
			for _, n := range df.AddedLines() {
				added[n] = true
			}
			for _, ev := range tf.Evidence {
				if added[ev.Line] {
					blocks = true
					break
				}
			}
		}
		rec.Findings = append(rec.Findings, model.ValidatedFinding{
			Finding: model.Finding{
				ID: "carried-" + tf.Fingerprint[:8], Category: tf.Category, Title: tf.Title,
				Evidence: tf.Evidence, Confidence: model.ConfidenceLikely,
				IntroducedBy: model.IntroducedAddedLine,
			},
			Path: tf.Path, EvidenceLevel: model.EvidenceNormalized,
			Blocks: blocks, Fingerprint: tf.Fingerprint,
		})
	}
}

func nowClock() time.Time { return time.Now() }
