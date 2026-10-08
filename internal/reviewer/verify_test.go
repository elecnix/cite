package reviewer

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// verifyScript answers triage, then one crash finding on a.go line 2, then
// the given verdict for the verify call.
func verifyScript(verdict string) (*fakeClient, *[]model.CompletionRequest) {
	var verifyCalls []model.CompletionRequest
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		switch {
		case isTriageCall(req):
			return triageJSON("a.go"), nil
		case req.System == embeddedVerifyPrompt:
			verifyCalls = append(verifyCalls, req)
			return verdict, nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain", []map[string]any{mkEvidence(2, "added line alpha here")})
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	return c, &verifyCalls
}

func runVerified(t *testing.T, verdict string) (*model.RunRecord, []model.CompletionRequest) {
	t.Helper()
	c, calls := verifyScript(verdict)
	o := baseOptions(c)
	o.Verify = true
	rec, err := runOnce(t, baseInputs(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rec, *calls
}

func TestVerifierSupportedKeepsTheFindingBlocking(t *testing.T) {
	rec, calls := runVerified(t, `{"verdict":"supported","refuting_path":"","refuting_line":0,"refuting_quote":"","reason":"nil reaches line 2"}`)
	if len(calls) != 1 {
		t.Fatalf("verify calls = %d, want 1", len(calls))
	}
	if len(rec.Findings) != 1 || !rec.Findings[0].Blocks || rec.Findings[0].VerifierResult != "supported" {
		t.Fatalf("want one supported blocking finding, got %+v", rec.Findings)
	}
	user := calls[0].User
	for _, want := range []string{`<file_under_review path="a.go">`, "0002 +|added line alpha here", `<claim trust="untrusted">`, "| category: crash", "| evidence line 2: added line alpha here"} {
		if !strings.Contains(user, want) {
			t.Errorf("verify request missing %q:\n%s", want, user)
		}
	}
}

// A refutation with a quote that is the named line drops the finding.
func TestVerifierRefutationWithAQuotedLineDrops(t *testing.T) {
	rec, _ := runVerified(t, `{"verdict":"unsupported","refuting_path":"","refuting_line":1,"refuting_quote":"context line one","reason":"line 1 guards it"}`)
	if len(rec.Findings) != 0 {
		t.Fatalf("refuted finding survived: %+v", rec.Findings)
	}
	d := findDrop(rec, model.DropVerifierUnsupported)
	if d == nil || !strings.Contains(d.Detail, "line 1 guards it") || !strings.Contains(d.Detail, "context line one") {
		t.Fatalf("drop should carry the reason and the refuting line: %+v", rec.Drops)
	}
}

// A refutation whose quote is not on the named line cannot drop a finding:
// the verifier invented a guard. The finding stays as a note.
func TestVerifierRefutationWithAnInventedQuoteDoesNotDrop(t *testing.T) {
	rec, _ := runVerified(t, `{"verdict":"unsupported","refuting_path":"","refuting_line":1,"refuting_quote":"if x == nil { return }","reason":"guarded"}`)
	if len(rec.Findings) != 1 {
		t.Fatalf("finding dropped on an invented refutation: drops %+v", rec.Drops)
	}
	if rec.Findings[0].Blocks || rec.Findings[0].VerifierResult != string(VerifierNeedsContextNotProvided) {
		t.Fatalf("want a non-blocking needs-context note, got %+v", rec.Findings[0])
	}
}

func TestVerifierNeedsContextDemotesToANote(t *testing.T) {
	rec, _ := runVerified(t, `{"verdict":"needs_context","refuting_path":"","refuting_line":0,"refuting_quote":"","reason":"depends on the caller"}`)
	if len(rec.Findings) != 1 || rec.Findings[0].Blocks {
		t.Fatalf("want a non-blocking note, got %+v", rec.Findings)
	}
}

func TestVerifierGarbageFailsOpenToANote(t *testing.T) {
	rec, _ := runVerified(t, `not json`)
	if len(rec.Findings) != 1 || rec.Findings[0].Blocks || rec.Findings[0].VerifierResult != string(VerifierError) {
		t.Fatalf("want a non-blocking note with verifier error, got %+v", rec.Findings)
	}
}

func TestRefutationQuotedInRelatedCode(t *testing.T) {
	in := VerifyInput{Path: "a.go", Lines: []string{"x"}}
	in.Related = append(in.Related, relatedSnippetFixture())
	ok := refutationQuoted(in, verdictAnswer{RefutingPath: "lib/p.go", RefutingLine: 11, RefutingQuote: `if s == "" {`})
	if !ok {
		t.Fatal("a quote of the related excerpt's line must count")
	}
	if refutationQuoted(in, verdictAnswer{RefutingPath: "lib/p.go", RefutingLine: 12, RefutingQuote: `if s == "" {`}) {
		t.Fatal("the same quote on the wrong line must not count")
	}
}

func TestVerifyPromptCopiesStayInSync(t *testing.T) {
	if readFile(t, "../../prompts/verify.system.md") != embeddedVerifyPrompt {
		t.Fatal("prompts/verify.system.md and the embedded internal/reviewer/prompts/verify.system.md differ")
	}
}

func relatedSnippetFixture() scope.RelatedSnippet {
	return scope.RelatedSnippet{Path: "lib/p.go", Symbol: "P", Kind: "definition", StartLine: 10,
		Lines: []string{"func P(s string) {", `	if s == "" {`, "		return", "	}"}}
}

// A fragment found in nearly every line ("if", "return") is not a quote of
// the line and cannot drop a finding.
func TestRefutationNeedsASubstantialQuote(t *testing.T) {
	in := VerifyInput{Path: "a.go", Lines: []string{`	if len(calls) < 4 || len(efforts) < 4 { // guard`}}
	for q, want := range map[string]bool{
		"if": false,
		"return": false,
		"if len(calls) < 4 || len(efforts) < 4 {": true,
		"len(calls) < 4 || len(efforts) < 4": true,
	} {
		if got := refutationQuoted(in, verdictAnswer{RefutingLine: 1, RefutingQuote: q}); got != want {
			t.Errorf("quote %q: %v, want %v", q, got, want)
		}
	}
}
