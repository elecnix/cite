package reviewer

// Issue #73: the reviewer intermittently cannot evaluate a diff at all and
// the merge gate turns that silence into a hard veto. Measured on the
// operator's own CI: the failure set moved between attempts (2 files, then
// 4, then 1) while all 13 per-call API outcomes were "ok" — the model calls
// succeeded; the responses just violated the per-file output contract, and
// the retry budget (run-global, 3 tokens) drained or the deadline expired
// with no second chance. These tests pin the fix:
//
//   - a deterministic echo guard: a schema-valid response naming any other
//     path is a labeling error, relabeled in code without a model round-trip;
//   - a per-file parse budget separate from the run-global transient bucket,
//     so one pathological file cannot starve coverage for the rest;
//   - exactly one deadline re-ask with a fresh budget, distinct from the
//     parse-failure path;
//   - the judge's condition (N files never evaluated, m re-asks spent)
//     surfaced on the run record for the gate to carry into the sticky state.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// reviewCallCount returns the number of review (non-triage) model calls a
// recorded client made.
func reviewCallCount(c *fakeClient) int {
	n := 0
	for _, call := range c.calls {
		if !isTriageCall(call) {
			n++
		}
	}
	return n
}

// fileOutcomeOf returns the recorded outcome for path, or nil.
func fileOutcomeOf(rec *model.RunRecord, path string) *model.FileOutcome {
	for i := range rec.Files {
		if rec.Files[i].Path == path {
			return &rec.Files[i]
		}
	}
	return nil
}

// The measured CI shape: the model returns a schema-valid review but echoes
// the wrong path ("unknown", "", another file). The payload contained exactly
// one file, so whatever the model reviewed, it reviewed the bytes we sent —
// the path field is a label, not a claim. The echo guard relabels it
// deterministically and spends no model round-trip: one review call, file
// reviewed, findings validated normally. Before the guard this burned the
// run-global bucket re-asking and ended errored(parse_failure) once drained.
func TestEchoGuardRelabelsWrongPathWithoutReask(t *testing.T) {
	in := baseInputs()
	f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
		[]map[string]any{mkEvidence(2, "added line alpha here")})
	for _, echoed := range []string{"unknown", "", "README.md"} {
		c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
			if isTriageCall(req) {
				return triageJSON("a.go"), nil
			}
			return reviewJSON(echoed, "reviewed", []map[string]any{f}), nil
		}}
		rec, err := runOnce(t, in, baseOptions(c))
		if err != nil {
			t.Fatalf("echo %q: Run: %v", echoed, err)
		}
		if got := reviewCallCount(c); got != 1 {
			t.Errorf("echo %q: review calls = %d, want 1 (the guard must relabel without a re-ask)", echoed, got)
		}
		fo := fileOutcomeOf(rec, "a.go")
		if fo == nil || fo.State != model.FileReviewed || !fo.Reviewed {
			t.Fatalf("echo %q: file outcome = %+v, want reviewed via the echo guard", echoed, fo)
		}
		if len(rec.Findings) != 1 || !rec.Findings[0].Blocks {
			t.Errorf("echo %q: findings = %+v, want the finding validated after relabeling", echoed, rec.Findings)
		}
	}
}

// The echo guard must not weaken validation: a relabeled response whose
// findings quote lines that never existed in the file under review still has
// every finding dropped by the evidence cascade. Relabeling fixes the label,
// never the content.
func TestEchoGuardKeepsFindingsFailClosed(t *testing.T) {
	in := baseInputs()
	f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
		[]map[string]any{mkEvidence(2, "this line was never in the file")})
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return reviewJSON("some/other/path.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := reviewCallCount(c); got != 1 {
		t.Errorf("review calls = %d, want 1 (echo guard, no re-ask)", got)
	}
	if fo := fileOutcomeOf(rec, "a.go"); fo == nil || fo.State != model.FileReviewed {
		t.Fatalf("file outcome = %+v, want reviewed (relabel succeeds even though findings drop)", fo)
	}
	if len(rec.Findings) != 0 {
		t.Errorf("findings = %d, want 0 — a hallucinated quote must still drop", len(rec.Findings))
	}
	if findDrop(rec, model.DropEvidenceMismatch) == nil {
		t.Fatalf("no DropEvidenceMismatch entry; drops %+v", rec.Drops)
	}
	if rec.EchoCorrections != 1 {
		t.Errorf("EchoCorrections = %d, want 1", rec.EchoCorrections)
	}
}

// One pathological file must not starve coverage for the rest: output-contract
// failures draw from a per-file budget, not the run-global bucket. Here a.go
// always returns a blank body (the measured OpenRouter shape) and exhausts
// its OWN budget; b.go's first two blanks then a valid response still gets
// its re-asks and reviews clean. Under the old run-global bucket a.go drained
// all three tokens and b.go went terminal on its first blank.
func TestPerFileParseBudgetSparesOtherFiles(t *testing.T) {
	in := baseInputs()
	in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "M", Path: "b.go", Adds: 1, Dels: 0})
	dfB, err := scope.ParseUnifiedDiff(fmt.Sprintf(`diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1,2 +1,3 @@
 context line one
+added line alpha here
 context line two
`))
	if err != nil {
		t.Fatal(err)
	}
	in.Diffs["b.go"] = dfB.Files[0]
	in.PostImage["b.go"] = []byte("context line one\nadded line alpha here\ncontext line two\n")

	bFails := 0
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		switch requestPath(req) {
		case "a.go":
			return "", nil // blank body, every attempt
		case "b.go":
			bFails++
			if bFails <= 2 {
				return "", nil // blank twice, then good — needs its full budget
			}
			return reviewJSON("b.go", "reviewed", nil), nil
		}
		return reviewJSON(requestPath(req), "reviewed", nil), nil
	}}
	o := baseOptions(c)
	o.Cfg = config.Default()
	o.Cfg.Roles = map[model.Role]config.RoleSpec{
		model.RoleReview: {Concurrency: 1}, // deterministic order: a.go first
	}
	rec, err := runOnce(t, in, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// a.go: 1 initial + its per-file budget, then terminal with the budget
	// spent recorded on the outcome.
	foA := fileOutcomeOf(rec, "a.go")
	if foA == nil || foA.State != model.FileErrored || foA.Reason != "parse_failure" {
		t.Fatalf("a.go outcome = %+v, want errored(parse_failure)", foA)
	}
	if foA.Reasks != defaultPerFileParseRetries {
		t.Errorf("a.go Reasks = %d, want %d (per-file budget spent)", foA.Reasks, defaultPerFileParseRetries)
	}
	// b.go: must still have had its own budget to draw on.
	foB := fileOutcomeOf(rec, "b.go")
	if foB == nil || foB.State != model.FileReviewed || !foB.Reviewed {
		t.Fatalf("b.go outcome = %+v, want reviewed — the pathological file must not starve it", foB)
	}
	if rec.ReasksSpent != defaultPerFileParseRetries+2 {
		t.Errorf("ReasksSpent = %d, want %d (a.go's budget + b.go's two re-asks)",
			rec.ReasksSpent, defaultPerFileParseRetries+2)
	}
}

// A genuinely unmeasurable file still fails fast: at most 1 initial attempt
// plus the per-file budget, then terminal parse_failure carrying the budget
// spent — a bounded red, not an unbounded one.
func TestPerFileParseBudgetExhaustedRecordsBudgetSpent(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return "", nil // blank body, every attempt
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := reviewCallCount(c); got != 1+defaultPerFileParseRetries {
		t.Errorf("review calls = %d, want %d (per-file budget bounded)", got, 1+defaultPerFileParseRetries)
	}
	fo := fileOutcomeOf(rec, "a.go")
	if fo == nil || fo.State != model.FileErrored || fo.Reason != "parse_failure" {
		t.Fatalf("file outcome = %+v, want errored(parse_failure)", fo)
	}
	if fo.Reasks != defaultPerFileParseRetries {
		t.Errorf("Reasks = %d, want %d — the outcome must carry the budget spent", fo.Reasks, defaultPerFileParseRetries)
	}
	if rec.Coverage.Complete {
		t.Errorf("coverage complete despite errored file — gate must fail closed")
	}
}

// A deadline expiry gets exactly one re-ask from a fresh per-file deadline
// budget — distinct from the parse budget and from the run-global transient
// bucket — then goes terminal. The first retry here rescues the file.
func TestDeadlineExceededRetriedOnceWithFreshBudget(t *testing.T) {
	in := baseInputs()
	calls := 0
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		calls++
		if calls == 1 {
			return "", model.ErrDeadline
		}
		return reviewJSON("a.go", "reviewed", nil), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := reviewCallCount(c); got != 2 {
		t.Errorf("review calls = %d, want 2 (one fresh-budget deadline re-ask)", got)
	}
	if fo := fileOutcomeOf(rec, "a.go"); fo == nil || fo.State != model.FileReviewed || !fo.Reviewed {
		t.Fatalf("file outcome = %+v, want reviewed after the deadline re-ask", fo)
	}
	if rec.ReasksSpent != 1 {
		t.Errorf("ReasksSpent = %d, want 1", rec.ReasksSpent)
	}
}

// A call that times out twice is still terminal — bounded, with the mechanism
// and the budget spent on the outcome. A deadline retry must never turn a
// fast red into a slow unbounded one.
func TestDeadlineRetryExhaustedStaysTerminal(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return "", model.ErrDeadline
	}}
	var logs []string
	o := baseOptions(c)
	o.Logger = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	rec, err := runOnce(t, in, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := reviewCallCount(c); got != 1+defaultPerFileDeadlineRetries {
		t.Errorf("review calls = %d, want %d (one deadline re-ask, then terminal)",
			got, 1+defaultPerFileDeadlineRetries)
	}
	fo := fileOutcomeOf(rec, "a.go")
	if fo == nil || fo.State != model.FileErrored || fo.Reason != "deadline_exceeded" {
		t.Fatalf("file outcome = %+v, want errored(deadline_exceeded)", fo)
	}
	if fo.Reasks != defaultPerFileDeadlineRetries {
		t.Errorf("Reasks = %d, want %d — the outcome must carry the budget spent", fo.Reasks, defaultPerFileDeadlineRetries)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"roles.review.timeout", "fresh-budget"} {
		if !strings.Contains(joined, want) {
			t.Errorf("deadline logs missing %q; logs:\n%s", want, joined)
		}
	}
}
