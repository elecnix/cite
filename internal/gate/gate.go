// Package gate implements PLAN §11: a three-state, fail-closed merge gate.
//
// The worst failure of a required check is not red — it is absent. So every
// path here terminates in exactly one of PASS, FOUND or
// COULD_NOT_EVALUATE, and only PASS concludes success. There is no neutral,
// no fail-open, and no fourth "probably fine" state.
package gate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// BypassLabel is the self-service break-glass label (§11). Any author can
// apply it; every use is loud and enumerable afterwards. The bypass buys
// time, not amnesty — bypassed merge commits are re-reviewed on the default
// branch by a scheduled job.
const BypassLabel = "cite-bypass"

// Options carries the inputs the gate must be told rather than guess.
// ProviderFailed and BudgetTripped come from job B's outcome; either forces
// COULD_NOT_EVALUATE regardless of what partial findings exist, because a
// run that died halfway cannot vouch for its own coverage.
type Options struct {
	ProviderFailed bool
	BudgetTripped  bool
}

// ApprovedSkipReasons is the closed set of skip reasons that count toward
// coverage. It mirrors scope's approved set; any other reason fails the
// gate, because a skipped file is not a reviewed file and "skipped" must
// never collapse into "clean".
var ApprovedSkipReasons = []string{
	scope.SkipReasonBinary,
	scope.SkipReasonVendored,
	scope.SkipReasonLockfile,
	scope.SkipReasonGenerated,
	scope.SkipReasonMinified,
	scope.SkipReasonIgnored,
	scope.SkipReasonOversized,
}

func approvedSkip(reason string) bool {
	for _, r := range ApprovedSkipReasons {
		if r == reason {
			return true
		}
	}
	return false
}

// Decide maps a run record onto one of the three §11 states, fail-closed:
//
//	COULD_NOT_EVALUATE — provider or budget failure, zero in-scope files
//	(the path-filter bypass shape), an errored file, an unapproved skip,
//	incomplete coverage, or samples < 1.
//	FOUND              — at least one finding blocks.
//	PASS               — every in-scope file reached a terminal reviewed or
//	approved-skip state, nothing blocks, and there was at least one sample.
//
// A nil rec fails closed. A nil cfg is treated as the default configuration.
// The cfg parameter selects nothing today (blocking is already computed in
// code on each finding); it is part of the contract so callers cannot
// silently drop configuration from the decision path later.
func Decide(rec *model.RunRecord, cfg *config.Config, opts Options) (model.Verdict, string) {
	_ = cfg // see doc comment: reserved on the decision path by contract.

	if rec == nil {
		return model.VerdictCouldNotEvaluate, "no run record reached the gate"
	}

	// Hard failures reported by the job that ran the review. The reason
	// recorded by the failing step is surfaced verbatim so the check summary
	// says what actually happened.
	if opts.ProviderFailed {
		return model.VerdictCouldNotEvaluate, nonEmpty(rec.VerdictReason, "provider unavailable")
	}
	if opts.BudgetTripped {
		return model.VerdictCouldNotEvaluate, nonEmpty(rec.VerdictReason, "budget tripped")
	}

	// A finding that survived validation and blocks decides the verdict
	// before coverage bookkeeping: the signal is stronger than the
	// accounting, and both states conclude failure anyway.
	for i := range rec.Findings {
		if rec.Findings[i].Blocks {
			return model.VerdictFound, blockingReason(&rec.Findings[i])
		}
	}

	cov := rec.Coverage

	// Zero in-scope files is never a pass (§11): a PR that changed files but
	// resolved to an empty in-scope set is exactly the shape of a
	// path-filter bypass, and going green having read nothing is the bug
	// this state exists to prevent.
	if cov.APIFiles <= 0 || scope.EmptyInScope(cov) {
		return model.VerdictCouldNotEvaluate, "zero in-scope files (path-filter bypass shape)"
	}

	// An errored file is terminal but not covered.
	var errored []string
	for _, f := range rec.Files {
		if f.State == model.FileErrored {
			errored = append(errored, f.Path)
		}
	}
	if len(errored) > 0 {
		return model.VerdictCouldNotEvaluate,
			fmt.Sprintf("file errored during review: %s", strings.Join(errored, ", "))
	}

	// A skipped file with an unexpected reason is not a reviewed file.
	var badSkips []string
	for _, f := range rec.Files {
		if f.State == model.FileSkipped && !approvedSkip(f.Reason) {
			badSkips = append(badSkips, fmt.Sprintf("%s (%s)", f.Path, f.Reason))
		}
	}
	if len(badSkips) > 0 {
		return model.VerdictCouldNotEvaluate,
			fmt.Sprintf("unapproved skip: %s", strings.Join(badSkips, ", "))
	}

	// Coverage arithmetic is computed in code, never attested (§12).
	if !cov.Complete {
		return model.VerdictCouldNotEvaluate, "coverage incomplete"
	}

	// A green is one sample (§8): zero samples means nothing ran.
	if rec.Samples < 1 {
		return model.VerdictCouldNotEvaluate, "no sample recorded"
	}

	return model.VerdictPass, "all in-scope files reviewed or approved-skipped; nothing blocks"
}

// DecideDisabled is the kill switch (§11): enabled=false makes the gate
// conclude success with "disabled by configuration". A kill switch produces
// a green check, never no check — one that stops the workflow converts a
// soft failure into a permanent merge freeze.
func DecideDisabled(cfg *config.Config) (model.Verdict, string) {
	_ = cfg // the kill switch ignores every other knob by design.
	return model.VerdictPass, "disabled by configuration"
}

// CheckRunPayload renders the GitHub check-run title and summary. The
// summary carries the one-sample line verbatim (a green is one observation),
// the state line, and the skipped-files aggregate when anything was skipped.
func CheckRunPayload(rec *model.RunRecord, verdict model.Verdict, reason string) (title, summary string) {
	title = fmt.Sprintf("cite: %s", verdict)

	var sb strings.Builder
	if rec != nil {
		sb.WriteString(rec.Summary())
		sb.WriteString("\n\n")
	}
	sb.WriteString(fmt.Sprintf("%s — %s", verdict, reason))

	if rec != nil {
		agg := skippedAggregate(rec.Files)
		if len(agg) > 0 {
			sb.WriteString("\n")
			reasons := make([]string, 0, len(agg))
			for r := range agg {
				reasons = append(reasons, r)
			}
			sort.Strings(reasons)
			for _, r := range reasons {
				fmt.Fprintf(&sb, "\nSkipped (%s): %d file(s): %s",
					r, len(agg[r]), strings.Join(agg[r], ", "))
			}
		}
	}
	return title, sb.String()
}

// BypassSummary renders the break-glass line: BYPASSED — <state> — @author —
// <run url>. It goes onto the concluding check and into the bypass log so
// "every pull request merged unreviewed on this date" stays a one-line query.
func BypassSummary(state model.Verdict, author, runURL string) string {
	return fmt.Sprintf("BYPASSED — %s — @%s — %s", state, author, runURL)
}

// PRState is the reaper's view of one open pull request.
type PRState struct {
	HeadSHA          string
	HasTerminalCheck bool
	AgeMinutes       int
}

// NeedsReaper returns the open PRs whose head SHA has had no terminal Cite
// check for at least staleThreshold minutes (§11: twenty). A stuck required
// check renders as "Expected — waiting for status to be reported" forever;
// the reaper writes a terminal failure ("run never reported") so the block
// self-heals into something a human can act on.
func NeedsReaper(openPRs []PRState, staleThreshold int) []PRState {
	var out []PRState
	for _, pr := range openPRs {
		if !pr.HasTerminalCheck && pr.AgeMinutes >= staleThreshold {
			out = append(out, pr)
		}
	}
	return out
}

func blockingReason(f *model.ValidatedFinding) string {
	if f == nil {
		return "a finding blocks"
	}
	return fmt.Sprintf("blocking %s finding: %s", f.Category, f.Title)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func skippedAggregate(files []model.FileOutcome) map[string][]string {
	agg := map[string][]string{}
	for _, f := range files {
		if f.State == model.FileSkipped {
			r := f.Reason
			if r == "" {
				r = "(unspecified)"
			}
			agg[r] = append(agg[r], f.Path)
		}
	}
	return agg
}
