// Package model holds the data contract shared by every part of Cite:
// scope, reviewer, publisher and gate. The finding schema is closed (§8):
// no severity, no CWE, no URLs, no free-form tags.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SchemaVersion is the version of the finding schema the model is asked to
// emit. A mismatch is a parse-time failure, not a warning.
const SchemaVersion = 1

// Category is the closed, mechanism-named vocabulary. There is no catch-all:
// no "bug", no "other" (§8). The blocking column is a property of measured
// competence; repository configuration may shrink it and may never grow it.
type Category string

const (
	CategorySecretExposure    Category = "secret-exposure"
	CategoryInjection         Category = "injection"
	CategoryAuthBypass        Category = "auth-bypass"
	CategoryDestructiveOp     Category = "destructive-operation"
	CategoryCrash             Category = "crash"
	CategoryLogicInversion    Category = "logic-inversion"
	CategoryResourceLeak      Category = "resource-leak"
	CategoryConcurrency       Category = "concurrency"
	CategoryErrorSwallow      Category = "error-swallow"
	CategoryAPIContractBreak  Category = "api-contract-break"
	CategoryConvention        Category = "convention"
)

// MayBlock reports whether the category is eligible to block a merge at all.
// convention can never block, in any configuration.
func (c Category) MayBlock() bool {
	switch c {
	case CategorySecretExposure, CategoryInjection, CategoryAuthBypass, CategoryDestructiveOp,
		CategoryCrash, CategoryLogicInversion:
		return true
	default:
		return false
	}
}

// IsSecurityDerived reports whether "security" is derived from the category
// rather than being a tag the model chose.
func (c Category) IsSecurityDerived() bool {
	switch c {
	case CategorySecretExposure, CategoryInjection, CategoryAuthBypass, CategoryDestructiveOp:
		return true
	default:
		return false
	}
}

// Confidence is defined by what you know, never a float (§8).
type Confidence string

const (
	ConfidenceCertain Confidence = "certain"
	ConfidenceLikely  Confidence = "likely"
	ConfidenceQuestion Confidence = "question"
)

// ClaimType enumerates the external claims a finding may declare. Each type
// has a mechanical disposition (§8, "Verifying the claims the model cannot
// check"). version_behavior is banned outright and rejected at parse time.
type ClaimType string

const (
	ClaimPathExists   ClaimType = "path_exists"
	ClaimSymbolExists ClaimType = "symbol_exists"
	ClaimConfigKey    ClaimType = "config_key"
	ClaimVersionBehavior ClaimType = "version_behavior" // rejected at parse time
	ClaimCIBehavior   ClaimType = "ci_behavior"      // note only, never blocking
	ClaimConvention   ClaimType = "convention"       // note only, rendered as a question
)

// ExternalClaim is a repository-dependent fact the model cannot verify from
// the bytes it was given. Declaring it is how a claim gets checked instead of
// believed.
type ExternalClaim struct {
	Type   ClaimType `json:"type"`
	Subject string   `json:"subject"`
	// Verified is filled in by the verifier pass, never by the model.
	Verified *bool `json:"verified,omitempty"`
	// Disposition is the mechanical outcome: "verified", "dropped",
	// "note", or "rejected".
	Disposition string `json:"disposition,omitempty"`
}

// IntroducedBy forces the attribution decision to be explicit and checkable.
type IntroducedBy string

const (
	IntroducedAddedLine         IntroducedBy = "added_line"
	IntroducedExistingMadeWrong IntroducedBy = "existing_line_made_wrong"
)

// Anchor is a post-change line range, validated against the parsed diff
// before publishing.
type Anchor struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// Evidence is one {line, quote} pair. The quote must be the characters from
// that line, copied exactly, without the "NNNN +|" prefix.
type Evidence struct {
	Line  int    `json:"line"`
	Quote string `json:"quote"`
}

// EvidenceKind distinguishes presence claims from absence claims. Absence
// claims have no span to quote; they require a quoted anchor span (the
// enclosing signature or block header) plus a missing assertion.
type EvidenceKind string

const (
	EvidencePresent EvidenceKind = "present"
	EvidenceAbsent  EvidenceKind = "absent"
)

// EvidenceLevel is the recorded cascade level (§8). token publishes as notes
// only; fail is dropped and logged with its reason.
type EvidenceLevel string

const (
	EvidenceExact      EvidenceLevel = "exact"
	EvidenceNormalized EvidenceLevel = "normalized"
	EvidenceElided     EvidenceLevel = "elided"
	EvidenceToken      EvidenceLevel = "token"
	EvidenceFailed     EvidenceLevel = "fail"
)

// FixShape whitelists the four allowed one-click-commit shapes (§9).
type FixShape string

const (
	FixDeleteLines   FixShape = "delete_lines"
	FixSubstituteToken FixShape = "substitute_token"
	FixShellQuoting  FixShape = "shell_quoting"
	FixAddGuard      FixShape = "add_guard"
)

// Fix is nullable and defaults to null. A fix becomes a one-click commit
// button, so the bar is "applying this without reading it cannot make things
// worse". Enforced mechanically: the replacement may only introduce tokens
// already in the file or language keywords.
type Fix struct {
	Shape      FixShape `json:"shape"`
	StartLine  int      `json:"start_line"`
	EndLine    int      `json:"end_line"`
	Original   string   `json:"original,omitempty"`
	Replacement string  `json:"replacement,omitempty"`
}

// Finding is the model-emitted fact set, in a closed schema, with structured
// output enforced by the provider rather than JSON-in-prose plus a validator.
// Deliberately absent: severity, cwe/owasp, references/URLs, free-form tags,
// file_summary.
type Finding struct {
	ID             string          `json:"id"`
	Category       Category        `json:"category"`
	Anchor         Anchor          `json:"anchor"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	Impact         string          `json:"impact"`
	Evidence       []Evidence      `json:"evidence"`
	EvidenceKind   EvidenceKind    `json:"evidence_kind,omitempty"`
	MissingAssertion string        `json:"missing_assertion,omitempty"`
	ExternalClaims []ExternalClaim `json:"external_claims"`
	IntroducedBy   IntroducedBy    `json:"introduced_by"`
	Confidence     Confidence      `json:"confidence"`
	Fix            *Fix            `json:"fix"`
}

// Outcome distinguishes "nothing to say" from "could not say anything";
// neither is inferred from an empty findings array.
type Outcome string

const (
	OutcomeReviewed          Outcome = "reviewed"
	OutcomeReviewedPartial   Outcome = "reviewed_partial_context"
	OutcomeNotReviewable     Outcome = "not_reviewable"
)

// FileReview is the parsed, schema-validated output for one review unit.
type FileReview struct {
	SchemaVersion      int      `json:"schema_version"`
	Path               string   `json:"path"`
	Outcome            Outcome  `json:"outcome"`
	NotReviewableReason string  `json:"not_reviewable_reason,omitempty"`
	Findings           []Finding `json:"findings"`
}

// ValidatedFinding is a finding that survived the evidence cascade, the
// anchor check and the external-claims gate. It carries the code-computed
// metadata the model is never trusted to produce.
type ValidatedFinding struct {
	Finding
	// Path is the locator. It is not part of the fingerprint.
	Path string `json:"path"`
	// EvidenceLevel is the recorded cascade level.
	EvidenceLevel EvidenceLevel `json:"evidence_level"`
	// Blocks is computed in code from verifiable fields (§8).
	Blocks bool `json:"blocks"`
	// Fingerprint is the content-addressed identity (§10).
	Fingerprint string `json:"fingerprint"`
	// VerifierResult is the discriminative verifier's answer for blocking
	// candidates: "supported", "unsupported", "needs-context-not-provided",
	// or "" when the verifier did not run.
	VerifierResult string `json:"verifier_result,omitempty"`
	// Occurrence disambiguates two identical findings in one file.
	Occurrence int `json:"occurrence,omitempty"`
}

// Fingerprint is content-addressed, not line-addressed, so it survives a
// rebase: hash(category, normalized quoted span, normalized title). Path is a
// locator, not part of the fingerprint.
func (v *ValidatedFinding) FingerprintOf() string {
	var sb strings.Builder
	sb.WriteString(string(v.Category))
	sb.WriteString("\x1f")
	for _, e := range v.Evidence {
		sb.WriteString(NormalizeForFingerprint(e.Quote))
		sb.WriteString("\x1e")
	}
	sb.WriteString("\x1f")
	sb.WriteString(NormalizeForFingerprint(v.Title))
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:16])
}

// NormalizeForFingerprint lowercases, collapses whitespace and strips
// punctuation so a reformat does not churn identity.
func NormalizeForFingerprint(s string) string {
	s = strings.ToLower(s)
	var sb strings.Builder
	lastSpace := true // trim leading
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !lastSpace {
				sb.WriteByte(' ')
				lastSpace = true
			}
		case r == '"' || r == '\'' || r == '`' || r == '(' || r == ')' ||
			r == '[' || r == ']' || r == '{' || r == '}' || r == ',' ||
			r == ';' || r == ':' || r == '.' || r == '!' || r == '?' ||
			r == '#' || r == '%' || r == '&' || r == '*' || r == '+' ||
			r == '=' || r == '/' || r == '\\' || r == '|' || r == '<' ||
			r == '>' || r == '~' || r == '^' || r == '$':
			// strip punctuation
		default:
			sb.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(sb.String())
}

// DropReason is why a generated finding never reached a human. Without the
// drop log there is no way to measure the recall cost of the safety rails.
type DropReason string

const (
	DropEvidenceMismatch  DropReason = "evidence_mismatch"
	DropAnchorInvalid     DropReason = "anchor_invalid"
	DropAnchorNotAddedLine DropReason = "anchor_not_added_line"
	DropClaimUnverified   DropReason = "external_claim_unverified"
	DropClaimRejectedType DropReason = "external_claim_rejected_type"
	DropCategoryNeverBlocksAndBelowNotes DropReason = "category_never_blocks_below_notes"
	DropBudget            DropReason = "comment_budget"
	DropPerFileBudget     DropReason = "per_file_budget"
	DropSuppressed        DropReason = "suppressed"
	DropAssemblyCut       DropReason = "assembly_cut"
	DropAmbiguousQuote    DropReason = "ambiguous_quote"
	DropVerifierUnsupported DropReason = "verifier_unsupported"
	DropAbsenceOnPartial  DropReason = "absence_claim_on_partial_context"
	DropParseFailure      DropReason = "parse_failure"
)

// DropEntry is one killed finding with its reason, written to the run record.
// It answers both "why did you say that" and "why didn't you say that".
type DropEntry struct {
	Path     string     `json:"path"`
	Category Category   `json:"category,omitempty"`
	Title    string     `json:"title,omitempty"`
	Reason   DropReason `json:"reason"`
	Detail   string     `json:"detail,omitempty"`
}

// FileTerminalState: every file in the changed-file manifest reaches exactly
// one terminal state. There is no fourth state and no absence.
type FileTerminalState string

const (
	FileReviewed       FileTerminalState = "reviewed"
	FileSkipped        FileTerminalState = "skipped"
	FileErrored        FileTerminalState = "error"
)

// Well-known skip reasons. A skip is never a pass (§11).
const (
	SkipGenerated FileTerminalState = "generated"
)

// FileOutcome is one file's terminal state in the run record.
type FileOutcome struct {
	Path        string            `json:"path"`
	OldPath     string            `json:"old_path,omitempty"` // rename source
	Status      string            `json:"status"`             // A / M / D / R###
	State       FileTerminalState `json:"state"`
	Reason      string            `json:"reason,omitempty"` // named skip reason
	BlobSHA     string            `json:"blob_sha,omitempty"`
	Findings    int               `json:"findings"`
	Reviewed    bool              `json:"-"`
}

// Verdict is the three-state gate. Only one of them is a pass (§11).
type Verdict string

const (
	VerdictPass             Verdict = "PASS"
	VerdictFound            Verdict = "FOUND"
	VerdictCouldNotEvaluate Verdict = "COULD_NOT_EVALUATE"
)

// Conclusion maps a verdict onto a GitHub check-run conclusion.
func (v Verdict) Conclusion() string {
	switch v {
	case VerdictPass:
		return "success"
	default:
		// FOUND and COULD_NOT_EVALUATE are both failure: fail-closed.
		return "failure"
	}
}

// Coverage is computed in code, never attested by the model (§12, I7).
type Coverage struct {
	APIFiles    int `json:"api_files"`     // count of changed files from the GitHub API
	Reviewed    int `json:"reviewed"`
	ApprovedSkip int `json:"approved_skip"`
	Errored     int `json:"errored"`
	Complete    bool `json:"complete"` // count(reviewed ∪ approved-skip) == count(api files)
}

// RunRecord is the artifact the reviewer hands to the publisher and the gate.
// The review body links it; it answers "why did you say that" in one place.
type RunRecord struct {
	SchemaVersion int    `json:"schema_version"`
	Repository    string `json:"repository"`
	PRNumber      int    `json:"pr_number,omitempty"`
	HeadSHA       string `json:"head_sha"`
	BaseRef       string `json:"base_ref"`
	BaseSHA       string `json:"base_sha"`
	MergeBaseSHA  string `json:"merge_base_sha"`

	Model        string `json:"model"`
	Temperature  float64 `json:"temperature"`
	Seed         *int64  `json:"seed,omitempty"`
	InputHash    string `json:"input_hash"`

	Files    []FileOutcome      `json:"files"`
	Findings []ValidatedFinding `json:"findings"`
	Drops    []DropEntry        `json:"drops"`
	Coverage Coverage           `json:"coverage"`

	Verdict Verdict `json:"verdict"`
	// VerdictReason explains COULD_NOT_EVALUATE in one line.
	VerdictReason string `json:"verdict_reason,omitempty"`

	// InstructionsUsed reports the applicability-triage outcome in the
	// footer: "Using N of M sections from X. K were authoring."
	InstructionsUsed []InstructionUsage `json:"instructions_used,omitempty"`

	// Scoped says when risk-ranking capped the reviewed set, never silently.
	RiskRanked bool `json:"risk_ranked,omitempty"`
	RiskRankedNote string `json:"risk_ranked_note,omitempty"`

	// Samples is 1: a green is one sample (§8).
	Samples int `json:"samples"`
}

// InstructionUsage records which instruction sections survived triage.
type InstructionUsage struct {
	File       string `json:"file"`
	TotalSections int `json:"total_sections"`
	UsedSections  int `json:"used_sections"`
	AuthoringSections int `json:"authoring_sections"`
}

// Summary renders the one-line check-run summary that says a green is one
// sample: "1 sample · 7 files reviewed · 0 blocking findings."
func (r *RunRecord) Summary() string {
	return fmt.Sprintf("%d sample · %d files reviewed · %d blocking findings. This is one observation, not assurance.",
		r.Samples, r.Coverage.Reviewed, countBlocking(r.Findings))
}

func countBlocking(fs []ValidatedFinding) int {
	n := 0
	for _, f := range fs {
		if f.Blocks {
			n++
		}
	}
	return n
}

// BlockingFindings returns the findings that block, computed in code.
func (r *RunRecord) BlockingFindings() []ValidatedFinding {
	var out []ValidatedFinding
	for _, f := range r.Findings {
		if f.Blocks {
			out = append(out, f)
		}
	}
	return out
}
