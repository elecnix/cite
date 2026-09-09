package reviewer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// reproDiff adds the import line at the top of a.go; the call site lives
// far below, outside the diff hunks — the issue-#45 shape.
const reproDiffText = `diff --git a/index.ts b/index.ts
--- a/index.ts
+++ b/index.ts
@@ -1,2 +1,3 @@
+import { maybeSuffixForkIdentity } from "./fork-identity.ts";
 context line one
 context line two
`

func reproInputs(callSite string) Inputs {
	df, err := scope.ParseUnifiedDiff(reproDiffText)
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	b.WriteString("import { maybeSuffixForkIdentity } from \"./fork-identity.ts\";\n")
	b.WriteString("context line one\n")
	b.WriteString("context line two\n")
	if callSite != "" {
		b.WriteString(callSite)
	}
	return Inputs{
		Manifest:      []scope.ManifestEntry{{Status: "M", Path: "index.ts", Adds: 1, Dels: 0}},
		Diffs:         map[string]*scope.DiffFile{"index.ts": df.Files[0]},
		PostImage:     map[string][]byte{"index.ts": []byte(b.String())},
		PRDescription: "Test PR",
		Nonce:         "nonce7f3a91",
	}
}

func reproFinding() map[string]any {
	f := mkFinding("f1", model.CategoryLogicInversion, 1, 1, "certain",
		[]map[string]any{mkEvidence(1, "import { maybeSuffixForkIdentity } from \"./fork-identity.ts\";")})
	f["title"] = "maybeSuffixForkIdentity imported but never called"
	f["body"] = "The full file contains no other occurrence of maybeSuffixForkIdentity, so the function is never invoked."
	return f
}

func reproScript(f map[string]any) func(int, model.CompletionRequest) (string, error) {
	return func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("index.ts"), nil
		}
		return reviewJSON("index.ts", "reviewed", []map[string]any{f}), nil
	}
}

// The issue-#45 repro: a blocking finding asserts "imported but never
// called" and "no other occurrence" while a call site exists in the same
// file outside the diff hunks. The mechanical check must drop it.
func TestNegativeClaimFalsifiedByCallSiteDropped(t *testing.T) {
	in := reproInputs("\tagentName = maybeSuffixForkIdentity(\n")
	c := &fakeClient{fn: reproScript(reproFinding())}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (falsified negative claim)", len(rec.Findings))
	}
	d := findDrop(rec, model.DropNegativeClaimFalsified)
	if d == nil {
		t.Fatalf("no DropNegativeClaimFalsified entry; drops %+v", rec.Drops)
	}
	if !strings.Contains(d.Detail, "line 4") {
		t.Errorf("Detail = %q, want the falsifying line number 4", d.Detail)
	}
}

// The negative claim is true (no call site anywhere in the file): the
// finding survives and the ordinary blocking formula applies.
func TestTrueNegativeClaimStillBlocks(t *testing.T) {
	in := reproInputs("")
	c := &fakeClient{fn: reproScript(reproFinding())}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1; drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true (true absence claim, certain, added anchor)")
	}
}

// "never called" is a call-shaped claim: the definition of the symbol in
// the same file does not contradict it, so a true "defined but never
// called" finding is not dropped.
func TestDefinitionOccurrenceDoesNotFalsifyCallClaim(t *testing.T) {
	in := reproInputs("func maybeSuffixForkIdentity(req *Request) *Response {\n")
	f := reproFinding()
	f["title"] = "maybeSuffixForkIdentity defined but never called"
	f["body"] = "The helper maybeSuffixForkIdentity is never called anywhere in this module."
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 || !rec.Findings[0].Blocks {
		t.Fatalf("Findings = %+v, want 1 blocking; drops %+v", rec.Findings, rec.Drops)
	}
}

// A strong "no other occurrence" claim is falsified by the definition
// itself — the claim asserts the absence of ANY occurrence.
func TestStrongOccurrenceClaimFalsifiedByDefinition(t *testing.T) {
	in := reproInputs("func maybeSuffixForkIdentity(req *Request) *Response {\n")
	f := reproFinding()
	f["body"] = "The full file contains no other occurrence of maybeSuffixForkIdentity."
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0", len(rec.Findings))
	}
	if findDrop(rec, model.DropNegativeClaimFalsified) == nil {
		t.Fatalf("no DropNegativeClaimFalsified entry; drops %+v", rec.Drops)
	}
}

// On a partially-sent file a narrative negative claim about the full file
// was never actually observed; it fails open to a note — it survives but
// must never block.
func TestUnverifiableNegativeClaimOnPartialFileNeverBlocks(t *testing.T) {
	in := reproInputs("")
	var b strings.Builder
	b.WriteString("import { maybeSuffixForkIdentity } from \"./fork-identity.ts\";\n")
	b.WriteString("context line one\n")
	b.WriteString("context line two\n")
	for i := 0; i < maxFileLines+10; i++ {
		fmt.Fprintf(&b, "padding filler line number %05d\n", i)
	}
	in.PostImage["index.ts"] = []byte(b.String())
	f := reproFinding()
	f["title"] = "maybeSuffixForkIdentity never used by this module"
	c := &fakeClient{fn: reproScript(f)}
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

// A negative claim whose symbol cannot be mechanically extracted (plain
// English prose, no backticks, no identifier-shaped token) cannot be
// checked. On a fully-sent file the claim may survive as a note, but it
// must never ground a merge blocker — fail open (issue #45, "no
// extractable symbol" branch of the stated design).
func TestUnextractableSymbolNegativeClaimNeverBlocks(t *testing.T) {
	in := reproInputs("\tresult = helper(\n")
	f := reproFinding()
	f["title"] = "the helper is never called by this module"
	f["body"] = "No call site for the helper exists, so the branch is dead."
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (fail open to note); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: the negative claim could not be mechanically verified")
	}
}
