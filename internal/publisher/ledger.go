// DismissalLedger: scoped, expiring records of human adjudication (§12).
//
// A dismissal is scoped to (fingerprint, repository) with a 90-day expiry.
// It tells the reconciler not to re-raise a thread — it NEVER changes the
// gate verdict for the current pull request. The ledger is a rebuildable
// cache persisted as a base64 JSON blob inside the sticky bot comment; if it
// is missing or corrupt it is rebuilt from the live threads (§10, "Where the
// state lives"). It is never the Actions cache and never a head-side file.
package publisher

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// LedgerExpiry: dismissals expire after 90 days. A stale dismissal stops
// suppressing and the finding re-raises on its next appearance.
const LedgerExpiry = 90 * 24 * time.Hour

// AuthorAssociation mirrors GitHub's author_association values for the
// dispute protocol's authorisation check.
type AuthorAssociation string

const (
	AssocNone                 AuthorAssociation = "NONE"
	AssocFirstTimeContributor AuthorAssociation = "FIRST_TIME_CONTRIBUTOR"
	AssocFirstTimer           AuthorAssociation = "FIRST_TIMER"
	AssocCollaborator         AuthorAssociation = "COLLABORATOR"
	AssocContributor          AuthorAssociation = "CONTRIBUTOR"
	AssocMember               AuthorAssociation = "MEMBER"
	AssocOwner                AuthorAssociation = "OWNER"
)

// LedgerEntryKind distinguishes what an entry attests.
type LedgerEntryKind string

const (
	// EntryDismissal records a human adjudication: do not re-raise until expiry.
	EntryDismissal LedgerEntryKind = "dismissal"
	// EntryPublished records only that Cite itself raised this fingerprint in
	// this repository. Dismissals are honoured only for fingerprints Cite
	// published (§12), so nobody can pre-dismiss a finding that has not been
	// raised. Published entries never suppress anything.
	EntryPublished LedgerEntryKind = "published"
)

// DismissalEntry is one ledger record. AdjudicatorAuthor and
// AuthorAssociation come only from authenticated GitHub API metadata (I2) —
// never from comment text.
type DismissalEntry struct {
	Kind              LedgerEntryKind `json:"kind"`
	Fingerprint       string          `json:"fingerprint"`
	Repository        string          `json:"repository"`
	AdjudicatorAuthor string          `json:"adjudicator_author,omitempty"`
	AuthorAssociation string          `json:"author_association,omitempty"`
	DismissedAt       time.Time       `json:"dismissed_at"`
}

// DismissalLedger is the append-only set of entries. Zero-value entries whose
// Kind is empty are read as EntryDismissal for compatibility with blobs
// written before kinds existed.
type DismissalLedger struct {
	Entries []DismissalEntry `json:"entries"`
}

func entryKind(e DismissalEntry) LedgerEntryKind {
	if e.Kind == "" {
		return EntryDismissal
	}
	return e.Kind
}

// Add records a human dismissal of (fingerprint, repo) by author at now.
func (l *DismissalLedger) Add(fingerprint, repo, author, association string, now time.Time) {
	l.Entries = append(l.Entries, DismissalEntry{
		Kind:              EntryDismissal,
		Fingerprint:       fingerprint,
		Repository:        repo,
		AdjudicatorAuthor: author,
		AuthorAssociation: association,
		DismissedAt:       now,
	})
}

func (l *DismissalLedger) addPublished(fingerprint, repo string, now time.Time) {
	l.Entries = append(l.Entries, DismissalEntry{
		Kind:        EntryPublished,
		Fingerprint: fingerprint,
		Repository:  repo,
		DismissedAt: now,
	})
}

// Active reports whether an unexpired dismissal exists for
// (fingerprint, repo) at time now.
func (l *DismissalLedger) Active(fingerprint, repo string, now time.Time) bool {
	for _, e := range l.Entries {
		if entryKind(e) != EntryDismissal {
			continue
		}
		if e.Fingerprint == fingerprint && e.Repository == repo && !expired(e, now) {
			return true
		}
	}
	return false
}

// Published reports whether Cite itself has ever raised this fingerprint in
// this repository (per the rebuilt-from-threads registry or explicit marks).
// A dismissal is only honoured for published fingerprints.
func (l *DismissalLedger) Published(fingerprint, repo string) bool {
	for _, e := range l.Entries {
		if entryKind(e) == EntryPublished && e.Fingerprint == fingerprint && e.Repository == repo {
			return true
		}
	}
	return false
}

func expired(e DismissalEntry, now time.Time) bool {
	return !now.Before(e.DismissedAt.Add(LedgerExpiry))
}

// Prune drops expired entries so the sticky-comment blob does not grow forever.
func (l *DismissalLedger) Prune(now time.Time) {
	kept := l.Entries[:0]
	for _, e := range l.Entries {
		if entryKind(e) == EntryDismissal && expired(e, now) {
			continue
		}
		kept = append(kept, e)
	}
	l.Entries = kept
}

// MarshalBlob serialises the ledger to a base64 JSON blob suitable for
// embedding in an HTML comment on the sticky bot comment (§10). It is a cache:
// corrupt or missing means rebuild, never data loss.
func (l DismissalLedger) MarshalBlob() (string, error) {
	raw, err := json.Marshal(l)
	if err != nil {
		return "", fmt.Errorf("publisher: marshal ledger: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// UnmarshalBlob parses a ledger blob. On garbage input it returns an error;
// callers rebuild from threads rather than failing the run.
func UnmarshalBlob(blob string) (DismissalLedger, error) {
	var l DismissalLedger
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return l, fmt.Errorf("publisher: decode ledger blob: %w", err)
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return l, fmt.Errorf("publisher: unmarshal ledger blob: %w", err)
	}
	return l, nil
}

// CanDismiss validates adjudicator authorisation from GitHub-reported facts
// only (§12): never the pull request author, never FIRST_TIME_CONTRIBUTOR or
// FIRST_TIMER. NONE / COLLABORATOR / MEMBER / OWNER / CONTRIBUTOR are
// accepted. A comment body claiming authority proves nothing.
func CanDismiss(author, association string, isPRAuthor bool) error {
	if isPRAuthor {
		return fmt.Errorf("publisher: %q is the pull request author and can never self-dismiss", author)
	}
	switch AuthorAssociation(association) {
	case AssocNone, AssocCollaborator, AssocContributor, AssocMember, AssocOwner:
		return nil
	case AssocFirstTimeContributor, AssocFirstTimer:
		return fmt.Errorf("publisher: %q has author_association %s and cannot dismiss", author, association)
	default:
		return fmt.Errorf("publisher: unknown author_association %q for %q", association, author)
	}
}

// RebuildFromThreads reconstructs the published-fingerprints registry from
// the live review threads when the sticky-comment blob is missing or corrupt.
//
// Honest limitation, documented because silent pretending would be worse: the
// threads do not record who dismissed what, so the rebuilt ledger contains
// only EntryPublished marks — every fingerprint currently live in repo is
// registered as "Cite raised this here". Those entries never suppress
// anything (Active stays false); they exist so future dismissals are honoured
// only for fingerprints Cite itself published, and so pre-dismissing an
// unpublished finding is impossible. Genuine dismissal entries reappear the
// next time a human dismisses something after the rebuild.
func RebuildFromThreads(repo string, threads []LiveThread, now time.Time) DismissalLedger {
	var l DismissalLedger
	seen := map[string]bool{}
	for _, t := range threads {
		if t.Fingerprint == "" || seen[t.Fingerprint] {
			continue
		}
		seen[t.Fingerprint] = true
		l.addPublished(t.Fingerprint, repo, now)
	}
	return l
}
