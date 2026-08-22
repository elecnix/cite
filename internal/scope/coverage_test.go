package scope

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

func outcome(path, state, reason string) model.FileOutcome {
	return model.FileOutcome{Path: path, State: model.FileTerminalState(state), Reason: reason}
}

func TestComputeCoverageArithmetic(t *testing.T) {
	files := []model.FileOutcome{
		outcome("a.go", "reviewed", ""),
		outcome("b.go", "reviewed", ""),
		outcome("go.sum", "skipped", SkipReasonLockfile),
		outcome("big.min.js", "skipped", SkipReasonMinified),
	}
	c := ComputeCoverage(files, 4)
	if c.APIFiles != 4 || c.Reviewed != 2 || c.ApprovedSkip != 2 || c.Errored != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if !c.Complete {
		t.Fatal("count(reviewed ∪ approved-skip)==count(api files): must be complete")
	}

	// An errored file is terminal but not covered: completeness breaks.
	c = ComputeCoverage(append(files, outcome("c.go", "error", "")), 5)
	if c.Complete || c.Errored != 1 {
		t.Fatalf("error must break coverage: %+v", c)
	}

	// An unapproved skip (risk cutoff) is not covered: fail closed.
	c = ComputeCoverage([]model.FileOutcome{
		outcome("a.go", "reviewed", ""),
		outcome("huge.go", "skipped", SkipReasonRiskCutoff),
	}, 2)
	if c.Complete || c.ApprovedSkip != 0 {
		t.Fatalf("risk cutoff must not count as covered: %+v", c)
	}

	// Over-covering (more reviewed than API files) cannot pass either.
	c = ComputeCoverage(files, 1)
	if c.Complete {
		t.Fatal("more outcomes than api files must not be complete")
	}
}

func TestComputeCoverageZeroInScope(t *testing.T) {
	// The dangerous shape: the GitHub API listed changed files but nothing
	// was reviewed or approved-skipped — a path-filter bypass goes green
	// exactly here (§11). Complete must be false.
	c := ComputeCoverage([]model.FileOutcome{}, 7)
	if c.Complete || c.APIFiles != 7 {
		t.Fatalf("%+v", c)
	}
	if !EmptyInScope(c) {
		t.Fatal("EmptyInScope must flag the bypass shape")
	}

	// A PR with no changed files at all: flagged too, but not the bypass shape.
	c = ComputeCoverage(nil, 0)
	if c.Complete {
		t.Fatal("zero changed files must not silently pass")
	}
	if EmptyInScope(c) {
		t.Fatal("APIFiles==0 is not the in-scope-bypass shape")
	}

	// Sanity: a fully-covered run is not empty.
	ok := ComputeCoverage([]model.FileOutcome{outcome("a.go", "reviewed", "")}, 1)
	if !ok.Complete || EmptyInScope(ok) {
		t.Fatalf("%+v", ok)
	}
}
