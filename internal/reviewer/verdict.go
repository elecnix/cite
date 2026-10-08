package reviewer

// VerifierVerdict is the discriminative verifier's answer for one blocking
// candidate (§8, "The verifier pass"). The vocabulary is closed: three
// answers and no catch-all, because a fourth unlabelled answer would be
// indistinguishable from a silent failure at every call site.
//
// The three strings are recorded verbatim in the run artifact's
// verifier_result field and printed by the local CLI, so they are wire
// values. Renaming one changes what a consumer of the artifact reads.
type VerifierVerdict string

const (
	// VerifierSupported means the quoted evidence, as written, establishes
	// the claim. It is the only verdict that leaves a finding blocking.
	VerifierSupported VerifierVerdict = "supported"

	// VerifierUnsupported means the quoted evidence does not establish the
	// claim. The finding is dropped under DropVerifierUnsupported rather
	// than demoted, because a claim its own evidence contradicts is the
	// fabrication case, not a judgement call.
	VerifierUnsupported VerifierVerdict = "unsupported"

	// VerifierNeedsContextNotProvided means the evidence cannot settle the
	// claim from the one file the reviewer saw. It is frequently the right
	// answer and it demotes the finding to a note; it is never a drop,
	// because "we could not check" is not "this is wrong".
	VerifierNeedsContextNotProvided VerifierVerdict = "needs-context-not-provided"

	// VerifierError records that the call itself failed. It is not one of
	// the three answers a verifier returns: it is what the record carries
	// when the transport gave nothing back. Like every non-supporting
	// verdict it fails open to a note, never to a block.
	VerifierError VerifierVerdict = "error"
)
