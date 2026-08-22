// Package soak implements `cite soak`: the pipeline regression harness of
// PLAN §15 ("What replay is and is not").
//
// THIS IS NOT A QUALITY EVALUATION. It measures nothing about how good the
// reviewer is — no labels, no precision, no recall. Re-running the reviewer
// over historical pull requests is not an evaluation: the merged code is the
// fixed code and the sample is survivorship-biased. Soak exercises the
// plumbing instead:
//
//   - schema validity of recorded model responses,
//   - anchor lines landing inside the diff's anchorable set,
//   - fingerprint stability across a simulated reformat,
//   - replay-twice reconciliation posting zero new threads the second time.
//
// Every CLI surface that renders these results must say so.
package soak

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
	"github.com/elecnix/cite/internal/scope"
)

// HelpText is the one honest sentence every caller must show (§15).
const HelpText = "cite soak is a pipeline regression harness, NOT a quality evaluation or A/B test: it checks schema validity, anchors in the diff, fingerprint stability across reformats, and replay-twice carry-forward."

// CaseFileSpec is the on-disk shape of cases/<name>/case.json.
type CaseFileSpec struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Base        []BaseFile   `json:"base"`
	Patch       string       `json:"patch"`
	Responses   []RecordedRe `json:"recorded_responses"`
	Expect      Expectations `json:"expect"`
}

// BaseFile is one file of the base tree.
type BaseFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RecordedRe is one recorded model response for a path.
type RecordedRe struct {
	Path         string `json:"path"`
	ResponseJSON string `json:"response_json"`
}

// Expectations declares what a healthy pipeline does with this case.
type Expectations struct {
	SchemaValid          bool `json:"schema_valid"`
	AnchorsInDiff        bool `json:"anchors_in_diff"`
	FingerprintsStable   bool `json:"fingerprints_stable"`
	ReplayZeroNewThreads bool `json:"replay_zero_new_threads"`
}

// Case is a loaded case ready to run.
type Case struct {
	Spec CaseFileSpec
	Dir  string // source directory, for diagnostics
}

// CheckResult is the outcome of one pipeline check.
type CheckResult struct {
	Name   string
	Pass   bool
	Detail string
}

// CaseResult is one case's full outcome.
type CaseResult struct {
	Name     string
	Checks   []CheckResult
	Duration time.Duration
}

// Pass reports whether every check passed.
func (r CaseResult) Pass() bool {
	for _, c := range r.Checks {
		if !c.Pass {
			return false
		}
	}
	return true
}

// Report is the aggregate over all cases.
type Report struct {
	Results []CaseResult
}

// Pass reports whether every case passed.
func (r Report) Pass() bool {
	for _, cr := range r.Results {
		if !cr.Pass() {
			return false
		}
	}
	return len(r.Results) > 0
}

// RunOptions carries determinism knobs for the harness.
type RunOptions struct {
	// Now fixes the reconciliation instant so ledger expiry cannot make the
	// harness flaky. Zero means a fixed epoch is used.
	Now time.Time
}

// LoadCases reads every cases/<name>/case.json under dir. A malformed case
// is an error, never a skipped row: a harness that silently drops broken
// cases regresses exactly when it is needed most.
func LoadCases(dir string) ([]Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("soak: read case dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var cases []Case
	seen := map[string]bool{}
	for _, name := range names {
		path := filepath.Join(dir, name, "case.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("soak: %s: %w", name, err)
		}
		var spec CaseFileSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("soak: %s: parse case.json: %w", name, err)
		}
		if err := validateSpec(&spec); err != nil {
			return nil, fmt.Errorf("soak: %s: %w", name, err)
		}
		if seen[spec.Name] {
			return nil, fmt.Errorf("soak: duplicate case name %q", spec.Name)
		}
		seen[spec.Name] = true
		cases = append(cases, Case{Spec: spec, Dir: filepath.Join(dir, name)})
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("soak: no cases found under %s", dir)
	}
	return cases, nil
}

func validateSpec(s *CaseFileSpec) error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("case.json missing name")
	}
	if s.Patch == "" {
		return fmt.Errorf("case %q has an empty patch", s.Name)
	}
	if _, err := scope.ParseUnifiedDiff(s.Patch); err != nil {
		return fmt.Errorf("case %q patch does not parse: %w", s.Name, err)
	}
	return nil
}

// RunCase executes the four pipeline checks against one case.
func RunCase(c Case, opts RunOptions) CaseResult {
	start := time.Now()
	res := CaseResult{Name: c.Spec.Name}

	res.Checks = append(res.Checks, checkSchema(c))
	res.Checks = append(res.Checks, checkAnchors(c))
	res.Checks = append(res.Checks, checkFingerprintStability(c))
	res.Checks = append(res.Checks, checkReplayZeroNewThreads(c, opts))

	res.Duration = time.Since(start)
	return res
}

// RunAll loads and runs every case under dir.
func RunAll(dir string) (Report, error) {
	cases, err := LoadCases(dir)
	if err != nil {
		return Report{}, err
	}
	opts := RunOptions{} // fixed default epoch inside RunCase
	rep := Report{}
	for _, c := range cases {
		rep.Results = append(rep.Results, RunCase(c, opts))
	}
	return rep, nil
}

// --- check 1: schema validity ---------------------------------------------

func checkSchema(c Case) CheckResult {
	cr := CheckResult{
		Name: "schema_valid",
		Pass: c.Spec.Expect.SchemaValid,
	}
	parsed := make(map[string]*model.FileReview, len(c.Spec.Responses))
	var problems []string
	for _, rr := range c.Spec.Responses {
		fr, err := model.ParseFileReview([]byte(rr.ResponseJSON))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rr.Path, err))
			continue
		}
		parsed[rr.Path] = fr
	}
	if c.Spec.Expect.SchemaValid && len(problems) > 0 {
		cr.Pass = false
		cr.Detail = strings.Join(problems, "; ")
		return cr
	}
	if !c.Spec.Expect.SchemaValid {
		if len(problems) == 0 {
			cr.Pass = false
			cr.Detail = "expected schema-invalid responses but all parsed"
		} else {
			cr.Pass = true
			cr.Detail = "rejected as expected: " + strings.Join(problems, "; ")
		}
		return cr
	}
	cr.Pass = true
	cr.Detail = fmt.Sprintf("%d response(s) parse cleanly", len(parsed))
	return cr
}

// parsedResponses returns the schema-valid reviews, or nil when any fails.
func parsedResponses(c Case) map[string]*model.FileReview {
	out := make(map[string]*model.FileReview, len(c.Spec.Responses))
	for _, rr := range c.Spec.Responses {
		fr, err := model.ParseFileReview([]byte(rr.ResponseJSON))
		if err != nil {
			return nil
		}
		out[rr.Path] = fr
	}
	return out
}

// --- check 2: anchors inside the diff --------------------------------------

func checkAnchors(c Case) CheckResult {
	cr := CheckResult{Name: "anchors_in_diff"}
	diff, err := scope.ParseUnifiedDiff(c.Spec.Patch)
	if err != nil {
		cr.Pass = false
		cr.Detail = fmt.Sprintf("patch does not parse: %v", err)
		return cr
	}
	reviews := parsedResponses(c)
	if reviews == nil {
		cr.Pass = false
		cr.Detail = "responses do not parse; anchors unverifiable"
		return cr
	}
	var problems []string
	nFindings := 0
	for path, fr := range reviews {
		df := diff.FileByPath(path)
		if df == nil {
			problems = append(problems, fmt.Sprintf("%s: path absent from diff", path))
			continue
		}
		anchorable := df.AnchorableLines()
		for i := range fr.Findings {
			f := &fr.Findings[i]
			nFindings++
			for line := f.Anchor.StartLine; line <= f.Anchor.EndLine; line++ {
				if !anchorable[line] {
					problems = append(problems,
						fmt.Sprintf("%s: finding %s anchors line %d outside the hunks", path, f.ID, line))
				}
			}
		}
	}
	if len(problems) > 0 {
		cr.Pass = false
		cr.Detail = strings.Join(problems, "; ")
		return cr
	}
	cr.Pass = true
	cr.Detail = fmt.Sprintf("%d finding(s) anchored inside the diff", nFindings)
	return cr
}

// --- check 3: fingerprint stability across a reformat -----------------------

func checkFingerprintStability(c Case) CheckResult {
	cr := CheckResult{Name: "fingerprints_stable"}
	reviews := parsedResponses(c)
	if reviews == nil {
		cr.Pass = false
		cr.Detail = "responses do not parse; fingerprints unverifiable"
		return cr
	}
	var problems []string
	n := 0
	for _, fr := range reviews {
		for i := range fr.Findings {
			f := fr.Findings[i]
			before := fingerprintOfFinding(&f, "")
			f.Evidence = perturbEvidence(f.Evidence)
			f.Title = perturbText(f.Title)
			after := fingerprintOfFinding(&f, "")
			n++
			if before != after {
				problems = append(problems,
					fmt.Sprintf("finding %s fingerprint changed across a reformat", f.ID))
			}
		}
	}
	if len(problems) > 0 {
		cr.Pass = false
		cr.Detail = strings.Join(problems, "; ")
		return cr
	}
	cr.Pass = true
	cr.Detail = fmt.Sprintf("%d fingerprint(s) stable across quote/whitespace/case perturbation", n)
	return cr
}

func perturbEvidence(ev []model.Evidence) []model.Evidence {
	out := make([]model.Evidence, len(ev))
	for i, e := range ev {
		out[i] = model.Evidence{Line: e.Line, Quote: perturbText(e.Quote)}
	}
	return out
}

// perturbText simulates a cosmetic reformat: swap quote characters, inject
// extra whitespace, and re-case words. FingerprintOf normalises all three,
// so identity must survive.
func perturbText(s string) string {
	s = strings.ReplaceAll(s, "\"", "'")
	s = strings.ReplaceAll(s, "'", "\"")
	s = strings.ReplaceAll(s, " ", "    ") // widen whitespace runs
	words := strings.Fields(s)
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:] // Title-Case each word
		}
	}
	return strings.Join(words, "    ")
}

// --- check 4: replay-twice posts zero new threads the second time ----------

func checkReplayZeroNewThreads(c Case, opts RunOptions) CheckResult {
	cr := CheckResult{Name: "replay_zero_new_threads"}
	reviews := parsedResponses(c)
	if reviews == nil {
		cr.Pass = false
		cr.Detail = "responses do not parse; replay unverifiable"
		return cr
	}

	current := validatedSet(reviews)

	now := opts.Now
	if now.IsZero() {
		now = time.Unix(1700000000, 0).UTC() // fixed epoch: deterministic
	}

	// Replay 1: nothing live yet, everything new is posted.
	plan1 := publisher.Reconcile(current, nil, publisher.DismissalLedger{},
		publisher.ReconcileOptions{Repository: "soak/case-" + c.Spec.Name, Now: now})

	// The threads GitHub would have after replay 1.
	live := make([]publisher.LiveThread, 0, len(plan1.CommentsToPost))
	for i, f := range plan1.CommentsToPost {
		live = append(live, publisher.LiveThread{
			ID:          int64(1000 + i),
			Fingerprint: f.FingerprintOf(),
			Path:        f.Path,
		})
	}

	// Replay 2: same findings again; invariant (§10) says zero new threads.
	plan2 := publisher.Reconcile(current, live, publisher.DismissalLedger{},
		publisher.ReconcileOptions{Repository: "soak/case-" + c.Spec.Name, Now: now})

	if len(plan2.CommentsToPost) > 0 {
		cr.Pass = false
		var dups []string
		for _, f := range plan2.CommentsToPost {
			dups = append(dups, fmt.Sprintf("%s/%s", f.Path, f.ID))
		}
		cr.Detail = fmt.Sprintf("replay 2 posted %d new thread(s): %s",
			len(plan2.CommentsToPost), strings.Join(dups, ", "))
		return cr
	}
	cr.Pass = true
	cr.Detail = fmt.Sprintf("replay 1 posted %d thread(s); replay 2 posted 0", len(live))
	return cr
}

// validatedSet converts parsed reviews into the ValidatedFinding set the
// publisher reconciles, computing Blocks in code from verifiable fields and
// assigning occurrence ordinals per (path, fingerprint).
func validatedSet(reviews map[string]*model.FileReview) []model.ValidatedFinding {
	var paths []string
	for p := range reviews {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []model.ValidatedFinding
	occurrences := map[string]int{}
	for _, p := range paths {
		fr := reviews[p]
		for i := range fr.Findings {
			f := fr.Findings[i]
			vf := model.ValidatedFinding{Finding: f, Path: p}
			vf.Blocks = f.Category.MayBlock() && f.Confidence != model.ConfidenceQuestion
			vf.Fingerprint = vf.FingerprintOf()
			key := p + "\x00" + vf.Fingerprint
			occurrences[key]++
			vf.Occurrence = occurrences[key]
			out = append(out, vf)
		}
	}
	return out
}

// fingerprintOfFinding computes a finding's fingerprint at a given path.
func fingerprintOfFinding(f *model.Finding, _ string) string {
	vf := model.ValidatedFinding{Finding: *f}
	return vf.FingerprintOf()
}

// RenderReport renders the plain-text table plus totals. Deterministic:
// rows appear in load order (LoadCases sorts by name).
func RenderReport(r Report) string {
	var sb strings.Builder
	sb.WriteString(HelpText)
	sb.WriteString("\n\n")

	passes := 0
	for _, cr := range r.Results {
		status := "FAIL"
		if cr.Pass() {
			status = "PASS"
			passes++
		}
		fmt.Fprintf(&sb, "%s  %s  (%s)\n", pad(cr.Name, 24), status, cr.Duration.Round(time.Microsecond))
		for _, chk := range cr.Checks {
			mark := "ok  "
			if !chk.Pass {
				mark = "FAIL"
			}
			detail := chk.Detail
			if detail == "" {
				detail = "-"
			}
			fmt.Fprintf(&sb, "  %-22s %s  %s\n", pad(chk.Name, 22), mark, detail)
		}
	}
	fmt.Fprintf(&sb, "\n%d/%d case(s) passed.\n", passes, len(r.Results))
	if !r.Pass() {
		sb.WriteString("RESULT: FAIL\n")
	} else {
		sb.WriteString("RESULT: PASS\n")
	}
	return sb.String()
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
