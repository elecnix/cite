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
//
// The cases are table-driven, because the mechanism is one mechanism and
// the cases are its inputs. A test per case would say the same thing
// several times over.

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

// The three declared absence types, and what each of them is falsified by.
//
// A claim the file contradicts drops the finding, because a claim the file
// contradicts is a false claim, and a note that names a helper the file
// defines is still a false claim published, only quieter. A claim the file
// bears out stands and can block, because that is the finding a reviewer
// gets right.
func TestDeclaredAbsenceClaimsAreCheckedOnTheirTypeAndSymbol(t *testing.T) {
	cases := []struct {
		name string
		// typ is the declared claim type.
		typ model.ClaimType
		// symbol is the declared subject.
		symbol string
		// contradicting is a line in the file that falsifies the claim.
		contradicting string
		// falsified says whether the contradicting line really falsifies
		// the claim of this type. A definition does not answer a call
		// claim, and a call does not answer a definition claim.
		falsified bool
		// dropDetail is a substring the drop reason must carry when the
		// claim is falsified.
		dropDetail string
	}{
		{
			name:          "symbol_not_defined is falsified by a definition",
			typ:           model.ClaimSymbolNotDefined,
			symbol:        "notok",
			contradicting: "func notok() { echo fail; }",
			falsified:     true,
			dropDetail:    "is defined at line 4",
		},
		{
			name:          "symbol_not_defined is not falsified by a call",
			typ:           model.ClaimSymbolNotDefined,
			symbol:        "notok",
			contradicting: "result = notok(",
			falsified:     false,
		},
		{
			name:          "symbol_not_called is falsified by a call",
			typ:           model.ClaimSymbolNotCalled,
			symbol:        "helper",
			contradicting: "result = helper(",
			falsified:     true,
			dropDetail:    "is called at line 4",
		},
		{
			name:          "symbol_not_called is not falsified by a definition",
			typ:           model.ClaimSymbolNotCalled,
			symbol:        "helper",
			contradicting: "func helper() { return 1 }",
			falsified:     false,
		},
		{
			name:          "occurrence_count_zero is falsified by any occurrence",
			typ:           model.ClaimOccurrenceCountZero,
			symbol:        "helper",
			contradicting: "// helper does the work",
			falsified:     true,
			dropDetail:    "occurs at line 4",
		},
		{
			name:          "occurrence_count_zero is not falsified by nothing at all",
			typ:           model.ClaimOccurrenceCountZero,
			symbol:        "helper",
			contradicting: "result = other(",
			falsified:     false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The file contradicts the claim: the finding is dropped.
			rec := runAbsenceClaim(t, []string{c.contradicting}, absenceClaim(c.typ, c.symbol), nil)
			if c.falsified {
				if len(rec.Findings) != 0 {
					t.Fatalf("Findings = %d, want 0 (the file contradicts the claim); findings %+v",
						len(rec.Findings), rec.Findings)
				}
				d := findDrop(rec, model.DropNegativeClaimFalsified)
				if d == nil {
					t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
				}
				if !strings.Contains(d.Detail, c.dropDetail) {
					t.Errorf("drop detail = %q, want it to contain %q", d.Detail, c.dropDetail)
				}
				return
			}
			// The line does not falsify the claim, so the claim stands
			// and the finding survives as the blocker it is.
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d, want 1 (the claim stands); drops %+v", len(rec.Findings), rec.Drops)
			}
			if !rec.Findings[0].Blocks {
				t.Errorf("Blocks = false, want true: the claim was checked and it holds")
			}
			if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "verified" {
				t.Errorf("Disposition = %q, want verified", got)
			}
		})
	}
}

// A declared claim the file bears out stands, and it can block. This is
// the finding a reviewer gets right, and the claim is what makes it
// checkable.
func TestADeclaredAbsenceClaimThatHoldsBlocks(t *testing.T) {
	for _, c := range []model.ClaimType{
		model.ClaimSymbolNotDefined,
		model.ClaimSymbolNotCalled,
		model.ClaimOccurrenceCountZero,
	} {
		t.Run(string(c), func(t *testing.T) {
			rec := runAbsenceClaim(t, nil, absenceClaim(c, "notok"), nil)
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
			}
			if !rec.Findings[0].Blocks {
				t.Errorf("Blocks = false, want true: a declared absence claim that holds blocks")
			}
			if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "verified" {
				t.Errorf("Disposition = %q, want verified", got)
			}
		})
	}
}

// An absence asserted in PROSE, with no declared claim, can never block.
// The phrase table still recognises the shape, so the finding is not
// dropped — it is demoted to a note.
//
// Before the change, this finding blocked: the phrase table granted
// "verified" and the ordinary blocking formula applied.
func TestAnUndeclaredAbsenceNeverBlocks(t *testing.T) {
	cases := []struct {
		name string
		// title and body are the finding's narrative.
		title string
		body  string
		// kind and assertion are the structural markers, when the finding
		// declares itself an absence.
		kind      string
		assertion string
	}{
		{
			name:  "an English phrase the table knows",
			title: "maybeSuffixForkIdentity is never called",
			body:  "The helper maybeSuffixForkIdentity is never invoked anywhere in this module.",
		},
		{
			name:      "a phrase in another language the table does not know",
			title:     "maybeSuffixForkIdentity n'est jamais appelée",
			body:      "La fonction maybeSuffixForkIdentity n'est jamais invoquée dans ce module.",
			kind:      "absent",
			assertion: "maybeSuffixForkIdentity n'est jamais appelée dans ce fichier",
		},
		{
			name:      "an absence declared by evidence_kind",
			title:     "The signature is never checked",
			body:      "Nothing in this file checks the signature.",
			kind:      "absent",
			assertion: "the signature is never checked anywhere in this file",
		},
		{
			name:      "an absence declared by missing_assertion",
			title:     "The signature is never checked",
			body:      "Nothing in this file checks the signature.",
			assertion: "la validation de la signature n'existe nulle part dans ce fichier",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := runAbsenceClaim(t, nil, nil, func(f map[string]any) {
				f["title"] = c.title
				f["body"] = c.body
				if c.kind != "" {
					f["evidence_kind"] = c.kind
				}
				if c.assertion != "" {
					f["missing_assertion"] = c.assertion
				}
			})
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d, want 1 (the finding survives as a note); drops %+v",
					len(rec.Findings), rec.Drops)
			}
			if rec.Findings[0].Blocks {
				t.Errorf("Blocks = true, want false: an undeclared absence was never mechanically checked")
			}
		})
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

// A declared claim that cannot be checked is a note, never a block, and it
// does not cost the file its review either.
//
// A claim with a symbol that is not a name gives the search nothing to
// look for. A claim about another path is about a file Cite did not
// check. A claim with no symbol at all names nothing. A claim whose own
// count contradicts its type is a claim that contradicts itself.
func TestUncheckableDeclaredAbsenceClaimsAreNotes(t *testing.T) {
	cases := []struct {
		name   string
		claims []any
	}{
		{
			name:   "the symbol is not a name",
			claims: absenceClaim(model.ClaimSymbolNotDefined, "the validation helper"),
		},
		{
			name:   "the symbol is missing",
			claims: absenceClaim(model.ClaimSymbolNotDefined, ""),
		},
		{
			name: "the claim is about another path",
			claims: []any{map[string]any{
				"type":    string(model.ClaimSymbolNotDefined),
				"subject": "notok",
				"symbol":  "notok",
				"path":    "other/file.sh",
			}},
		},
		{
			name: "the count contradicts the type",
			claims: []any{map[string]any{
				"type":    string(model.ClaimOccurrenceCountZero),
				"subject": "helper",
				"symbol":  "helper",
				"count":   3,
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := runAbsenceClaim(t, nil, c.claims, nil)
			if len(rec.Findings) != 1 {
				t.Fatalf("Findings = %d, want 1 (not dropped); drops %+v", len(rec.Findings), rec.Drops)
			}
			if rec.Findings[0].Blocks {
				t.Errorf("Blocks = true, want false: a claim nobody could check cannot block")
			}
			if got := rec.Findings[0].ExternalClaims[0].Disposition; got != "note" {
				t.Errorf("Disposition = %q, want note", got)
			}
		})
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
