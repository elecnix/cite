package model

// The blocking formula of PLAN.md §8 lives here, once, with every input
// named. It is the only policy in Cite that decides whether a finding
// concludes a merge, and it was previously spelled inline at more than one
// site. Two spellings of the same policy are one spelling too many: a
// conjunct tightened in one copy and forgotten in the other changes which
// findings block a real merge without any code change saying so.
//
// The formula, as PLAN.md §8 writes it:
//
//	blocks = category ∈ gate.blocking_categories
//	       ∧ every evidence quote matches the file
//	       ∧ the anchor is on an added line
//	       ∧ external_claims is empty, or every claim verified true
//	       ∧ confidence == "certain"
//	       ∧ the discriminative verifier returned "supported"
//
// The functions below split that conjunction into the part decidable from a
// finding and its evidence (BlocksOnEvidence) and the part that also needs
// the per-run checks (ComputeBlocks). The discriminative verifier is the
// sixth conjunct and is applied by the caller rather than here: an
// "unsupported" verdict drops the finding from the record entirely, which is
// not a boolean the caller can fold in here.

// BlockInputs carries one field per conjunct of the §8 formula. Every field
// is a fact the caller established, never an assumption: a caller that
// could not check a conjunct passes false and the formula fails closed,
// because an unverified premise must never ground a block.
type BlockInputs struct {
	// Category is the finding's category. Category.MayBlock is consulted
	// first, so convention can never block in any configuration.
	Category Category

	// BlockingSet is the repository's gate.blocking_categories, resolved
	// to a set. A nil or empty set blocks nothing: the caller that has the
	// repository configuration resolves the defaults before calling.
	BlockingSet map[Category]bool

	// EvidenceMatches is true when every evidence quote matched the
	// reviewed content at some cascade level.
	EvidenceMatches bool

	// AnchorOnAddedLine is true when the anchor range contains at least
	// one added line of this change.
	AnchorOnAddedLine bool

	// ClaimsVerified is true when external_claims is empty or every claim
	// was verified true.
	ClaimsVerified bool

	// NegativeClaimsVerified is true when every negative-existence claim
	// on the finding was checked against the reviewed content and held.
	// A claim that could not be checked leaves this false: it can never
	// ground a block.
	NegativeClaimsVerified bool

	// Confidence is the finding's own confidence. Only certain blocks.
	Confidence Confidence

	// VerifierSupported is the discriminative verifier's answer. It is
	// read by ComputeBlocks, so a caller that runs the verifier after
	// computing the rest must fold the answer in itself.
	VerifierSupported bool
}

// BlocksOnEvidence is the part of the §8 formula a caller can decide from a
// finding and its validated evidence alone: category eligibility, the
// repository's blocking set, evidence that matches, an anchor on an added
// line, and certain confidence. The claim and verifier conjuncts are absent
// because they need per-run checks, so a caller asking "would this block if
// every remaining check passed" asks this.
func BlocksOnEvidence(in BlockInputs) bool {
	return in.Category.MayBlock() &&
		in.BlockingSet[in.Category] &&
		in.EvidenceMatches &&
		in.AnchorOnAddedLine &&
		in.Confidence == ConfidenceCertain
}

// BlockingCandidate is the §8 formula minus the discriminative verifier:
// BlocksOnEvidence plus verified external claims and verified
// negative-existence claims. It is what a caller decides before the verifier
// has run, so it can tell whether a finding is worth verifying and whether a
// verifier that errors out or answers "needs-context-not-provided" is
// demoting a finding that would otherwise have blocked.
func BlockingCandidate(in BlockInputs) bool {
	return BlocksOnEvidence(in) &&
		in.ClaimsVerified &&
		in.NegativeClaimsVerified
}

// ComputeBlocks is the §8 formula over the inputs a caller has established:
// BlockingCandidate plus a discriminative verifier that returned supported.
// Every caller passes its own facts; none is inferred here.
func ComputeBlocks(in BlockInputs) bool {
	return BlockingCandidate(in) && in.VerifierSupported
}
