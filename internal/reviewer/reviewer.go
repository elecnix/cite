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

	usageMu sync.Mutex
	usage   model.Usage // run-total of every completion response's counters (§15)

	callMu   sync.Mutex
	runStart time.Time
	callLog  []model.CallEntry // one entry per model call attempt, for forensics
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

// accumulateUsage folds one response's token counters into the run total.
// Calls arrive concurrently from the review worker pool, so the total is
// mutex-protected like the other run-scoped state.
func (r *Reviewer) accumulateUsage(u model.Usage) {
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	r.usage.InputTokens += u.InputTokens
	r.usage.OutputTokens += u.OutputTokens
	r.usage.CacheReadTokens += u.CacheReadTokens
	r.usage.CacheWriteTokens += u.CacheWriteTokens
}

// totalUsage returns the accumulated run-total.
func (r *Reviewer) totalUsage() model.Usage {
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	return r.usage
}

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

// recordCall appends one per-attempt entry to the run's call log. The log
// answers the forensics question the raw CI log cannot: which calls ran, when,
// how long each took, and what each cost. A run killed mid-flight still
// carries the entries recorded so far — Run attaches the log to the record it
// returns, and the GitHub Action archives that record on failure.
func (r *Reviewer) recordCall(unit string, attempt int, start time.Time, dur time.Duration, resp *model.CompletionResponse, err error) {
	e := model.CallEntry{
		Unit:      unit,
		Attempt:   attempt + 1,
		StartS:    start.Sub(r.runStart).Seconds(),
		DurationS: dur.Seconds(),
		Outcome:   model.CallOK,
	}
	switch {
	case err != nil && errors.Is(err, model.ErrDeadline):
		e.Outcome = model.CallDeadlineExceeded
		e.Error = err.Error()
	case err != nil && errors.Is(err, model.ErrDeterministic):
		e.Outcome = model.CallDeterministicFail
		e.Error = err.Error()
	case err != nil:
		if ctxErr := r.runCtx.Err(); ctxErr != nil {
			e.Outcome = model.CallCanceled
		} else {
			e.Outcome = model.CallError
		}
		e.Error = err.Error()
	case resp != nil:
		e.InputTokens = resp.Usage.InputTokens
		e.OutputTokens = resp.Usage.OutputTokens
		if resp.FinishReason == "length" {
			e.Outcome = model.CallTruncated
		}
	}
	r.callMu.Lock()
	defer r.callMu.Unlock()
	r.callLog = append(r.callLog, e)
}

// requireParameters reads the require_parameters knob off the loaded
// configuration. Cfg is required on Options, but the nil guard keeps a
// misconstructed Reviewer from panicking mid-run (mirrors roleSettings).
func requireParameters(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.RequireParameters
}

// roleSettings resolves effective role configuration with the §7 defaults:
// timeouts are per role because a slow local model and a fast hosted one
// cannot share one number.
//
// The output cap resolves most-specific-first, in three layers:
//
//  1. roles.<role>.max_output_tokens — an explicit operator instruction, and
//     it wins outright. Silently shrinking a number someone wrote down would
//     be exactly the kind of invisible behaviour change §6 forbids.
//  2. the resolved model entry's max_tokens — what the model says it can
//     emit. This both raises the cap on a roomy model and lowers it on a
//     narrow one, which is the only way Cite can know a ceiling it cannot
//     query.
//  3. the built-in default.
//
// The review deadline has no fixed default: when no explicit timeout is
// configured it derives from the SAME resolved cap (issue #28) via
// config.DerivedReviewTimeout, so a larger token budget buys proportionally
// more wall clock instead of dying at "context deadline exceeded". Triage and
// assemble keep their fixed defaults; they emit bounded output regardless of
// file size.
//
// A cap that is still too small truncates, and a truncation stays terminal
// and reported (model.ErrDeterministic → FileErrored → COULD_NOT_EVALUATE).
// Raising the ceiling must never turn a truncation into a silent partial
// review.
func (r *Reviewer) roleSettings(role model.Role, defTimeout time.Duration, defConcurrency, defMaxTokens int) (timeout time.Duration, concurrency, maxTokens int) {
	timeout, concurrency, maxTokens = defTimeout, defConcurrency, defMaxTokens
	explicitTimeout := false
	if r.o.Cfg == nil {
		return
	}
	if n := r.o.Cfg.ModelMaxTokens(role); n > 0 {
		maxTokens = n
	}
	spec, ok := r.o.Cfg.Roles[role]
	if ok {
		if spec.Timeout != "" {
			if d, err := time.ParseDuration(spec.Timeout); err == nil && d > 0 {
				timeout = d
				explicitTimeout = true
			}
		}
		if spec.Concurrency > 0 {
			concurrency = spec.Concurrency
		}
		if spec.MaxOutputTokens > 0 {
			maxTokens = spec.MaxOutputTokens
		}
	}
	if !explicitTimeout && role == model.RoleReview {
		// Issue #28: scale the deadline to the resolved output cap. An unset
		// cap (maxTokens <= 0) falls back to the built-in 32768-token default
		// inside DerivedReviewTimeout.
		timeout = config.DerivedReviewTimeout(maxTokens)
	}
	return
}

// completeWithRetry performs one bounded model call. Deterministic failures
// are terminal (a truncated response truncates identically on retry);
// transient failures consume from the run-global bucket. A deadline expiry is
// ALSO terminal: re-issuing a call that just burned its whole wall-clock
// budget doubles the cost for an outcome the operator cannot distinguish
// from waiting — the provider charged for the first attempt, and a second
// attempt is money spent to lose money slower. The failure names the knob
// that raises the deadline instead (issue #28). The per-request deadline
// comes from the role config, set at this call site — never inherited from an
// SDK.
func (r *Reviewer) completeWithRetry(ctx context.Context, unit string, req model.CompletionRequest, timeout time.Duration) (*model.CompletionResponse, error) {
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		callStart := time.Now()
		resp, err := r.o.Client.Complete(cctx, req)
		callDur := time.Since(callStart)
		cancel()
		r.recordCall(unit, attempt, callStart, callDur, resp, err)
		if err == nil {
			r.accumulateUsage(resp.Usage)
			return resp, nil
		}
		if errors.Is(err, model.ErrDeterministic) {
			return nil, err // terminal: no retry
		}
		if errors.Is(err, model.ErrDeadline) {
			// Terminal, never retried: the first attempt already burned its
			// full time budget (and the provider's tokens). Name the knob so
			// the fix is one line away (issue #28; truthful remedy strings
			// for both the derived and the explicit case, issue #59).
			r.logf("%s call exceeded its %s per-call deadline; NOT retrying — a timeout retry would pay twice for the same wait. If calls legitimately need longer, set an explicit roles.%s.timeout in .github/cite.yml (the built-in review deadline only derives from roles.%s.max_output_tokens when no explicit timeout is set: 60s + tokens/128, so RAISE the cap for a longer derived deadline — lowering it tightens the deadline)", unit, timeout, unit, unit)
			return nil, err
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
	r.runStart = time.Now()

	rec := &model.RunRecord{
		SchemaVersion: model.SchemaVersion,
		Model:         r.o.Client.ModelID(),
		Temperature:   pinnedTemperature,
		InputHash:     inputHash(&in),
		Samples:       1, // a green is one sample (§8)
	}
	// Name the provider and model up front: a run that dies mid-flight
	// (deadline expiry, provider outage) never reaches the end-of-run record
	// dump, and its failure logs must still say what they were talking to
	// (issue #28; PR elecnix/pi-agent-identity#45).
	if pd, ok := r.o.Client.(model.ProviderDescriber); ok {
		r.logf("provider=%s model=%s", pd.DescribeProvider(), r.o.Client.ModelID())
	} else {
		r.logf("model=%s", r.o.Client.ModelID())
	}
	// A pinned timeout tighter than the default is honoured, never
	// overridden — the operator gets one info line instead, so a pin
	// calibrated before the 15-minute defaults is at least visible in the CI
	// output of the run it makes stricter.
	for _, line := range r.o.Cfg.TimeoutAdvisories() {
		r.logf("info: %s", line)
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
		// The unflagged check is flaggedSet (the flagged-priority set), not
		// flagged: on triage failure the fallback sets flaggedEntries =
		// reviewable while flagged stays nil, so testing flagged here would
		// append every file a second time and pay for every review twice.
		flaggedSet := map[string]bool{}
		for _, e := range flaggedEntries {
			flaggedSet[e.Path] = true
		}
		var ordered []scope.ManifestEntry
		for _, e := range flaggedEntries {
			ordered = append(ordered, e)
		}
		for _, e := range reviewable {
			if !flaggedSet[e.Path] {
				ordered = append(ordered, e) // unflagged: safer-default batched review
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
			_, concurrency, _ := r.roleSettings(model.RoleReview, 0 /* review deadline derives from the output cap inside roleSettings (issue #28) */, config.DefaultReviewConcurrency, defaultReviewMaxTokens)
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
	rec.Usage = r.totalUsage()
	r.callMu.Lock()
	rec.Calls = r.callLog
	r.callMu.Unlock()
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

// explicitReviewTimeout reports whether the configuration sets a literal
// roles.review.timeout. Issue #59: a remedy string that says "your
// configured deadline" when nobody configured one (it derived from the
// output-token cap) — or that recommends lowering the cap, which tightens
// a derived deadline — names a dial that does not move, in exactly the
// case the operator needs to move one.
func explicitReviewTimeout(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	spec, ok := cfg.Roles[model.RoleReview]
	if !ok {
		return false
	}
	d, err := time.ParseDuration(spec.Timeout)
	return err == nil && d > 0
}

// deadlineRemedy is the remedy sentence carried by a deadline-exceeded
// error. It must say which deadline tripped and name only levers that
// exist and move in the direction the remedy implies (issue #59).
func deadlineRemedy(cfg *config.Config, timeout time.Duration) string {
	if explicitReviewTimeout(cfg) {
		return fmt.Sprintf("the call hit its configured %.0fs review deadline; raise roles.review.timeout in .github/cite.yml if calls legitimately need longer", timeout.Seconds())
	}
	// Derived: 60s base + max_output_tokens/128 tok/s (config.DerivedReviewTimeout).
	// The cap RAISES the deadline; lowering it would tighten the very
	// deadline that tripped.
	return fmt.Sprintf("the call hit its %.0fs review deadline (derived, not configured: 60s + roles.review.max_output_tokens/128); set an explicit roles.review.timeout in .github/cite.yml for a deadline the cap cannot move, or RAISE the output-token cap if the model was legitimately still generating — lowering the cap tightens this deadline", timeout.Seconds())
}

func (r *Reviewer) reviewFile(ctx context.Context, in *Inputs, rec *model.RunRecord, e scope.ManifestEntry) error {
	if ctx.Err() != nil {
		r.recordFile(rec, model.FileOutcome{
			Path: e.Path, OldPath: e.OldPath, Status: e.Status,
			State: model.FileErrored, Reason: "canceled",
		})
		return ctx.Err()
	}

	fc, env := buildFileContext(e, in)
	timeout, _, maxTokens := r.roleSettings(model.RoleReview, 0 /* review deadline derives from the output cap inside roleSettings (issue #28) */, config.DefaultReviewConcurrency, defaultReviewMaxTokens)

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
		System:            systemPrompt(), // segment A: stable across runs and repos
		User:              r.segB + cacheBreakpoint + payload,
		MaxOutputTokens:   maxTokens, // bounded by an output-token cap, never an inactivity timeout (§7)
		Temperature:       pinnedTemperature,
		ResponseSchema:    reviewResponseSchema(),
		RequireParameters: requireParameters(r.o.Cfg),
	}
	var fr *model.FileReview
	var perr error
	for {
		resp, err := r.completeWithRetry(ctx, unitReview, req, timeout)
		if err != nil {
			reason := "model_error"
			if errors.Is(err, model.ErrDeterministic) {
				reason = "deterministic_failure"
			} else if ctx.Err() != nil {
				reason = "canceled"
			} else if errors.Is(err, model.ErrDeadline) {
				reason = "deadline_exceeded"
				// Name the knob, not just the symptom (issue #28), and be
				// truthful about WHICH deadline tripped (issue #59): an
				// explicit roles.review.timeout is what the operator
				// configured, and the token cap drives nothing for it; a
				// derived deadline is nobody's configured value and GROWS
				// with the cap (60s + tokens/128), so "lower the cap"
				// would tighten the very deadline that tripped.
				err = fmt.Errorf("%w: %s", err, deadlineRemedy(r.o.Cfg, timeout))
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

		fr, perr = model.ParseFileReview([]byte(resp.Text))
		syntaxErr := perr != nil && errors.Is(perr, model.ErrSyntax)
		blankBody := strings.TrimSpace(resp.Text) == ""
		// Issue #59: a schema-level failure (wrong schema_version, unknown
		// outcome, an anchor out of range, or a valid review naming the
		// wrong file) is also a mechanical failure of the output format,
		// not a confident wrong answer about the code. The §8
		// never-re-quote rule is about re-asking the model to re-quote
		// inside a validated finding — a complete fresh re-request faces
		// the full validation pipeline and cannot launder a bad claim
		// through a matching quote. These failures carry no findings for
		// the file under review, so a bounded re-ask (same run-global
		// bucket, same exhaustion point as every transient failure) is
		// taken before going terminal.
		if perr == nil && fr != nil && fr.Path == e.Path {
			break
		}
		// A blank body is transient provider garbage, a syntax error is a
		// mechanical formatting artifact (single-quoted keys, truncated
		// output), and a schema error is the model breaking its own
		// output contract: none carries a validated finding, so a re-ask
		// invites nothing back (§8's grounding concern does not reach
		// here). All draw from the same run-global bucket as other
		// transient failures; with retries exhausted they stay terminal as
		// parse_failure. (Deadline expiry draws from no bucket at all:
		// it is terminal without a retry — see completeWithRetry.)
		if !r.tryRetry(unitReview) {
			break
		}
		kind := "was empty"
		if !blankBody {
			if syntaxErr {
				kind = "failed strict JSON decode"
			} else if perr != nil {
				kind = "violated the review schema"
			} else {
				kind = fmt.Sprintf("echoed path %q", fr.Path)
			}
		}
		r.logf("review of %s response %s (%v); retrying from run-global bucket", e.Path, kind, perr)
	}

	if perr != nil || (fr.Path != "" && fr.Path != e.Path) {
		// Terminal once the bucket is exhausted: this unit ends errored
		// (parse_failure), coverage stays incomplete, the gate fails
		// closed — a re-ask beyond the bound would convert a bounded cost
		// into an unbounded one (issue #59).
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
