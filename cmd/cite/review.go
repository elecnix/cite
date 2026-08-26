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
		return reviewPR(*prSpec, *cfgPath, *dryRun, *disabled, sink)
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

func loadConfig(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: config invalid, using defaults: %v\n", err)
		return config.Default()
	}
	return cfg
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
	cfg := loadConfig(cfgPath)
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

func reviewPR(spec, cfgPath string, dryRun, disabled bool, sink publisher.Sink) error {
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
	cfg := loadConfig(cfgPath)

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
		checkID, err = c.CreateCheckRun(ctx, pr.HeadSHA, "cite", "Cite is reviewing", "queued", "queued")
		if err != nil {
			return fmt.Errorf("creating check run: %w", err)
		}
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
		prevState := readSticky(ctx, c, num)
		toReview := publisher.FilesToReview(prevState.BlobSHAs, curSHAs)
		if len(prevState.BlobSHAs) > 0 && len(toReview) < len(entries) {
			logToStderr("incremental: %d of %d files changed content since last review", len(toReview), len(entries))
			carryIntoRecord(rec, prevState, toReview)
		}

		var threadData map[int64]*threadFinding
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

		// A thread resolves ONLY when its quoted span is verified gone from the
		// new content — never on a merely-disappeared fingerprint (§10).
		spanGone := func(t publisher.LiveThread) bool {
			return spanGoneFor(threadData[t.ID], post)(t)
		}
		plan = publisher.Reconcile(rec.Findings, live, ledger, publisher.ReconcileOptions{
			Repository: repoFull,
			Now:        time.Now(),
			SpanGone:   spanGone,
		})
	}

	applyCost(rec, cfg)
	verdict, reason := gate.Decide(rec, cfg, gate.Options{})
	rec.Verdict, rec.VerdictReason = verdict, reason

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
			if nodeID, ok := threadNodeIDs[id]; ok {
				if err := c.ResolveReviewThread(ctx, nodeID); err != nil {
					logToStderr("resolve thread %d failed: %v", id, err)
				}
			}
		}
		for _, t := range live {
			if containsID(plan.ThreadsToMinimise, t.ID) {
				_ = c.MinimizeComment(ctx, t.ID)
			}
		}
		writeSticky(ctx, c, num, rec, ledger, curSHAs, plan.CommentsToPost)
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
// silently dropped (§10: incremental fails toward re-review).
func carryIntoRecord(rec *model.RunRecord, prev *stickyState, toReview []string) {
	reviewing := map[string]bool{}
	for _, p := range toReview {
		reviewing[p] = true
	}
	for _, tf := range prev.Findings {
		if reviewing[tf.Path] {
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
		rec.Findings = append(rec.Findings, model.ValidatedFinding{
			Finding: model.Finding{
				ID: "carried-" + tf.Fingerprint[:8], Category: tf.Category, Title: tf.Title,
				Evidence: tf.Evidence, Confidence: model.ConfidenceLikely,
				IntroducedBy: model.IntroducedAddedLine,
			},
			Path: tf.Path, EvidenceLevel: model.EvidenceNormalized,
			Blocks: tf.Category.MayBlock(), Fingerprint: tf.Fingerprint,
		})
	}
}

func nowClock() time.Time { return time.Now() }
