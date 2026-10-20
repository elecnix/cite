package reviewer

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// Issue #203: the shape of an absence claim is a declared field, not a
// phrase in English.
//
// The guard used to recover the shape of the absence by substring-matching
// a table of English phrases against the finding's narrative, and to guess
// the subject from the token before the phrase. That made coverage a
// function of the narrative vocabulary, and the direction that failed was
// the dangerous one: a missing phrase meant the ordinary blocking formula
// applied to an absence claim nobody had checked. It only ever worked in
// English.
//
// These tests hold the new mechanism in place:
//
//   - the absence shapes are claim types with a structured subject;
//   - the check runs on the declared type and the declared symbol;
//   - a declared claim the file contradicts drops the finding;
//   - an UNDECLARED absence never blocks, however it is phrased;
//   - the phrase tables are a downgrade-only fallback.

// absencePostImage is the base post-image with extra lines appended, so a
// test can put a definition, a call or an occurrence in the file without
// disturbing the added line the finding anchors on.
func absencePostImage(extra ...string) []byte {
	b := strings.Builder{}
	b.WriteString("context line one\nadded line alpha here\ncontext line two\n")
	for _, l := range extra {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// absenceClaim builds the external_claims entry a model declares for an
// absence shape (issue #203): the type says which shape, the symbol says
// which name.
func absenceClaim(typ model.ClaimType, symbol string) []any {
	return []any{map[string]any{
		"type":    string(typ),
		"subject": symbol,
		"symbol":  symbol,
	}}
}

// runAbsenceClaim reviews one certain logic-inversion finding on the added
// line, over a post-image with the given extra lines, declaring the given
// claims.
func runAbsenceClaim(t *testing.T, extra []string, claims []any, mutate func(map[string]any)) *model.RunRecord {
	t.Helper()
	in := baseInputs()
	in.PostImage["a.go"] = absencePostImage(extra...)
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryLogicInversion, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		f["external_claims"] = claims
		if mutate != nil {
			mutate(f)
		}
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec
}

// A declared symbol_not_defined claim the file contradicts is dropped: the
// file defines the symbol, so the finding is a false claim, and a note that
// names a defined helper is still a false claim published.
func TestDeclaredSymbolNotDefinedFalsifiedByTheDefinitionDrops(t *testing.T) {
	rec := runAbsenceClaim(t, []string{"func notok() { echo fail; }"},
		absenceClaim(model.ClaimSymbolNotDefined, "notok"), nil)
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (notok is defined at line 4); findings %+v", len(rec.Findings), rec.Findings)
	}
	d := findDrop(rec, model.DropNegativeClaimFalsified)
	if d == nil {
		t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
	}
	if !strings.Contains(d.Detail, "is defined at line 4") {
		t.Errorf("drop detail = %q, want it to name the definition at line 4", d.Detail)
	}
}

// The same claim in a file that defines the symbol nowhere holds, so the
// finding survives and blocks. This is the finding a reviewer gets right.
func TestDeclaredSymbolNotDefinedHoldsWhenNothingDefinesIt(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimSymbolNotDefined, "notok"), nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true: a declared absence claim that holds blocks")
	}
	if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "verified" {
		t.Errorf("Disposition = %q, want verified", got)
	}
}

// A declared symbol_not_called claim is falsified by a CALL, and by
// nothing else: the definition of the symbol does not answer it.
func TestDeclaredSymbolNotCalledFalsifiedByACallDrops(t *testing.T) {
	rec := runAbsenceClaim(t, []string{"result = helper("},
		absenceClaim(model.ClaimSymbolNotCalled, "helper"), nil)
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (helper is called at line 4); findings %+v", len(rec.Findings), rec.Findings)
	}
	if findDrop(rec, model.DropNegativeClaimFalsified) == nil {
		t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
	}
}

// A declared symbol_not_called claim is NOT falsified by a definition: a
// "defined but never called" finding is true, and a call-shaped rule would
// have dropped it on its own definition.
func TestDeclaredSymbolNotCalledNotFalsifiedByADefinition(t *testing.T) {
	rec := runAbsenceClaim(t, []string{"func helper() { return 1 }"},
		absenceClaim(model.ClaimSymbolNotCalled, "helper"), nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true: a definition does not answer a call claim")
	}
}

// A declared occurrence_count_zero claim is falsified by ANY occurrence of
// the symbol.
func TestDeclaredOccurrenceCountZeroFalsifiedByAnyOccurrence(t *testing.T) {
	rec := runAbsenceClaim(t, []string{"// helper does the work"},
		absenceClaim(model.ClaimOccurrenceCountZero, "helper"), nil)
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (helper occurs at line 4); findings %+v", len(rec.Findings), rec.Findings)
	}
	if findDrop(rec, model.DropNegativeClaimFalsified) == nil {
		t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
	}
}

// An occurrence_count_zero claim in a file where the symbol does not occur
// holds, and the finding blocks.
func TestDeclaredOccurrenceCountZeroHoldsWhenTheSymbolIsAbsent(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimOccurrenceCountZero, "helper"), nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true: a declared zero-count claim that holds blocks")
	}
}

// The heart of issue #203: an absence asserted in PROSE, with no declared
// claim, can never block. The phrase table still recognises the shape, so
// the finding is not dropped — it is demoted to a note.
//
// Before the change this finding blocked: the phrase table granted
// "verified" and the ordinary blocking formula applied.
func TestProseAbsenceWithoutADeclaredClaimNeverBlocks(t *testing.T) {
	rec := runAbsenceClaim(t, nil, nil, func(f map[string]any) {
		f["title"] = "maybeSuffixForkIdentity is never called"
		f["body"] = "The helper maybeSuffixForkIdentity is never invoked anywhere in this module."
	})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (the finding survives as a note); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: an undeclared absence was never mechanically checked")
	}
}

// The same finding in a language the phrase table does not know behaves
// the same way. Before the change it was worse than the English case: no
// phrase matched, so the guard was absent entirely and the ordinary
// blocking formula applied to an unchecked absence claim.
//
// The structural marker is what closes that gap: evidence_kind "absent" is
// a field, and a field does not depend on the language the model wrote in.
func TestAbsenceInAnotherLanguageNeverBlocks(t *testing.T) {
	rec := runAbsenceClaim(t, nil, nil, func(f map[string]any) {
		f["title"] = "maybeSuffixForkIdentity n'est jamais appelée"
		f["body"] = "La fonction maybeSuffixForkIdentity n'est jamais invoquée dans ce module."
		f["evidence_kind"] = "absent"
		f["missing_assertion"] = "maybeSuffixForkIdentity n'est jamais appelée dans ce fichier"
	})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: an absence no checker could read was never verified")
	}
}

// A prose absence that is TRUE survives as a note: it is not dropped,
// because nothing in the file contradicts it, and it does not block,
// because nobody checked it. The finding is published either way, and the
// difference between the two verdicts is the point of the change.
func TestTrueProseAbsenceSurvivesAsANonBlockingNote(t *testing.T) {
	rec := runAbsenceClaim(t, nil, nil, func(f map[string]any) {
		f["title"] = "maybeSuffixForkIdentity is never called"
		f["body"] = "The helper maybeSuffixForkIdentity is never invoked anywhere in this module."
	})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (a true absence is not dropped); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: a prose-only absence was never mechanically checked")
	}
	if findDrop(rec, model.DropNegativeClaimFalsified) != nil {
		t.Errorf("a true prose absence must not be dropped; drops %+v", rec.Drops)
	}
}

// The same finding WITH the declared claim of the same shape is checked
// against the file, and a true one blocks. This is the pair the prose
// fallback and the declared claim have to agree on: the fallback demotes,
// the declared claim decides.
func TestDeclaredClaimOfTheSameShapeStillBlocks(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimSymbolNotCalled, "maybeSuffixForkIdentity"),
		func(f map[string]any) {
			f["title"] = "maybeSuffixForkIdentity is never called"
			f["body"] = "The helper maybeSuffixForkIdentity is never invoked anywhere in this module."
		})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true: the declared claim was checked and it holds")
	}
}

// The fallback keeps the falsifying power it always had: a prose absence
// the file contradicts is still dropped. The demotion is a demotion, not a
// pardon.
func TestProseAbsenceContradictedByTheFileStillDrops(t *testing.T) {
	rec := runAbsenceClaim(t, []string{"result = helperFn("}, nil, func(f map[string]any) {
		f["title"] = "helperFn is never called"
		f["body"] = "The helper helperFn is never invoked anywhere in this module."
	})
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0; findings %+v", len(rec.Findings), rec.Findings)
	}
	if findDrop(rec, model.DropNegativeClaimFalsified) == nil {
		t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
	}
}

// A finding that declares itself an absence claim (evidence_kind "absent")
// but declares no absence CLAIM cannot block: the mechanism of the finding
// is an absence, and an absence nobody can check is not a blocker.
func TestAbsenceFindingWithoutADeclaredClaimNeverBlocks(t *testing.T) {
	rec := runAbsenceClaim(t, nil, nil, func(f map[string]any) {
		f["evidence_kind"] = "absent"
		f["missing_assertion"] = "there is no validation of the signature anywhere in this file"
	})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (the finding survives as a note); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: an absence finding with no declared claim cannot block")
	}
}

// A finding that writes what is missing into missing_assertion has
// asserted an absence, whether or not it also set evidence_kind. The
// requirement does not depend on a second field being remembered, and it
// does not depend on the language the assertion is written in.
func TestMissingAssertionAloneNeverBlocks(t *testing.T) {
	rec := runAbsenceClaim(t, nil, nil, func(f map[string]any) {
		f["missing_assertion"] = "la validation de la signature n'existe nulle part dans ce fichier"
	})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (the finding survives as a note); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: a missing_assertion asserts an absence nobody declared")
	}
}

// The same absence finding WITH a declared claim is checkable, so it can
// still block.
func TestAbsenceFindingWithADeclaredClaimCanBlock(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimSymbolNotCalled, "validateSignature"),
		func(f map[string]any) {
			f["evidence_kind"] = "absent"
			f["missing_assertion"] = "validateSignature is not called anywhere in this file"
		})
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true: a declared absence claim that holds blocks")
	}
}

// A declared absence claim whose symbol is not a name cannot be checked:
// the search has nothing to look for, so zero hits prove nothing and the
// claim earns the note disposition. The finding is NOT dropped: an
// uncheckable claim is not a falsified one.
func TestDeclaredAbsenceClaimWithANonNameSubjectIsANote(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimSymbolNotDefined, "the validation helper"), nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (not dropped); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: a claim nobody could check cannot block")
	}
	if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "note" {
		t.Errorf("Disposition = %q, want note", got)
	}
}

// A declared absence claim about ANOTHER file cannot be checked against the
// file under review, so it is a note and never a block.
func TestDeclaredAbsenceClaimAboutAnotherPathIsANote(t *testing.T) {
	claims := []any{map[string]any{
		"type":    string(model.ClaimSymbolNotDefined),
		"subject": "notok",
		"symbol":  "notok",
		"path":    "other/file.sh",
	}}
	rec := runAbsenceClaim(t, nil, claims, nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: a claim about another file cannot block")
	}
}

// A declared absence claim on a partially-sent file cannot be confirmed
// either: the file was truncated, so the absence was never observed. It
// fails open to a note.
func TestDeclaredAbsenceClaimOnAPartialFileNeverBlocks(t *testing.T) {
	in := baseInputs()
	b := strings.Builder{}
	b.WriteString("context line one\nadded line alpha here\n")
	for i := 0; i < maxFileLines+10; i++ {
		b.WriteString("padding filler line number 00005\n")
	}
	in.PostImage["a.go"] = []byte(b.String())
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryLogicInversion, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		f["external_claims"] = absenceClaim(model.ClaimSymbolNotCalled, "helper")
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (fail open to note); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false on a partial view")
	}
}

// A declared absence claim with no symbol does not cost the file its
// review. The parse keeps it, and the validator gives it the note
// disposition: the search has nothing to look for, so the claim can
// neither confirm nor refute the absence, and it must never block.
func TestAbsenceClaimWithNoSymbolIsANoteAndTheFileSurvives(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimSymbolNotDefined, ""), nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: a claim nobody could check cannot block")
	}
	if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "note" {
		t.Errorf("Disposition = %q, want note", got)
	}
}

// An occurrence_count_zero claim that declares a non-zero count contradicts
// itself. It is a note, not a block, and it does not cost the file its
// review either.
func TestSelfContradictingCountClaimIsANote(t *testing.T) {
	claims := []any{map[string]any{
		"type":    string(model.ClaimOccurrenceCountZero),
		"subject": "helper",
		"symbol":  "helper",
		"count":   3,
	}}
	rec := runAbsenceClaim(t, nil, claims, nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: a claim that contradicts itself cannot block")
	}
	if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "note" {
		t.Errorf("Disposition = %q, want note", got)
	}
}

// The drop log says which failure happened. A phrase-matched absence with
// no extractable symbol is a different failure from a finding that
// asserts an absence in a field and declares no claim, and the more
// specific reason survives.
func TestTheMoreSpecificAbsenceReasonSurvives(t *testing.T) {
	// A phrase match with no extractable symbol: the fallback's own
	// reason applies, and it names the phrase.
	f := &model.Finding{
		Title: "the branch is dead",
		Body:  "No call site exists for it, so the branch is dead.",
	}
	res := checkAbsenceClaims(f, "a.go", []string{"context line one"}, false)
	if res.Verified {
		t.Errorf("Verified = true, want false: the claim could not be checked")
	}
	if !strings.Contains(res.Detail, "no extractable symbol") {
		t.Errorf("Detail = %q, want the fallback's own reason about the missing symbol", res.Detail)
	}

	// The same finding with the structural marker set: the fallback's
	// reason still wins, because it is the more specific one.
	f2 := &model.Finding{
		Title:            "the branch is dead",
		Body:             "No call site exists for it, so the branch is dead.",
		EvidenceKind:     model.EvidenceAbsent,
		MissingAssertion: "no call site exists for it",
	}
	res2 := checkAbsenceClaims(f2, "a.go", []string{"context line one"}, false)
	if res2.Verified {
		t.Errorf("Verified = true, want false")
	}
	if !strings.Contains(res2.Detail, "no extractable symbol") {
		t.Errorf("Detail = %q, want the fallback's own reason to survive", res2.Detail)
	}

	// A finding that asserts an absence in a field and has no phrase at
	// all gets the structural reason.
	f3 := &model.Finding{
		Title:            "Something is missing here",
		Body:             "The check is missing from this function.",
		EvidenceKind:     model.EvidenceAbsent,
		MissingAssertion: "the signature check is missing",
	}
	res3 := checkAbsenceClaims(f3, "a.go", []string{"context line one"}, false)
	if res3.Verified {
		t.Errorf("Verified = true, want false")
	}
	if !strings.Contains(res3.Detail, "declares no absence claim") {
		t.Errorf("Detail = %q, want the structural reason", res3.Detail)
	}
}

// The record says what happened to the claim: a claim that was checked and
// held is "verified", a claim nobody could check is a "note". An
// unverified claim also fails the "every claim verified true" conjunct, so
// the finding can never ground a block on the strength of a claim nobody
// ran.
func TestAbsenceClaimDispositionIsRecorded(t *testing.T) {
	rec := runAbsenceClaim(t, nil, absenceClaim(model.ClaimSymbolNotCalled, "helper"), nil)
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	c := rec.Findings[0].ExternalClaims[0]
	if c.Disposition != "verified" {
		t.Errorf("Disposition = %q, want verified", c.Disposition)
	}
	if c.Verified == nil || !*c.Verified {
		t.Errorf("Verified = %v, want true: the claim was checked and it held", c.Verified)
	}
}
