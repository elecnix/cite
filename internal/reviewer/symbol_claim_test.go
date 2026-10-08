package reviewer

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// runSymbolClaim reviews one certain crash finding on an added line that
// declares a single symbol_exists claim with the given subject, against a
// verifier that knows no symbols at all.
func runSymbolClaim(t *testing.T, subject string) *model.RunRecord {
	t.Helper()
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		f["external_claims"] = []any{map[string]string{"type": "symbol_exists", "subject": subject}}
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	o := baseOptions(c)
	o.Verifier = &fakeVerifier{symbols: map[string]bool{}}
	rec, err := runOnce(t, in, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec
}

// A symbol_exists claim whose subject is a sentence about toolchain
// behaviour names no symbol, so a definition search can neither confirm
// nor refute it. The finding must survive as a note that never blocks,
// the same disposition the claim gets when it is filed as a convention.
func TestSymbolExistsProseSubjectKeptAsNote(t *testing.T) {
	subjects := []string{
		"`go env GOBIN GOPATH` prints an empty line for an unset GOBIN, so the output has at most one non-empty line",
		"go env GOBIN GOPATH prints an empty line for an unset GOBIN",
	}
	for _, subject := range subjects {
		t.Run(subject, func(t *testing.T) {
			rec := runSymbolClaim(t, subject)
			if d := findDrop(rec, model.DropClaimUnverified); d != nil {
				t.Fatalf("finding dropped as %s: %s", d.Reason, d.Detail)
			}
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
			}
			f := rec.Findings[0]
			if f.Blocks {
				t.Errorf("Blocks = true, want false: an unchecked claim never grounds a block")
			}
			if len(f.ExternalClaims) != 1 || f.ExternalClaims[0].Disposition != "note" {
				t.Errorf("ExternalClaims = %+v, want one claim with disposition note", f.ExternalClaims)
			}
			if f.ExternalClaims[0].Verified != nil {
				t.Errorf("Verified = %v, want unset for a claim nobody checked", *f.ExternalClaims[0].Verified)
			}
		})
	}
}

// An identifier the repository does not define is still a fabrication,
// however the model decorates it: backticks, a call suffix or a qualified
// name all reach the definition search and drop the finding.
func TestSymbolExistsMissingIdentifierStillDrops(t *testing.T) {
	subjects := []string{
		"validateSignature",
		"`validateSignature`",
		"validateSignature()",
		"auth.ValidateSignature",
		"Verifier::check_signature",
	}
	for _, subject := range subjects {
		t.Run(subject, func(t *testing.T) {
			rec := runSymbolClaim(t, subject)
			if len(rec.Findings) != 0 {
				t.Fatalf("Findings = %d, want 0", len(rec.Findings))
			}
			if findDrop(rec, model.DropClaimUnverified) == nil {
				t.Fatalf("no DropClaimUnverified entry; drops %+v", rec.Drops)
			}
		})
	}
}

// A defined symbol wrapped in backticks or written as a call reaches the
// verifier as the bare name, so the decoration cannot turn a true claim
// into a drop.
func TestSymbolExistsDecoratedSubjectVerifiesBareName(t *testing.T) {
	for _, subject := range []string{"`validateSignature`", "validateSignature()"} {
		t.Run(subject, func(t *testing.T) {
			in := baseInputs()
			c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
				if isTriageCall(req) {
					return triageJSON("a.go"), nil
				}
				f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
					[]map[string]any{mkEvidence(2, "added line alpha here")})
				f["external_claims"] = []any{map[string]string{"type": "symbol_exists", "subject": subject}}
				return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
			}}
			o := baseOptions(c)
			o.Verifier = &fakeVerifier{symbols: map[string]bool{"validateSignature": true}}
			rec, err := runOnce(t, in, o)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
			}
			if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "verified" {
				t.Errorf("Disposition = %q, want verified", got)
			}
		})
	}
}
