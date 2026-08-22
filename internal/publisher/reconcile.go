// Fingerprint reconciliation (§10): findings have identity, and running on
// every push must not mean N copies of every finding.
//
// The fingerprint is content-addressed, not line-addressed, so it survives a
// rebase. Path is a locator, not part of the fingerprint — otherwise a rename
// re-raises every finding in the file. Exact match first, then a fuzzy
// fallback on (category, title) with span similarity above 0.6. Two identical
// findings in one file get an occurrence ordinal, and reconciliation is a
// greedy matching problem rather than an equality lookup.
package publisher

import (
	"sort"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/model"
)

// FuzzyMatchThreshold is the span-similarity bar for the fuzzy fallback.
// Churn is made cheap instead of chasing perfect stability: the failure mode
// of a loose threshold is one duplicate comment, not a silent carry-forward
// loss.
const FuzzyMatchThreshold = 0.6

// LiveThread is the reconciler's view of one GitHub review thread.
type LiveThread struct {
	ID          int64
	Fingerprint string
	Path        string
	IsOutdated  bool
	// ResolvedByHuman: a human resolved this thread. Cite never re-opens,
	// re-resolves or re-posts against it; human adjudication wins.
	ResolvedByHuman bool
}

// ReconciliationPlan is what the publisher executes. Every step is safe to
// redo (§10, "Publishing is ordered so every step is safe to redo").
type ReconciliationPlan struct {
	// CommentsToPost: only NEW fingerprints. Invariant (§10): replaying the
	// same pull request twice posts zero new threads the second time.
	CommentsToPost []model.ValidatedFinding
	// ThreadsToResolve: ONLY when the underlying span is verified gone from
	// the new file content (the caller supplies SpanGone). A merely-
	// disappeared fingerprint between pushes is NOT resolution — an attacker
	// reformatting a file must not silently clear a real finding.
	ThreadsToResolve []int64
	// ThreadsToMinimise: outdated threads whose finding still stands.
	ThreadsToMinimise []int64
	// SuppressedByLedger: dismissed fingerprints are not re-raised, but they
	// appear here so the gate verdict is unchanged — a dismissal never clears
	// the current gate (§12).
	SuppressedByLedger []model.ValidatedFinding
}

// ReconcileOptions carries the caller-supplied facts Reconcile must not guess.
type ReconcileOptions struct {
	// Repository scopes ledger lookups; dismissals never cross repositories.
	Repository string
	// Now is the reconciliation instant for ledger expiry. Zero means
	// time.Now at call time.
	Now time.Time
	// SpanGone reports whether the quoted span of a live thread is verified
	// gone from the new file content. It is called only for threads whose
	// fingerprint no current finding matches. Nil means "cannot verify":
	// no thread is ever resolved on an unverifiable basis (fail toward
	// keeping the thread, never toward clearing it).
	SpanGone func(LiveThread) bool
}

// Reconcile computes the plan. Matching is greedy and documented:
//
//  1. Exact fingerprint match, preferring the same path (a rename must not
//     orphan a thread, so path is a preference, never a requirement).
//     Identical findings in one file are paired with identical threads in
//     thread-ID order — the occurrence ordinal.
//  2. Fuzzy fallback among the leftovers: same category and token-Jaccard
//     similarity of (evidence quotes + title) above FuzzyMatchThreshold,
//     highest score first, ties preferring the same path then the lowest
//     thread ID.
//
// Unmatched current findings then split: ledger-dismissed fingerprints go to
// SuppressedByLedger (not re-raised, gate unchanged); everything else is new
// and lands in CommentsToPost with occurrence ordinals assigned.
//
// Unmatched live threads (never human-resolved ones) resolve only when
// SpanGone verifies the span is gone. Matched threads that GitHub marks
// outdated are minimised.
func Reconcile(current []model.ValidatedFinding, live []LiveThread, ledger DismissalLedger, opts ReconcileOptions) ReconciliationPlan {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	plan := ReconciliationPlan{}
	used := make([]bool, len(live))

	// --- pass 1: exact fingerprint match ---------------------------------
	// Queue of unused thread indices per fingerprint, in thread-ID order so
	// duplicate findings pair deterministically with duplicate threads.
	byFP := map[string][]int{}
	for j, t := range live {
		if t.ResolvedByHuman {
			continue
		}
		byFP[t.Fingerprint] = append(byFP[t.Fingerprint], j)
	}
	for fp := range byFP {
		js := byFP[fp]
		sort.Slice(js, func(a, b int) bool { return live[js[a]].ID < live[js[b]].ID })
		byFP[fp] = js
	}

	takeExact := func(f model.ValidatedFinding) int {
		fp := fingerprintOf(f)
		js := byFP[fp]
		// Prefer an unused thread on the same path; fall back to any path so
		// a rename does not orphan the thread.
		for _, prefer := range []func(int) bool{
			func(j int) bool { return live[j].Path == f.Path },
			func(int) bool { return true },
		} {
			for _, j := range js {
				if used[j] || !prefer(j) {
					continue
				}
				used[j] = true
				return j
			}
		}
		return -1
	}

	matched := make([]bool, len(current)) // finding matched a live thread
	for i, f := range current {
		if j := takeExact(f); j >= 0 {
			matched[i] = true
			if live[j].IsOutdated {
				plan.ThreadsToMinimise = append(plan.ThreadsToMinimise, live[j].ID)
			}
		}
	}

	// --- pass 2: fuzzy fallback on (category, normalized text) -----------
	type pair struct {
		i, j  int
		score float64
	}
	var candidates []pair
	for i, f := range current {
		if matched[i] {
			continue
		}
		ft := tokenSet(f)
		for j, t := range live {
			if used[j] || t.ResolvedByHuman {
				continue
			}
			tf := threadTokens(t, f)
			if f.Category != categoryOfThread(t, f) {
				// Category is part of the fuzzy key: a crash must never
				// silently absorb a convention nit's thread.
				continue
			}
			if s := jaccard(ft, tf); s > FuzzyMatchThreshold {
				candidates = append(candidates, pair{i, j, s})
			}
		}
	}
	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].score != candidates[b].score {
			return candidates[a].score > candidates[b].score
		}
		if live[candidates[a].j].Path != live[candidates[b].j].Path {
			return live[candidates[a].j].Path < live[candidates[b].j].Path
		}
		return live[candidates[a].j].ID < live[candidates[b].j].ID
	})
	for _, c := range candidates {
		if matched[c.i] || used[c.j] {
			continue
		}
		matched[c.i] = true
		used[c.j] = true
		if live[c.j].IsOutdated {
			plan.ThreadsToMinimise = append(plan.ThreadsToMinimise, live[c.j].ID)
		}
	}

	// --- split the unmatched findings ------------------------------------
	for i, f := range current {
		if matched[i] {
			continue
		}
		if ledger.Active(fingerprintOf(f), opts.Repository, now) {
			// Not re-raised — but listed, so the gate verdict is unchanged.
			// A dismissal never clears the current gate (§12).
			plan.SuppressedByLedger = append(plan.SuppressedByLedger, f)
			continue
		}
		plan.CommentsToPost = append(plan.CommentsToPost, f)
	}
	AssignOccurrences(plan.CommentsToPost)

	// --- threads whose finding is gone -----------------------------------
	for j, t := range live {
		if used[j] || t.ResolvedByHuman {
			continue
		}
		if opts.SpanGone != nil && opts.SpanGone(t) {
			plan.ThreadsToResolve = append(plan.ThreadsToResolve, t.ID)
		}
		// else: the fingerprint merely disappeared between pushes. Not
		// resolution. The thread stays open.
	}
	return plan
}

// AssignOccurrences numbers identical findings within one file 1..k so each
// gets its own thread identity. Stable: the input order decides who is #1.
func AssignOccurrences(findings []model.ValidatedFinding) {
	type key struct{ path, fp string }
	counts := map[key]int{}
	for i := range findings {
		k := key{findings[i].Path, fingerprintOf(findings[i])}
		counts[k]++
		findings[i].Occurrence = counts[k]
	}
}

func fingerprintOf(f model.ValidatedFinding) string {
	if f.Fingerprint != "" {
		return f.Fingerprint
	}
	return f.FingerprintOf()
}

// tokenSet extracts the comparison tokens for a finding: normalized evidence
// quotes plus the normalized title.
func tokenSet(f model.ValidatedFinding) map[string]bool {
	var sb strings.Builder
	for _, e := range f.Evidence {
		sb.WriteString(e.Quote)
		sb.WriteString("\n")
	}
	sb.WriteString(f.Title)
	return tokensOf(model.NormalizeForFingerprint(sb.String()))
}

// threadTokens builds the same token set from a live thread. The thread
// carries only its fingerprint, so the caller supplies the current finding it
// is being compared against for category/evidence; the title tokens come from
// that finding's stored title via the fingerprint side-channel is impossible —
// instead the caller passes the finding and we use the thread's fingerprint
// only as an opaque identity. Fuzzy matching therefore compares the current
// finding's tokens with the candidate thread's *sibling current finding*
// tokens when available. In practice the publisher keeps a side map from
// thread fingerprint to the finding text it was posted with; where that is
// absent (rebuilt threads), fuzzy matching falls back to comparing against
// the finding's own tokens, which cannot exceed the threshold and so never
// falsely matches.
func threadTokens(t LiveThread, against model.ValidatedFinding) map[string]bool {
	if side := sideChannelTokens(t.Fingerprint); side != nil {
		return side
	}
	// No recorded text: an empty set yields Jaccard 0, so the thread can
	// never fuzzy-match. Exact fingerprints still match. This is the
	// documented greedy-matching trade-off: one duplicate comment rather
	// than a silent loss.
	return map[string]bool{}
}

// categoryOfThread returns the category to compare for fuzzy matching. See
// threadTokens: without recorded text there is no category either, and the
// empty-token rule already prevents a match.
func categoryOfThread(t LiveThread, against model.ValidatedFinding) model.Category {
	if c, ok := sideChannelCategory(t.Fingerprint); ok {
		return c
	}
	return against.Category
}

// --- side channel ---------------------------------------------------------
//
// GitHub review threads do not expose arbitrary metadata, so the publisher
// records the category and normalized token set of every fingerprint it
// posts, in-process, to make the fuzzy fallback workable on later pushes
// where the thread body is parsed by the caller and registered here. This is
// pure in-process bookkeeping for the pure core; the network wave wires the
// Register call from parsed thread bodies.

type threadSide struct {
	tokens   map[string]bool
	category model.Category
}

var threadSideChannel = map[string]threadSide{}

// RegisterThreadText records the category and token set behind a fingerprint,
// enabling the fuzzy fallback for that thread. Idempotent.
func RegisterThreadText(fingerprint string, category model.Category, evidenceQuotes []string, title string) {
	var sb strings.Builder
	for _, q := range evidenceQuotes {
		sb.WriteString(q)
		sb.WriteString("\n")
	}
	sb.WriteString(title)
	threadSideChannel[fingerprint] = threadSide{
		tokens:   tokensOf(model.NormalizeForFingerprint(sb.String())),
		category: category,
	}
}

func sideChannelTokens(fp string) map[string]bool {
	if s, ok := threadSideChannel[fp]; ok {
		return s.tokens
	}
	return nil
}

func sideChannelCategory(fp string) (model.Category, bool) {
	if s, ok := threadSideChannel[fp]; ok {
		return s.category, true
	}
	return "", false
}

// tokensOf splits normalized text into its token set.
func tokensOf(normalized string) map[string]bool {
	set := map[string]bool{}
	for _, tok := range strings.Fields(normalized) {
		set[tok] = true
	}
	return set
}

// jaccard is the token-Jaccard similarity |A∩B| / |A∪B|. Two empty sets are
// dissimilar (0), never identical.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for tok := range a {
		if b[tok] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
