package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ParseFileReview validates a raw model response against the closed schema.
// Structured output is enforced by the provider where available; this is the
// second gate. A schema violation is a parse failure, not a best-effort read.
func ParseFileReview(data []byte) (*FileReview, error) {
	var fr FileReview
	if err := json.Unmarshal(data, &fr); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if fr.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("schema: version %d, want %d", fr.SchemaVersion, SchemaVersion)
	}
	switch fr.Outcome {
	case OutcomeReviewed, OutcomeReviewedPartial, OutcomeNotReviewable:
	default:
		return nil, fmt.Errorf("schema: unknown outcome %q", fr.Outcome)
	}
	if fr.Outcome == OutcomeNotReviewable && fr.NotReviewableReason == "" {
		return nil, fmt.Errorf("schema: not_reviewable requires a reason")
	}
	seen := map[string]bool{}
	for i := range fr.Findings {
		f := &fr.Findings[i]
		if f.ID == "" {
			return nil, fmt.Errorf("schema: finding %d missing id", i)
		}
		if seen[f.ID] {
			return nil, fmt.Errorf("schema: duplicate finding id %q", f.ID)
		}
		seen[f.ID] = true
		if !validCategory(f.Category) {
			return nil, fmt.Errorf("schema: finding %s unknown category %q", f.ID, f.Category)
		}
		if !validConfidence(f.Confidence) {
			return nil, fmt.Errorf("schema: finding %s unknown confidence %q", f.ID, f.Confidence)
		}
		if f.IntroducedBy != IntroducedAddedLine && f.IntroducedBy != IntroducedExistingMadeWrong {
			return nil, fmt.Errorf("schema: finding %s unknown introduced_by %q", f.ID, f.IntroducedBy)
		}
		if len(f.Evidence) == 0 {
			return nil, fmt.Errorf("schema: finding %s needs at least one evidence quote", f.ID)
		}
		if f.Anchor.StartLine <= 0 || f.Anchor.EndLine < f.Anchor.StartLine {
			return nil, fmt.Errorf("schema: finding %s anchor out of range", f.ID)
		}
		for j := range f.ExternalClaims {
			c := &f.ExternalClaims[j]
			if c.Type == ClaimVersionBehavior {
				// Banned rather than demoted (§8): never actionable,
				// always confident.
				return nil, fmt.Errorf("schema: finding %s declares banned claim type version_behavior", f.ID)
			}
			if !validClaimType(c.Type) {
				return nil, fmt.Errorf("schema: finding %s unknown external_claim type %q", f.ID, c.Type)
			}
		}
		if f.Fix != nil && !validFixShape(f.Fix.Shape) {
			return nil, fmt.Errorf("schema: finding %s unknown fix shape %q", f.ID, f.Fix.Shape)
		}
	}
	return &fr, nil
}

func validCategory(c Category) bool {
	switch c {
	case CategorySecretExposure, CategoryInjection, CategoryAuthBypass, CategoryDestructiveOp,
		CategoryCrash, CategoryLogicInversion, CategoryResourceLeak, CategoryConcurrency,
		CategoryErrorSwallow, CategoryAPIContractBreak, CategoryConvention:
		return true
	}
	return false
}

func validConfidence(c Confidence) bool {
	switch c {
	case ConfidenceCertain, ConfidenceLikely, ConfidenceQuestion:
		return true
	}
	return false
}

func validClaimType(t ClaimType) bool {
	switch t {
	case ClaimPathExists, ClaimSymbolExists, ClaimConfigKey, ClaimCIBehavior, ClaimConvention:
		return true
	}
	return false
}

func validFixShape(s FixShape) bool {
	switch s {
	case FixDeleteLines, FixSubstituteToken, FixShellQuoting, FixAddGuard:
		return true
	}
	return false
}

// SanitizeText strips bidi and zero-width control characters and neutralises
// GitHub-rendered hazards in model-authored text (§12, I5). No model-authored
// URL, image, @-mention, #123 reference or issue-closing keyword ever reaches
// GitHub's renderer. This is an allowlist-shaped strip applied to fields that
// carry model text; the schema itself has no field those things can travel in
// unstripped.
var forbiddenRunes = func() map[rune]bool {
	m := map[rune]bool{}
	// zero-width and invisible characters
	for _, r := range []rune{
		0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x202A, 0x202B, 0x202C,
		0x202D, 0x202E, 0x2060, 0x2061, 0x2062, 0x2063, 0xFEFF, 0x00AD,
	} {
		m[r] = true
	}
	return m
}()

var (
	reURL     = regexp.MustCompile(`(?i)\b(https?|ftp)://[^\s)\]<>"']+`)
	reMention = regexp.MustCompile(`(^|[^\w` + "`" + `])@([A-Za-z0-9][A-Za-z0-9-]*)`)
	reClosing = regexp.MustCompile(`(?i)\b(fixes|fix|closes|close|resolves|resolve)\s+#([0-9]+)`)
)

// SanitizeText strips bidi and zero-width control characters and removes the
// GitHub-rendered hazards model text must never carry (§12, I5): URLs
// (hallucinated essentially always), markdown image/link beacons,
// @-mentions (a notification storm) and issue-closing keywords.
func SanitizeText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if forbiddenRunes[r] || (r < 0x20 && r != '\n' && r != '\t') {
			continue
		}
		sb.WriteRune(r)
	}
	out := sb.String()
	out = reURL.ReplaceAllString(out, "[link removed]")
	// Break markdown link/image syntax: an unpaired "](" cannot render a
	// beacon or hide a URL.
	out = strings.ReplaceAll(out, "](", "] (")
	out = reMention.ReplaceAllString(out, "$1$2")
	out = reClosing.ReplaceAllString(out, "$1 $2")
	return out
}
