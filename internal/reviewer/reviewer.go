// Package reviewer runs the review pass (PLAN.md §7) and the finding
// validation pipeline (PLAN.md §8). The shape is the hybrid: ONE cheap
// whole-diff triage call first, serialized; then batched frontier calls over
// the flagged subset, with a bounded output per unit, a per-unit deadline, a
// run-global retry bucket and partial results written incrementally.
//
// Unflagged-file policy (§7 decision, documented): this implementation takes
// the safer default — files the triage pass did not flag are still reviewed
// in batched waves, after every flagged file. Triage flags therefore order
// and scope risk ranking rather than suppress reviews; a triage miss can
// cost money but cannot cost recall. The alternative (marking explicitly
// cleared unflagged files as reviewed with zero findings without a model
// call) was rejected because "the triage pass said it was fine" is exactly
// the shape of an all-or-nothing whole-diff failure, and coverage arithmetic
// would then rest on one cheap sample.
package reviewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// Verifier performs the mechanical external-claim checks (§8,
// "verify after, do not inform before"): git ls-tree for paths,
// definition-shaped search for symbols. The caller supplies the
// git/API-backed implementation; a nil Verifier makes path_exists /
// symbol_exists claims unverifiable, which drops any finding that declares
// one — fail closed.
type Verifier interface {
	PathExists(path string) bool
	SymbolExists(symbol string) bool
}

// DiscriminativeVerifier is the short discriminative call run on blocking
// candidates only (§8, "The verifier pass"). It must return
// "supported", "unsupported" or "needs-context-not-provided". It must NOT be
// framed as a judge arguing a finding is real: models are strong advocates
// and weak skeptics.
type DiscriminativeVerifier interface {
	Verify(ctx context.Context, path string, f model.Finding) (string, error)
}

// Options configures a Reviewer. Cfg and Client are required.
type Options struct {
	Cfg          *config.Config
	Client       model.Client
	Instr        *instructions.ResolvedInstructions
	Verifier     Verifier
	DiscVerifier DiscriminativeVerifier
	Logger       func(format string, args ...any)
}

// Inputs are the run inputs. Removed lines come from Diffs (they have no
// post-change anchor and are rendered in <removed_lines> with OLD numbers);
// there is deliberately no removed-image input.
type Inputs struct {
	Manifest      []scope.ManifestEntry
	Diffs         map[string]*scope.DiffFile
	PostImage     map[string][]byte // path -> full post-change file content
	PRDescription string
	Nonce         string // per-run nonce protecting untrusted blocks (§7)
}

// Reviewer executes one review pass per Run call.
type Reviewer struct {
	o           Options
	segB        string // per-run cached context segment (see prompt.go)
	blockingSet map[model.Category]bool

	runCtx context.Context // run-scoped ctx handed to the discriminative verifier

	retryMu   sync.Mutex
	retryLeft map[string]int // run-global retry token bucket, per unit type
	filesMu   sync.Mutex
	finalized map[string]bool // manifest paths with a recorded terminal state
}

// New builds a Reviewer.
func New(o Options) *Reviewer {
	set := map[model.Category]bool{}
	cats := o.Cfg.BlockingCategories
	if len(cats) == 0 {
		// An unset blocking set means the defaults; a repository may shrink
		// it and may never grow it (§8). (A deliberately empty configured
		// set is indistinguishable from unset here — recorded as a known
		// edge of the config representation.)
		cats = config.DefaultBlockingCategories()
	}
	for _, c := range cats {
		set[c] = true
	}
	return &Reviewer{
		o:           o,
		blockingSet: set,
		retryLeft: map[string]int{
			unitTriage: defaultRetriesPerUnitType,
			unitReview: defaultRetriesPerUnitType,
			unitVerify: defaultRetriesPerUnitType,
		},
		finalized: map[string]bool{},
	}
}

const (
	unitTriage = "triage"
	unitReview = "review"
	unitVerify = "verify"

	// defaultRetriesPerUnitType: ONE run-global token bucket per unit type,
	// not per-call budgets — 400 call sites × 3 retries each is 1,200
	// provider calls (§7).
	defaultRetriesPerUnitType = 3
)

func (r *Reviewer) logf(format string, args ...any) {
	if r.o.Logger != nil {
		r.o.Logger(format, args...)
	}
}

// tryRetry consumes one retry token from the run-global bucket for the unit
// type; false means exhausted.
func (r *Reviewer) tryRetry(unit string) bool {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	if r.retryLeft[unit] <= 0 {
		return false
	}
	r.retryLeft[unit]--
	return true
}

// roleSettings resolves effective role configuration with the §7 defaults:
// timeouts are per role because a slow local model and a fast hosted one
// cannot share one number.
func (r *Reviewer) roleSettings(role model.Role, defTimeout time.Duration, defConcurrency, defMaxTokens int) (timeout time.Duration, concurrency, maxTokens int) {
	timeout, concurrency, maxTokens = defTimeout, defConcurrency, defMaxTokens
	if r.o.Cfg == nil {
		return
	}
	spec, ok := r.o.Cfg.Roles[role]
	if !ok {
		return
	}
	if spec.Timeout != "" {
		if d, err := time.ParseDuration(spec.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	if spec.Concurrency > 0 {
		concurrency = spec.Concurrency
	}
	if spec.MaxOutputTokens > 0 {
		maxTokens = spec.MaxOutputTokens
	}
	return
}

// completeWithRetry performs one bounded model call. Deterministic failures
// are terminal (a truncated response truncates identically on retry);
// transient failures consume from the run-global bucket. The per-request
// deadline comes from the role config, set at this call site — never
// inherited from an SDK.
func (r *Reviewer) completeWithRetry(ctx context.Context, unit string, req model.CompletionRequest, timeout time.Duration) (*model.CompletionResponse, error) {
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := r.o.Client.Complete(cctx, req)
		cancel()
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, model.ErrDeterministic) {
			return nil, err // terminal: no retry
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // run canceled or expired: not retried here
		}
		if !r.tryRetry(unit) {
			r.logf("%s call failed permanently after %d attempt(s), retries exhausted: %v", unit, attempt+1, err)
			return nil, err
		}
		r.logf("%s call failed (%v); retrying from run-global bucket", unit, err)
	}
}

type hasher struct {
	h interface {
		Write(p []byte) (int, error)
		Sum(b []byte) []byte
	}
}

func newHasher() *hasher { return &hasher{h: sha256.New()} }

func (w *hasher) write(s string) { _, _ = w.h.Write([]byte(s)) }
func (w *hasher) writeBytes(b []byte) {
	_, _ = w.h.Write(b)
	_, _ = w.h.Write([]byte{0})
}
func (w *hasher) hex() string { return hex.EncodeToString(w.h.Sum(nil)) }

// Run executes the review pass and returns the run record plus any
// run-level error (e.g. context cancellation). Partial results are present
// in the record even when an error is returned: a killed run still reports
// which files it read.
func (r *Reviewer) Run(ctx context.Context, in Inputs) (*model.RunRecord, error) {
	if r.o.Cfg == nil {
		return nil, errors.New("reviewer: Options.Cfg is required")
	}
	if r.o.Client == nil {
		return nil, errors.New("reviewer: Options.Client is required")
	}
	r.runCtx = ctx

	rec := &model.RunRecord{
		SchemaVersion: model.SchemaVersion,
		Model:         r.o.Client.ModelID(),
		Temperature:   pinnedTemperature,
		InputHash:     inputHash(&in),
		Samples:       1, // a green is one sample (§8)
	}
	if r.o.Instr != nil {
		rec.InstructionsUsed = r.o.Instr.Usage()
	}

	// Segment B is built once: byte-identical across every call in the run
	// (manifest + PR description + nonce + repo instructions).
	r.segB = r.contextSegment(&in)

	// Classify skips and split out deletions before any model call (§7):
	// generated files, lockfiles, vendored trees, minified output, binaries,
	// paths_ignore — each with its named reason. A skip is never a pass.
	var reviewable []scope.ManifestEntry
	for _, e := range in.Manifest {
		fo := model.FileOutcome{Path: e.Path, OldPath: e.OldPath, Status: e.Status}
		switch {
		case e.Status == "D":
			// A deleted file has no post-change artifact to review and no
			// post-change anchor: its content appears only as removed
			// lines, which can never be commented on (§7). It is recorded
			// as reviewed with zero findings so coverage arithmetic stays
			// exact without burning a model call on an empty envelope.
			fo.State = model.FileReviewed
			fo.Reviewed = true
			r.recordFile(rec, fo)
		default:
			if reason, ok := scope.SkipReason(e.Path, in.PostImage[e.Path], r.o.Cfg.PathsIgnore); ok {
				fo.State = model.FileSkipped
				fo.Reason = reason
				r.recordFile(rec, fo)
				continue
			}
			if in.PostImage[e.Path] == nil {
				fo.State = model.FileErrored
				fo.Reason = "missing_post_image"
				r.recordFile(rec, fo)
				continue
			}
			reviewable = append(reviewable, e)
		}
	}

	// One cheap whole-diff triage call FIRST, serialized (§7). On failure
	// or unusable output, fall back to reviewing all files batched — which
	// is also the unflagged-file default, so fallback costs ordering only.
	flagged, usable := r.runTriage(ctx, &in)
	var flaggedEntries []scope.ManifestEntry
	for _, e := range reviewable {
		if flagged[e.Path] {
			flaggedEntries = append(flaggedEntries, e)
		}
	}
	if !usable {
		flaggedEntries = reviewable
	}

	// Above 40 flagged files Cite risk-ranks and reviews the top N by added
	// source lines, and says so in one line of the review body — never
	// silently (§7). Cut entries are recorded skipped(risk_rank_cutoff),
	// which is deliberately NOT an approved skip: they were not reviewed.
	if scope.ShouldRiskRank(flaggedEntries) {
		review, cut := scope.RankForReview(flaggedEntries, scope.RiskRankCutoff)
		for _, e := range cut {
			r.recordFile(rec, model.FileOutcome{
				Path: e.Path, OldPath: e.OldPath, Status: e.Status,
				State: model.FileSkipped, Reason: scope.SkipReasonRiskCutoff,
			})
		}
		rec.RiskRanked = true
		rec.RiskRankedNote = scope.RiskRankedNote(len(review), len(flaggedEntries))
		flaggedSet := map[string]bool{}
		for _, e := range review {
			flaggedSet[e.Path] = true
		}
		var ordered []scope.ManifestEntry
		for _, e := range review {
			ordered = append(ordered, e)
		}
		for _, e := range reviewable {
			if !flaggedSet[e.Path] && !flagged[e.Path] {
				ordered = append(ordered, e) // unflagged: safer-default batched review
			}
		}
		reviewable = ordered
	} else {
		// Flagged first (triage priority), then unflagged — both reviewed.
		flaggedSet := map[string]bool{}
		for _, e := range flaggedEntries {
			flaggedSet[e.Path] = true
		}
		var ordered []scope.ManifestEntry
		for _, e := range flaggedEntries {
			ordered = append(ordered, e)
		}
		for _, e := range reviewable {
			if !flagged[e.Path] {
				ordered = append(ordered, e)
			}
		}
		reviewable = ordered
	}

	// Serialize the FIRST frontier call (§7: a cache entry becomes
	// available once the first response begins; fan out before that and
	// every concurrent request pays a cache write). We await its completion
	// before dispatching anyone else.
	var runErr error
	if len(reviewable) > 0 {
		first := reviewable[0]
		runErr = r.reviewFile(ctx, &in, rec, first)
		r.markFinalized(first.Path)

		// Proceed with the rest unless the RUN itself is dead. One dead
		// unit must not kill the run: reviewFile returns an error only on
		// cancellation.
		if runErr == nil || ctx.Err() == nil {
			rest := reviewable[1:]
			_, concurrency, _ := r.roleSettings(model.RoleReview, config.DefaultReviewTimeout, config.DefaultReviewConcurrency, defaultReviewMaxTokens)
			for start := 0; start < len(rest) && runErr == nil; start += batchSize {
				end := start + batchSize
				if end > len(rest) {
					end = len(rest)
				}
				batch := rest[start:end]
				runErr = r.runBatch(ctx, &in, rec, batch, concurrency)
			}
		}
	}

	// Every manifest path reaches exactly one terminal state — no fourth
	// state and no absence (§7). Anything left un-finalized by cancellation
	// is recorded errored(canceled); the gate fails closed on errors.
	for _, e := range in.Manifest {
		r.finalizeDefault(rec, e, "canceled")
	}

	rec.Coverage = scope.ComputeCoverage(rec.Files, len(in.Manifest))
	sort.SliceStable(rec.Files, func(i, j int) bool { return rec.Files[i].Path < rec.Files[j].Path })
	return rec, runErr
}

// runBatch processes one batched wave of ~6 files with the configured
// concurrency. A worker pool (not a launch-then-semaphore) keeps dispatch
// order FIFO: with concurrency 1 the wave order is exactly manifest order,
// which makes the triage-first shape observable. Batch barriers keep partial
// degradation local: a dead unit costs its own retry, not the run (§7).
func (r *Reviewer) runBatch(ctx context.Context, in *Inputs, rec *model.RunRecord, batch []scope.ManifestEntry, concurrency int) error {
	if concurrency < 1 {
		concurrency = 1
	}
	idx := make(chan int)
	var (
		mu      sync.Mutex
		firstEr error
	)
	var wg sync.WaitGroup
	workers := concurrency
	if workers > len(batch) {
		workers = len(batch)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				e := batch[i]
				err := r.reviewFile(ctx, in, rec, e)
				r.markFinalized(e.Path)
				if err != nil {
					mu.Lock()
					if firstEr == nil {
						firstEr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := range batch {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return firstEr
}

// segB is built once in Run: the per-run cached context segment.

func (r *Reviewer) reviewFile(ctx context.Context, in *Inputs, rec *model.RunRecord, e scope.ManifestEntry) error {
	if ctx.Err() != nil {
		r.recordFile(rec, model.FileOutcome{
			Path: e.Path, OldPath: e.OldPath, Status: e.Status,
			State: model.FileErrored, Reason: "canceled",
		})
		return ctx.Err()
	}

	fc, env := buildFileContext(e, in)
	timeout, _, maxTokens := r.roleSettings(model.RoleReview, config.DefaultReviewTimeout, config.DefaultReviewConcurrency, defaultReviewMaxTokens)

	// Per-file payload: exactly one code artifact (§7). It is derived from
	// scope.BuildEnvelope output so the rendering lives in one place: the
	// envelope minus its manifest+pr_description prefix is precisely the
	// <file_under_review>/<removed_lines> sections. Those go AFTER the
	// cache-breakpoint marker; segment B (r.segB) carries the manifest,
	// the nonce-carrying PR description and the repo instructions and is
	// byte-identical for every call in this run.
	full := strings.TrimSuffix(scope.BuildEnvelope(in.Manifest, in.PRDescription, in.Nonce, env), "\n")
	prefix := strings.TrimSuffix(scope.BuildEnvelope(in.Manifest, in.PRDescription, in.Nonce, nil), "\n")
	payload := strings.TrimPrefix(full, prefix+"\n\n")

	req := model.CompletionRequest{
		System:          systemPrompt(), // segment A: stable across runs and repos
		User:            r.segB + cacheBreakpoint + payload,
		MaxOutputTokens: maxTokens, // bounded by an output-token cap, never an inactivity timeout (§7)
		Temperature:     pinnedTemperature,
		ResponseSchema:  reviewResponseSchema(),
	}
	resp, err := r.completeWithRetry(ctx, unitReview, req, timeout)
	if err != nil {
		reason := "model_error"
		if errors.Is(err, model.ErrDeterministic) {
			reason = "deterministic_failure"
		} else if ctx.Err() != nil {
			reason = "canceled"
		} else if errors.Is(err, model.ErrDeadline) {
			reason = "deadline_exceeded"
		}
		r.logf("review of %s ended in error: %v", e.Path, err)
		r.recordFile(rec, model.FileOutcome{
			Path: e.Path, OldPath: e.OldPath, Status: e.Status,
			State: model.FileErrored, Reason: reason,
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil // one dead unit must not kill the run (partial results §7)
	}

	fr, perr := model.ParseFileReview([]byte(resp.Text))
	if perr != nil || (fr.Path != "" && fr.Path != e.Path) {
		// A schema violation or wrong-path echo is terminal for this unit:
		// re-asking invites a matching quote for the same wrong claim (§8).
		detail := fmt.Sprintf("parse failure: %v", perr)
		if perr == nil {
			detail = fmt.Sprintf("response echoes path %q, want %q", fr.Path, e.Path)
		}
		r.logf("review of %s unusable: %s", e.Path, detail)
		r.recordFile(rec, model.FileOutcome{
			Path: e.Path, OldPath: e.OldPath, Status: e.Status,
			State: model.FileErrored, Reason: "parse_failure",
		})
		return nil
	}

	findings, drops := r.validateFindings(fc, fr)
	r.recordDrops(rec, drops)
	r.recordFindings(rec, findings)
	r.recordFile(rec, model.FileOutcome{
		Path: e.Path, OldPath: e.OldPath, Status: e.Status,
		State:    model.FileReviewed,
		Reviewed: true,
		Findings: len(findings),
	})
	return nil
}

func (r *Reviewer) recordFile(rec *model.RunRecord, fo model.FileOutcome) {
	r.filesMu.Lock()
	defer r.filesMu.Unlock()
	rec.Files = append(rec.Files, fo)
	if fo.State != "" {
		r.finalized[fo.Path] = true
	}
}

func (r *Reviewer) markFinalized(path string) {
	r.filesMu.Lock()
	defer r.filesMu.Unlock()
	r.finalized[path] = true
}

// finalizeDefault gives any manifest path without a terminal state an
// errored one, preserving the §7 invariant when a run dies mid-flight.
func (r *Reviewer) finalizeDefault(rec *model.RunRecord, e scope.ManifestEntry, reason string) {
	r.filesMu.Lock()
	defer r.filesMu.Unlock()
	if r.finalized[e.Path] {
		return
	}
	r.finalized[e.Path] = true
	rec.Files = append(rec.Files, model.FileOutcome{
		Path: e.Path, OldPath: e.OldPath, Status: e.Status,
		State: model.FileErrored, Reason: reason,
	})
}

func (r *Reviewer) recordFindings(rec *model.RunRecord, fs []model.ValidatedFinding) {
	r.filesMu.Lock()
	defer r.filesMu.Unlock()
	rec.Findings = append(rec.Findings, fs...)
}

func (r *Reviewer) recordDrops(rec *model.RunRecord, ds []model.DropEntry) {
	r.filesMu.Lock()
	defer r.filesMu.Unlock()
	rec.Drops = append(rec.Drops, ds...)
}
