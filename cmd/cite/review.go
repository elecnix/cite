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
	"os"
	"sort"
	"strings"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *diffPath != "" && *prSpec != "":
		return fmt.Errorf("use --diff or --pr, not both")
	case *diffPath != "":
		return reviewLocal(*diffPath, *cfgPath)
	case *prSpec != "":
		return reviewPR(*prSpec, *cfgPath, *dryRun, *disabled)
	default:
		fs.Usage()
		return fmt.Errorf("review: one of --diff or --pr is required")
	}
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

func reviewLocal(diffPath, cfgPath string) error {
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
	verdict, reason := gate.Decide(rec, cfg, gate.Options{})
	rec.Verdict, rec.VerdictReason = verdict, reason
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
	Ledger       string                       `json:"ledger,omitempty"` // base64 blob
	BlobSHAs     map[string]string            `json:"blob_shas,omitempty"`
	Findings     []threadFinding              `json:"findings,omitempty"`
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

func reviewPR(spec, cfgPath string, dryRun, disabled bool) error {
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
	if !dryRun && !disabled {
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

	// Sticky comment: ledger + incremental state.
	prevState := readSticky(ctx, c, num)

	// Incremental re-review keyed on content (§10): only files whose blob
	// SHA changed are reviewed fresh; findings on untouched files carry
	// forward. Fails toward re-review: carried findings re-enter the plan so
	// their threads stay alive.
	curSHAs := map[string]string{}
	for path, x := range extras {
		curSHAs[path] = x.BlobSHA
	}
	toReview := publisher.FilesToReview(prevState.BlobSHAs, curSHAs)
	if len(prevState.BlobSHAs) > 0 && len(toReview) < len(entries) {
		logToStderr("incremental: %d of %d files changed content since last review", len(toReview), len(entries))
		carryIntoRecord(rec, prevState, toReview)
	}

	// Budget, then reconciliation against live threads.
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
	_ = chosen

	live, threadData, threadNodeIDs, err := threadsFromGitHub(ctx, c, num)
	if err != nil {
		return concludeFailure(ctx, c, checkID, dryRun, model.VerdictCouldNotEvaluate, "fetching review threads: "+err.Error())
	}
	ledger := publisher.DismissalLedger{}
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
	plan := publisher.Reconcile(rec.Findings, live, ledger, publisher.ReconcileOptions{
		Repository: repoFull,
		Now:        time.Now(),
		SpanGone:   spanGone,
	})

	verdict, reason := gate.Decide(rec, cfg, gate.Options{})
	rec.Verdict, rec.VerdictReason = verdict, reason

	// Publish, ordered so every step is safe to redo (§10).
	body, comments, unanchorable := buildReviewPayload(diffs, plan, instr, rec)
	if !dryRun {
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
