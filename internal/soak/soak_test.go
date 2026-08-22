package soak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
)

const repoCases = "../../bench/cases"

func TestShippedCasesPass(t *testing.T) {
	rep, err := RunAll(repoCases)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("loaded %d case(s), want 3", len(rep.Results))
	}
	for _, cr := range rep.Results {
		if !cr.Pass() {
			t.Errorf("case %s failed:", cr.Name)
			for _, chk := range cr.Checks {
				if !chk.Pass {
					t.Errorf("  %s: %s", chk.Name, chk.Detail)
				}
			}
		}
	}
	if !rep.Pass() {
		t.Fatal("report should pass")
	}
}

func TestLoadCasesSortedAndNamed(t *testing.T) {
	cases, err := LoadCases(repoCases)
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	for i := 1; i < len(cases); i++ {
		if cases[i-1].Spec.Name >= cases[i].Spec.Name {
			t.Fatalf("cases not sorted by name: %s >= %s", cases[i-1].Spec.Name, cases[i].Spec.Name)
		}
	}
}

func TestLoadCasesMalformedErrorsLoudly(t *testing.T) {
	dir := t.TempDir()

	// A case directory whose case.json is broken JSON.
	broken := filepath.Join(dir, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "case.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCases(dir); err == nil {
		t.Fatal("broken case.json must be a loud error")
	} else if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error should name the case: %v", err)
	}

	// A case with an unparseable patch.
	badPatch := filepath.Join(dir, "badpatch")
	if err := os.MkdirAll(badPatch, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"name":"badpatch","patch":"@@ -1,1 +1,1 @@\n+x"}`
	if err := os.WriteFile(filepath.Join(badPatch, "case.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCases(dir); err == nil {
		t.Fatal("hunk header before any file header must error")
	}

	// A case with no patch at all.
	empty := filepath.Join(dir, "nopatch")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty, "case.json"), []byte(`{"name":"nopatch"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCases(dir); err == nil {
		t.Fatal("empty patch must error")
	}

	// No cases at all.
	emptyDir := t.TempDir()
	if _, err := LoadCases(emptyDir); err == nil {
		t.Fatal("an empty case dir must error, not produce an empty report")
	}
}

func TestRunCaseFailsWhenAnchorOutsideDiff(t *testing.T) {
	dir := t.TempDir()
	caseDir := filepath.Join(dir, "bad-anchor")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{
	  "name": "bad-anchor",
	  "patch": "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n+\tfunc F() {}\n",
	  "recorded_responses": [
	    {"path": "a.go", "response_json": "{\"schema_version\":1,\"path\":\"a.go\",\"outcome\":\"reviewed\",\"findings\":[{\"id\":\"x\",\"category\":\"crash\",\"anchor\":{\"start_line\":50,\"end_line\":50},\"title\":\"t\",\"body\":\"b\",\"impact\":\"i\",\"evidence\":[{\"line\":50,\"quote\":\"func F()\"}],\"external_claims\":[],\"introduced_by\":\"added_line\",\"confidence\":\"certain\",\"fix\":null}]}"}
	  ],
	  "expect": {"schema_valid": true, "anchors_in_diff": true, "fingerprints_stable": true, "replay_zero_new_threads": true}
	}`
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := LoadCases(dir)
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	res := RunCase(cases[0], RunOptions{})
	if res.Pass() {
		t.Fatal("an anchor at line 50 outside the hunks must fail anchors_in_diff")
	}
	var anchors CheckResult
	for _, c := range res.Checks {
		if c.Name == "anchors_in_diff" {
			anchors = c
		}
	}
	if anchors.Pass || !strings.Contains(anchors.Detail, "line 50") {
		t.Fatalf("anchors check = %+v, want failure naming line 50", anchors)
	}
}

func TestFingerprintStabilityCheckCatchesUnstableIdentity(t *testing.T) {
	fp := func(category, title, quote string) string {
		vf := model.ValidatedFinding{Finding: model.Finding{
			Category: model.Category(category),
			Title:    title,
			Evidence: []model.Evidence{{Line: 1, Quote: quote}},
		}}
		return vf.FingerprintOf()
	}
	// Sanity: the perturbation itself must not change FingerprintOf.
	base := fp("crash", "nil map write on empty config", `cfg.Values["k"]`)
	perturbed := fp("crash", perturbText("nil map write on empty config"), perturbText(`cfg.Values["k"]`))
	if base != perturbed {
		t.Fatalf("perturbation changed the fingerprint: %s vs %s", base, perturbed)
	}
	// And a real content change must change it.
	if base == fp("crash", "nil slice index on empty config", `cfg.Values["k"]`) {
		t.Fatal("different titles must produce different fingerprints")
	}
}

func TestReportRenderingStable(t *testing.T) {
	rep, err := RunAll(repoCases)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	a := RenderReport(rep)
	b := RenderReport(rep)
	if a != b {
		t.Fatal("RenderReport is not deterministic")
	}
	if !strings.Contains(a, "NOT a quality evaluation") {
		t.Errorf("report must carry the honest help line:\n%s", a)
	}
	if !strings.Contains(a, "RESULT: PASS") {
		t.Errorf("report should end PASS:\n%s", a)
	}
	if !strings.Contains(a, "3/3 case(s) passed.") {
		t.Errorf("report should carry totals:\n%s", a)
	}
	for _, name := range []string{"positive-injection", "clean-equivalent-rewrite", "near-miss-correct-unlock"} {
		if !strings.Contains(a, name) {
			t.Errorf("report missing case %s:\n%s", name, a)
		}
	}
}

func TestReportRenderingShowsFailure(t *testing.T) {
	rep := Report{Results: []CaseResult{{
		Name:   "failing",
		Checks: []CheckResult{{Name: "schema_valid", Pass: false, Detail: "boom"}},
	}}}
	out := RenderReport(rep)
	if !strings.Contains(out, "RESULT: FAIL") || !strings.Contains(out, "boom") {
		t.Fatalf("failure report incomplete:\n%s", out)
	}
	if !strings.Contains(out, "0/1 case(s) passed.") {
		t.Fatalf("totals wrong:\n%s", out)
	}
}

func TestNearMissCaseAssertsZeroFindings(t *testing.T) {
	cases, err := LoadCases(repoCases)
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	for _, c := range cases {
		if !strings.HasPrefix(c.Spec.Name, "near-miss") {
			continue
		}
		for _, rr := range c.Spec.Responses {
			fr, err := model.ParseFileReview([]byte(rr.ResponseJSON))
			if err != nil {
				t.Fatalf("%s: %v", c.Spec.Name, err)
			}
			if len(fr.Findings) != 0 {
				t.Errorf("near-miss case %s must record zero findings, got %d", c.Spec.Name, len(fr.Findings))
			}
		}
	}
}
