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

// Issue #68 repro: a weak call-claim ("imported but never called") that
// quotes its own call site as evidence must still be falsified by that
// call site. The quoted-span exclusion is load-bearing only for strong
// occurrence-claims; applying it to weak call-claims lets a fabricated
// "never called" that quotes the call site verify clean and block.
func TestWeakCallClaimQuotingCallSiteDropped(t *testing.T) {
	in := reproInputs("\tagentName = maybeSuffixForkIdentity(\n")
	f := reproFinding()
	f["title"] = "maybeSuffixForkIdentity imported but never called"
	f["body"] = "The symbol maybeSuffixForkIdentity is never invoked anywhere in this file."
	f["evidence"] = []map[string]any{mkEvidence(4, "agentName = maybeSuffixForkIdentity(")}
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (weak call-claim falsified by its own quoted call site); drops %+v", len(rec.Findings), rec.Drops)
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
//
// Issue #203: the absence has to be DECLARED for it to be checkable. The
// declared claim is what the mechanical check runs on, so a claim asserted
// only in prose is no longer checkable and cannot ground a blocker. The
// declared form of this claim is symbol_not_called.
func TestTrueNegativeClaimStillBlocks(t *testing.T) {
	in := reproInputs("")
	f := reproFinding()
	f["external_claims"] = absenceClaim(model.ClaimSymbolNotCalled, "maybeSuffixForkIdentity")
	c := &fakeClient{fn: reproScript(f)}
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
	f["external_claims"] = absenceClaim(model.ClaimSymbolNotCalled, "maybeSuffixForkIdentity")
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

// --- definition-absence claims (issue #201) --------------------------------

// shellDiffText adds one line to test.sh: the one-line if form whose failure
// branch calls notok. notok is defined on the line below it, which is the
// shape Cite published as "notok is undefined" with blocks=true, so the pull
// request it reviewed could not go green.
const shellDiffText = `diff --git a/test.sh b/test.sh
--- a/test.sh
+++ b/test.sh
@@ -1,1 +1,3 @@
+if [ "$rc" = 0 ]; then ok "exited 0"; else notok "exited $rc"; fi
+notok() { echo "  FAIL: $1" >&2; fail=$((fail + 1)); }
 context line one
`

// shellInputs builds the post-image whose second line defines notok, or a
// post-image without the definition when defined is false: the same call
// site, one file that defines the helper and one that does not.
func shellInputs(defined bool) Inputs {
	df, err := scope.ParseUnifiedDiff(shellDiffText)
	if err != nil {
		panic(err)
	}
	post := "if [ \"$rc\" = 0 ]; then ok \"exited 0\"; else notok \"exited $rc\"; fi\n"
	if defined {
		post += "notok() { echo \"  FAIL: $1\" >&2; fail=$((fail + 1)); }\n"
	}
	post += "context line one\n"
	return Inputs{
		Manifest:      []scope.ManifestEntry{{Status: "M", Path: "test.sh", Adds: 2, Dels: 0}},
		Diffs:         map[string]*scope.DiffFile{"test.sh": df.Files[0]},
		PostImage:     map[string][]byte{"test.sh": []byte(post)},
		PRDescription: "Test PR",
		Nonce:         "nonce7f3a91",
	}
}

// shellFinding is the claim Cite filed: an all-lowercase helper named in the
// title, asserted undefined, anchored on the one-line if that calls it.
func shellFinding() map[string]any {
	f := mkFinding("f1", model.CategoryLogicInversion, 1, 1, "certain",
		[]map[string]any{mkEvidence(1, `if [ "$rc" = 0 ]; then ok "exited 0"; else notok "exited $rc"; fi`)})
	f["title"] = "notok is undefined: failure paths never increment fail"
	f["body"] = "notok is invoked here but the file never defines it, so the failure path never increments fail."
	return f
}

// The definition sits in the post-image the reviewer itself quoted, so the
// claim is mechanically false and the finding is dropped, not softened to a
// note: a note that names a helper the file defines is still a false claim
// published, only quieter.
func TestDefinitionAbsenceClaimFalsifiedByTheDefinitionInTheFile(t *testing.T) {
	f := shellFinding()
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, shellInputs(true), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (notok is defined at line 2); findings %+v", len(rec.Findings), rec.Findings)
	}
	d := findDrop(rec, model.DropNegativeClaimFalsified)
	if d == nil {
		t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
	}
	if !strings.Contains(d.Detail, "is defined at line 2") {
		t.Errorf("drop detail = %q, want it to name the definition at line 2", d.Detail)
	}
}

// The shape discipline: a definition claim is falsified by a definition and
// by nothing else. The same call site in a file that defines notok nowhere
// leaves the claim standing, so the finding survives as the blocker it is:
// this is the finding a reviewer gets right, and a call-shaped rule would
// have dropped it on its own quoted call site.
func TestDefinitionAbsenceClaimNotFalsifiedByItsCallSite(t *testing.T) {
	f := shellFinding()
	f["external_claims"] = absenceClaim(model.ClaimSymbolNotDefined, "notok")
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, shellInputs(false), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (nothing defines notok); drops %+v", len(rec.Findings), rec.Drops)
	}
	if !rec.Findings[0].Blocks {
		t.Errorf("Blocks = false, want true: a call site does not answer a definition claim")
	}
}

// A definition-absence sentence whose subject is a filler word names no
// symbol, so nothing can be searched for and the finding fails open to a
// note. It never becomes a false drop of some English word's definition.
func TestDefinitionAbsenceClaimWithoutASubjectNeverBlocks(t *testing.T) {
	f := shellFinding()
	f["title"] = "the function is undefined"
	f["body"] = "the helper is called before anything defines it, so the run dies."
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, shellInputs(true), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1 (fail open to note); drops %+v", len(rec.Findings), rec.Drops)
	}
	if rec.Findings[0].Blocks {
		t.Errorf("Blocks = true, want false: no symbol could be extracted from the claim")
	}
}

// The copula belongs to the phrase. With the bare "not defined" matching
// first, the token before it would be "is", the filler filter would reject
// it, and the claim would fall open to a note instead of being falsified by
// the definition the finding ignores.
func TestDefinitionAbsenceClaimWithCopulaFindsItsSubject(t *testing.T) {
	f := shellFinding()
	f["title"] = "notok is not defined anywhere"
	f["body"] = "The failure branch calls notok, which nothing declares."
	c := &fakeClient{fn: reproScript(f)}
	rec, err := runOnce(t, shellInputs(true), baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (notok is defined at line 2); findings %+v", len(rec.Findings), rec.Findings)
	}
	if findDrop(rec, model.DropNegativeClaimFalsified) == nil {
		t.Fatalf("no %s drop; drops %+v", model.DropNegativeClaimFalsified, rec.Drops)
	}
}
