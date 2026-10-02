package reviewer

import "testing"

// TestVerifierVerdictVocabularyIsWire pins the three answers and the recorded
// failure to the exact strings the run artifact carries in verifier_result.
// They are read by consumers of the artifact, so a rename here is a change to
// published output, not a refactor.
func TestVerifierVerdictVocabularyIsWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  VerifierVerdict
		want string
	}{
		{"supported", VerifierSupported, "supported"},
		{"unsupported", VerifierUnsupported, "unsupported"},
		{"needs context", VerifierNeedsContextNotProvided, "needs-context-not-provided"},
		{"error", VerifierError, "error"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, string(tc.got), tc.want)
		}
	}
}

// TestVerifierVerdictIsDistinct guards the no-catch-all property the type
// exists for: a fourth constant that collapsed onto one of the three would
// make two different answers indistinguishable in the record.
func TestVerifierVerdictIsDistinct(t *testing.T) {
	seen := map[VerifierVerdict]string{}
	for _, v := range []VerifierVerdict{
		VerifierSupported,
		VerifierUnsupported,
		VerifierNeedsContextNotProvided,
		VerifierError,
	} {
		if prev, dup := seen[v]; dup {
			t.Errorf("verdict %q duplicates %s", v, prev)
		}
		seen[v] = string(v)
	}
	if len(seen) != 4 {
		t.Errorf("got %d distinct verdicts, want 4", len(seen))
	}
}
