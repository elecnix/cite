package model

// The §8 blocking formula is the policy that decides whether a finding
// concludes a merge, so it is pinned conjunct by conjunct here. A conjunct
// that is quietly dropped from ComputeBlocks, or a caller that starts
// passing true for a check it did not make, changes which findings block a
// real pull request. These tests fail when either happens.

import "testing"

// full is a set of inputs every conjunct of §8 holds.
func full() BlockInputs {
	return BlockInputs{
		Category:               CategoryInjection,
		BlockingSet:            map[Category]bool{CategoryInjection: true, CategoryCrash: true},
		EvidenceMatches:        true,
		AnchorOnAddedLine:      true,
		ClaimsVerified:         true,
		NegativeClaimsVerified: true,
		Confidence:             ConfidenceCertain,
		VerifierSupported:      true,
	}
}

func TestComputeBlocksEveryConjunctIsRequired(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*BlockInputs)
	}{
		{"category outside the blocking set", func(in *BlockInputs) { in.BlockingSet = map[Category]bool{CategoryCrash: true} }},
		{"no blocking set at all", func(in *BlockInputs) { in.BlockingSet = nil }},
		{"empty blocking set", func(in *BlockInputs) { in.BlockingSet = map[Category]bool{} }},
		{"evidence did not match", func(in *BlockInputs) { in.EvidenceMatches = false }},
		{"anchor is not on an added line", func(in *BlockInputs) { in.AnchorOnAddedLine = false }},
		{"external claims not verified", func(in *BlockInputs) { in.ClaimsVerified = false }},
		{"negative-existence claim not checked", func(in *BlockInputs) { in.NegativeClaimsVerified = false }},
		{"confidence below certain", func(in *BlockInputs) { in.Confidence = ConfidenceLikely }},
		{"confidence question", func(in *BlockInputs) { in.Confidence = ConfidenceQuestion }},
		{"verifier did not return supported", func(in *BlockInputs) { in.VerifierSupported = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := full()
			c.mutate(&in)
			if ComputeBlocks(in) {
				t.Fatalf("%s: ComputeBlocks returned true; the finding blocks with a conjunct unestablished", c.name)
			}
		})
	}
	if !ComputeBlocks(full()) {
		t.Fatal("ComputeBlocks returned false with every conjunct established")
	}
}

// convention can never block, in any configuration (§8): a repository that
// lists it cannot buy a merge blocker with it.
func TestComputeBlocksConventionNeverBlocks(t *testing.T) {
	in := full()
	in.Category = CategoryConvention
	in.BlockingSet = map[Category]bool{CategoryConvention: true}
	if ComputeBlocks(in) {
		t.Fatal("convention blocked with every other conjunct established; it must never block in any configuration")
	}
	if BlocksOnEvidence(in) {
		t.Fatal("BlocksOnEvidence accepted convention as a blocking candidate")
	}
}

// The verifier is applied after the fact by the caller, so the candidate
// computed before it runs must not already require the verifier's answer.
func TestBlockingCandidateIgnoresTheVerifierConjunct(t *testing.T) {
	in := full()
	in.VerifierSupported = false
	if !BlockingCandidate(in) {
		t.Fatal("BlockingCandidate returned false before the verifier ran; the verifier would never be asked about a blocking candidate")
	}
	if ComputeBlocks(in) {
		t.Fatal("ComputeBlocks returned true for a finding the discriminative verifier did not support")
	}
}

// BlocksOnEvidence is the guard callers use before the per-run checks exist:
// a claim or verifier conjunct left false must not change its answer.
func TestBlocksOnEvidenceIgnoresThePerRunConjuncts(t *testing.T) {
	in := full()
	in.ClaimsVerified = false
	in.NegativeClaimsVerified = false
	in.VerifierSupported = false
	if !BlocksOnEvidence(in) {
		t.Fatal("BlocksOnEvidence returned false because per-run checks were absent; they are not its inputs")
	}
	in.Confidence = ConfidenceLikely
	if BlocksOnEvidence(in) {
		t.Fatal("BlocksOnEvidence accepted a finding below certain confidence")
	}
}
