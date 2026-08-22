package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/scope"
)

// --- fakes -----------------------------------------------------------------

// fakeClient records the full call sequence (triage-then-batched shape must
// be observable) and scripts responses.
type fakeClient struct {
	mu    sync.Mutex
	calls []model.CompletionRequest
	fn    func(i int, req model.CompletionRequest) (string, error)
}

func (c *fakeClient) Complete(ctx context.Context, req model.CompletionRequest) (*model.CompletionResponse, error) {
	c.mu.Lock()
	i := len(c.calls)
	c.calls = append(c.calls, req)
	c.mu.Unlock()
	if c.fn == nil {
		return &model.CompletionResponse{Text: "{}"}, nil
	}
	text, err := c.fn(i, req)
	if err != nil {
		return nil, err
	}
	return &model.CompletionResponse{Text: text}, nil
}

func (c *fakeClient) ModelID() string { return "fake-model" }

var pathRe = regexp.MustCompile(`<file_under_review path="([^"]+)"`)

// requestPath extracts the file under review from a review-call User payload.
func requestPath(req model.CompletionRequest) string {
	if m := pathRe.FindStringSubmatch(req.User); m != nil {
		return m[1]
	}
	return ""
}

func isTriageCall(req model.CompletionRequest) bool { return req.System == triageSystemPrompt }

// fakeVerifier implements Verifier with canned answers; an absent answer is
// false (fail closed).
type fakeVerifier struct {
	paths   map[string]bool
	symbols map[string]bool
}

func (v *fakeVerifier) PathExists(p string) bool   { return v.paths[p] }
func (v *fakeVerifier) SymbolExists(s string) bool { return v.symbols[s] }

// fakeDisc implements DiscriminativeVerifier with one canned answer.
type fakeDisc struct {
	res   string
	err   error
	calls int
}

func (d *fakeDisc) Verify(_ context.Context, _ string, _ model.Finding) (string, error) {
	d.calls++
	return d.res, d.err
}

// --- builders ---------------------------------------------------------------

const baseDiffText = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 context line one
+added line alpha here
 context line two
`

func basePostImage() []byte {
	return []byte("context line one\nadded line alpha here\ncontext line two\n")
}

func baseManifest() []scope.ManifestEntry {
	return []scope.ManifestEntry{{Status: "M", Path: "a.go", Adds: 1, Dels: 0}}
}

func baseInputs() Inputs {
	df, err := scope.ParseUnifiedDiff(baseDiffText)
	if err != nil {
		panic(err)
	}
	return Inputs{
		Manifest:      baseManifest(),
		Diffs:         map[string]*scope.DiffFile{"a.go": df.Files[0]},
		PostImage:     map[string][]byte{"a.go": basePostImage()},
		PRDescription: "Test PR",
		Nonce:         "nonce7f3a91",
	}
}

func mkEvidence(line int, quote string) map[string]any {
	return map[string]any{"line": line, "quote": quote}
}

func mkFinding(id string, category model.Category, start, end int, conf string, ev []map[string]any) map[string]any {
	return map[string]any{
		"id":              id,
		"category":        category,
		"anchor":          map[string]any{"start_line": start, "end_line": end},
		"title":           "Title for " + id,
		"body":            "Body for " + id,
		"impact":          "Impact of " + id,
		"evidence":        ev,
		"external_claims": []any{},
		"introduced_by":   "added_line",
		"confidence":      conf,
		"fix":             nil,
	}
}

func reviewJSON(path, outcome string, findings []map[string]any) string {
	b, err := json.Marshal(map[string]any{
		"schema_version": model.SchemaVersion,
		"path":           path,
		"outcome":        outcome,
		"findings":       findings,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func triageJSON(paths ...string) string {
	type f struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	fl := make([]f, 0, len(paths))
	for _, p := range paths {
		fl = append(fl, f{Path: p, Reason: "logic"})
	}
	b, err := json.Marshal(map[string]any{"schema_version": model.SchemaVersion, "flagged": fl})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// defaultScript serves triage calls with flags and everything else with an
// empty review for the requested path.
func defaultScript(flags ...string) func(int, model.CompletionRequest) (string, error) {
	return func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON(flags...), nil
		}
		return reviewJSON(requestPath(req), "reviewed", nil), nil
	}
}

func runOnce(t *testing.T, in Inputs, o Options) (*model.RunRecord, error) {
	t.Helper()
	return New(o).Run(context.Background(), in)
}

func baseOptions(c model.Client) Options {
	return Options{Cfg: config.Default(), Client: c}
}

func findDrop(rec *model.RunRecord, reason model.DropReason) *model.DropEntry {
	for i := range rec.Drops {
		if rec.Drops[i].Reason == reason {
			return &rec.Drops[i]
		}
	}
	return nil
}

// --- tests ------------------------------------------------------------------

func TestHappyPathExactCascadeBlocks(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 {
		t.Fatalf("Findings = %d (%+v), drops %+v", len(rec.Findings), rec.Findings, rec.Drops)
	}
	vf := rec.Findings[0]
	if !vf.Blocks {
		t.Errorf("Blocks = false, want true (certain + added anchor + no claims + exact evidence)")
	}
	if vf.EvidenceLevel != model.EvidenceExact {
		t.Errorf("EvidenceLevel = %q, want exact", vf.EvidenceLevel)
	}
	if vf.Path != "a.go" {
		t.Errorf("Path = %q", vf.Path)
	}
	if vf.Fingerprint == "" {
		t.Error("Fingerprint empty")
	}
	if rec.Coverage.Reviewed != 1 || !rec.Coverage.Complete {
		t.Errorf("Coverage = %+v", rec.Coverage)
	}
	if rec.Samples != 1 || rec.Model != "fake-model" {
		t.Errorf("Samples=%d Model=%q", rec.Samples, rec.Model)
	}
}

func TestNormalizedQuoteMatchesCRLFAndNBSPTabLines(t *testing.T) {
	in := baseInputs()
	// Line 2 uses NBSP separators and a tab-indented line elsewhere; the
	// quote uses plain spaces.
	in.PostImage["a.go"] = []byte("context\tline one\nsig := read(header)\u00A0\u00A0\ncontext line two\n")
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "sig := read(header)")})
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 1 || rec.Findings[0].EvidenceLevel != model.EvidenceNormalized {
		t.Fatalf("want 1 finding at normalized level, got %+v / drops %+v", rec.Findings, rec.Drops)
	}
}

func TestFabricatedQuoteDroppedAndLogged(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: defaultScript("a.go")}
	c.fn = func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "this line was never in the file")})
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0", len(rec.Findings))
	}
	d := findDrop(rec, model.DropEvidenceMismatch)
	if d == nil {
		t.Fatalf("no DropEvidenceMismatch entry; drops %+v", rec.Drops)
	}
	if d.Path != "a.go" {
		t.Errorf("drop path = %q", d.Path)
	}
}

func TestAmbiguousQuoteWithoutLineHintDropped(t *testing.T) {
	in := baseInputs()
	in.PostImage["a.go"] = []byte("return wrap(err, \"op failed x\")\nadded line alpha here\nreturn wrap(err, \"op failed x\")\n")
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(0, "return wrap(err, \"op failed x\")")})
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0", len(rec.Findings))
	}
	if findDrop(rec, model.DropAmbiguousQuote) == nil {
		t.Fatalf("no DropAmbiguousQuote entry; drops %+v", rec.Drops)
	}
}

func TestAbsenceClaimOnPartialContextDropped(t *testing.T) {
	in := baseInputs()
	var b strings.Builder
	b.WriteString("context line one\nadded line alpha here\n")
	for i := len("x\n"); i < maxFileLines+10; i++ {
		fmt.Fprintf(&b, "padding filler line number %05d\n", i)
	}
	in.PostImage["a.go"] = []byte(b.String())
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryInjection, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		f["evidence_kind"] = "absent"
		f["missing_assertion"] = "there is no validation anywhere"
		return reviewJSON("a.go", "reviewed_partial_context", []map[string]any{f}), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0", len(rec.Findings))
	}
	d := findDrop(rec, model.DropAbsenceOnPartial)
	if d == nil {
		t.Fatalf("no DropAbsenceOnPartial entry; drops %+v", rec.Drops)
	}
}

func TestPathExistsFalseDropsFinding(t *testing.T) {
	in := baseInputs()
	v := &fakeVerifier{paths: map[string]bool{}}
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		f["external_claims"] = []any{map[string]string{"type": "path_exists", "subject": "internal/missing/config.go"}}
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	o := baseOptions(c)
	o.Verifier = v
	rec, err := runOnce(t, in, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0", len(rec.Findings))
	}
	d := findDrop(rec, model.DropClaimUnverified)
	if d == nil {
		t.Fatalf("no DropClaimUnverified entry; drops %+v", rec.Drops)
	}
	if !strings.Contains(d.Detail, "path_exists") {
		t.Errorf("Detail = %q, want mention of path_exists", d.Detail)
	}
}

func TestSymbolExistsMissDropsFinding(t *testing.T) {
	in := baseInputs()
	v := &fakeVerifier{symbols: map[string]bool{}} // zero hits repo-wide → drop
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryCrash, 2, 2, "certain",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		f["external_claims"] = []any{map[string]string{"type": "symbol_exists", "subject": "validateSignature"}}
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	o := baseOptions(c)
	o.Verifier = v
	rec, err := runOnce(t, in, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0", len(rec.Findings))
	}
	if findDrop(rec, model.DropClaimUnverified) == nil {
		t.Fatalf("no DropClaimUnverified entry; drops %+v", rec.Drops)
	}
}

func TestConventionSuppressedWhenNitsOff(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		f := mkFinding("f1", model.CategoryConvention, 2, 2, "question",
			[]map[string]any{mkEvidence(2, "added line alpha here")})
		return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
	}}
	cfg := config.Default() // nits: false by default (§13.4)
	rec, err := runOnce(t, in, Options{Cfg: cfg, Client: c})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.Findings) != 0 {
		t.Fatalf("Findings = %d, want 0 (convention suppressed with nits off)", len(rec.Findings))
	}
	d := findDrop(rec, model.DropSuppressed)
	if d == nil || d.Detail != "nits off" {
		t.Fatalf("drops = %+v, want suppressed/nits off", rec.Drops)
	}
}

func TestBlockingRequiresCertainAddedLineAndSupport(t *testing.T) {
	build := func(conf string, start int, disc *fakeDisc) (*model.RunRecord, *fakeDisc) {
		in := baseInputs()
		quote := "context line one"
		if start == 2 {
			quote = "added line alpha here" // must match the quoted post-image line
		}
		c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
			if isTriageCall(req) {
				return triageJSON("a.go"), nil
			}
			f := mkFinding("f1", model.CategoryCrash, start, start, conf,
				[]map[string]any{mkEvidence(start, quote)})
			// Anchor validity only needs added-or-context lines; blocking
			// additionally needs an ADDED line inside the anchor range.
			return reviewJSON("a.go", "reviewed", []map[string]any{f}), nil
		}}
		o := baseOptions(c)
		o.DiscVerifier = disc
		rec, err := runOnce(t, in, o)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return rec, disc
	}

	// certain + added-line anchor + supported → blocks.
	dOK := &fakeDisc{res: "supported"}
	rec, _ := build("certain", 2, dOK)
	if len(rec.Findings) != 1 || !rec.Findings[0].Blocks {
		t.Fatalf("supported+certain+added anchor should block: %+v drops %+v", rec.Findings, rec.Drops)
	}
	if dOK.calls != 1 {
		t.Errorf("disc verifier calls = %d, want 1", dOK.calls)
	}
	if rec.Findings[0].VerifierResult != "supported" {
		t.Errorf("VerifierResult = %q", rec.Findings[0].VerifierResult)
	}

	// likely confidence → never blocks, verifier not even consulted.
	dNone := &fakeDisc{res: "supported"}
	rec, d := build("likely", 2, dNone)
	if len(rec.Findings) != 1 || rec.Findings[0].Blocks {
		t.Fatalf("likely must not block: %+v", rec.Findings)
	}
	if d.calls != 0 {
		t.Errorf("verifier called %d times for non-candidate", d.calls)
	}

	// anchor on context-only lines → valid note, never blocks.
	rec, _ = build("certain", 1, &fakeDisc{res: "supported"})
	if len(rec.Findings) != 1 || rec.Findings[0].Blocks {
		t.Fatalf("context-only anchor must not block: %+v drops %+v", rec.Findings, rec.Drops)
	}

	// unsupported → dropped with DropVerifierUnsupported.
	rec, _ = build("certain", 2, &fakeDisc{res: "unsupported"})
	if len(rec.Findings) != 0 {
		t.Fatalf("unsupported candidate must be dropped: %+v", rec.Findings)
	}
	if findDrop(rec, model.DropVerifierUnsupported) == nil {
		t.Fatalf("no DropVerifierUnsupported entry; drops %+v", rec.Drops)
	}

	// needs-context-not-provided → note, never blocks.
	rec, _ = build("certain", 2, &fakeDisc{res: "needs-context-not-provided"})
	if len(rec.Findings) != 1 || rec.Findings[0].Blocks {
		t.Fatalf("needs-context must stay a note: %+v", rec.Findings)
	}
}

func TestTriageThenBatchedShapeObservable(t *testing.T) {
	const n = 8
	in := Inputs{PRDescription: "Batch PR", Nonce: "n-once"}
	post := map[string][]byte{}
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("f%d.go", i)
		in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "A", Path: p, Adds: 5})
		post[p] = []byte(fmt.Sprintf("package main // file %d content padding\n", i))
	}
	in.PostImage = post
	flags := []string{"f2.go", "f5.go"}
	c := &fakeClient{fn: defaultScript(flags...)}

	o := baseOptions(c)
	o.Cfg = config.Default()
	o.Cfg.Roles = map[model.Role]config.RoleSpec{
		model.RoleReview: {Concurrency: 1}, // deterministic wave order for observation
	}

	rec, err := New(o).Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.calls) != 1+n {
		t.Fatalf("calls = %d, want %d (1 triage + %d reviews)", len(c.calls), 1+n, n)
	}
	if !isTriageCall(c.calls[0]) {
		t.Fatalf("first call is not the triage pass")
	}
	// The FIRST frontier call is serialized before anyone else starts
	// (§7): it is flagged-first, so it must be f2.go.
	if got := requestPath(c.calls[1]); got != "f2.go" {
		t.Errorf("first review call = %q, want flagged f2.go", got)
	}
	// With concurrency 1 the wave order is observable: flagged files are
	// reviewed before unflagged ones (triage priority), unflagged keep
	// manifest order.
	wantOrder := []string{"f2.go", "f5.go", "f0.go", "f1.go", "f3.go", "f4.go", "f6.go", "f7.go"}
	gotOrder := pathsOf(c.calls[1:])
	for i, w := range wantOrder {
		if gotOrder[i] != w {
			t.Fatalf("review order = %v, want %v", gotOrder, wantOrder)
		}
	}
	// All files reach reviewed — unflagged get the safer-default batched review too.
	for _, fo := range rec.Files {
		if fo.State != model.FileReviewed || !fo.Reviewed {
			t.Errorf("%s state=%s, want reviewed", fo.Path, fo.State)
		}
	}
	// Segment discipline: nonce lives in segment B (every user payload),
	// never in segment A (the system prompt).
	for _, call := range c.calls {
		if strings.Contains(call.System, "n-once") {
			t.Errorf("volatile nonce leaked into segment A (system prompt)")
		}
		if !isTriageCall(call) && !strings.Contains(call.User, "n-once") {
			t.Errorf("nonce missing from segment B of review call")
		}
	}
	// Segment B is byte-identical across all review calls of the run.
	var segB string
	for _, call := range c.calls {
		if isTriageCall(call) {
			continue
		}
		b := strings.SplitN(call.User, cacheBreakpoint, 2)[0]
		if segB == "" {
			segB = b
		} else if b != segB {
			t.Errorf("segment B differs across calls — cache would cold-miss")
		}
	}
}

func pathsOf(calls []model.CompletionRequest) []string {
	var out []string
	for _, c := range calls {
		out = append(out, requestPath(c))
	}
	return out
}

func TestDeterministicFailureNotRetried(t *testing.T) {
	in := baseInputs()
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		return "", fmt.Errorf("%w: output truncated at token cap", model.ErrDeterministic)
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	reviewCalls := 0
	for _, call := range c.calls {
		if !isTriageCall(call) {
			reviewCalls++
		}
	}
	if reviewCalls != 1 {
		t.Errorf("review calls = %d, want exactly 1 (deterministic failures are terminal)", reviewCalls)
	}
	var fo *model.FileOutcome
	for i := range rec.Files {
		if rec.Files[i].Path == "a.go" {
			fo = &rec.Files[i]
		}
	}
	if fo == nil || fo.State != model.FileErrored || fo.Reason != "deterministic_failure" {
		t.Fatalf("file outcome = %+v, want errored(deterministic_failure)", fo)
	}
	if rec.Coverage.Complete {
		t.Errorf("coverage complete despite errored file — gate must fail closed")
	}
}

func TestTransientErrorRetriedOnceThenSucceeds(t *testing.T) {
	in := baseInputs()
	calls := 0
	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON("a.go"), nil
		}
		calls++
		if calls == 1 {
			return "", fmt.Errorf("provider_unavailable: HTTP 503")
		}
		return reviewJSON("a.go", "reviewed", nil), nil
	}}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 2 {
		t.Errorf("review calls = %d, want 2 (one transient retry)", calls)
	}
	for _, fo := range rec.Files {
		if fo.Path == "a.go" && fo.State != model.FileReviewed {
			t.Errorf("a.go state = %s, want reviewed after retry", fo.State)
		}
	}
}

func TestPartialResultsAfterMidRunCancellation(t *testing.T) {
	in := Inputs{PRDescription: "P", Nonce: "nn", PostImage: map[string][]byte{}}
	for _, p := range []string{"f1.go", "f2.go", "f3.go"} {
		in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "A", Path: p, Adds: 3})
		in.PostImage[p] = []byte("package main // padded content here\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &fakeClient{fn: func(_ int, req model.CompletionRequest) (string, error) {
		if isTriageCall(req) {
			return triageJSON(), nil
		}
		switch requestPath(req) {
		case "f2.go":
			<-ctx.Done() // hold this unit open until the run is killed
			return "", ctx.Err()
		default:
			return reviewJSON(requestPath(req), "reviewed", nil), nil
		}
	}}

	done := make(chan struct{})
	var rec *model.RunRecord
	var runErr error
	go func() {
		defer close(done)
		rec, runErr = New(baseOptions(c)).Run(ctx, in) //nolint:vet // test
	}()
	// Wait until the blocked unit is in flight, then kill the run.
	for {
		c.mu.Lock()
		n := len(c.calls)
		c.mu.Unlock()
		if n >= 3 { // triage + serialized f1 + f2 held open
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done

	if runErr == nil {
		t.Fatalf("Run err = nil, want cancellation error")
	}
	states := map[string]model.FileOutcome{}
	for _, fo := range rec.Files {
		states[fo.Path] = fo
	}
	if states["f1.go"].State != model.FileReviewed {
		t.Errorf("f1.go = %+v, want reviewed (partial results survive a killed run)", states["f1.go"])
	}
	if states["f2.go"].State != model.FileErrored {
		t.Errorf("f2.go = %+v, want errored", states["f2.go"])
	}
	if st := states["f3.go"].State; st != model.FileReviewed && st != model.FileErrored {
		t.Errorf("f3.go = %+v, want a terminal state (reviewed or errored, no absence)", states["f3.go"])
	}
	if rec.Coverage.Complete {
		t.Errorf("coverage must be incomplete after cancellation")
	}
}

func TestRiskRankingEngagesAboveFortyFlaggedFiles(t *testing.T) {
	const n = 45
	in := Inputs{PRDescription: "Big PR", Nonce: "rk"}
	post := map[string][]byte{}
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("big%03d.go", i)
		in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "A", Path: p, Adds: 10})
		post[p] = []byte("// padded source line for ranking test file\n")
	}
	in.PostImage = post
	flags := make([]string, n)
	for i := range flags {
		flags[i] = fmt.Sprintf("big%03d.go", i)
	}
	c := &fakeClient{fn: defaultScript(flags...)}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rec.RiskRanked {
		t.Fatal("RiskRanked = false, want true above 40 flagged files")
	}
	if rec.RiskRankedNote == "" || !strings.Contains(rec.RiskRankedNote, "Risk-ranked") {
		t.Errorf("RiskRankedNote = %q, want the §7 coverage-footer line", rec.RiskRankedNote)
	}
	reviewed, cut := 0, 0
	for _, fo := range rec.Files {
		switch {
		case fo.State == model.FileReviewed:
			reviewed++
		case fo.State == model.FileSkipped && fo.Reason == scope.SkipReasonRiskCutoff:
			cut++
		default:
			t.Errorf("%s unexpected terminal state %s(%s)", fo.Path, fo.State, fo.Reason)
		}
	}
	if reviewed != scope.RiskRankCutoff || cut != n-scope.RiskRankCutoff {
		t.Errorf("reviewed=%d cut=%d, want %d/%d", reviewed, cut, scope.RiskRankCutoff, n-scope.RiskRankCutoff)
	}
	// Cut entries are deliberately NOT approved skips: coverage stays
	// incomplete so the scoped review can never look complete (§7).
	if rec.Coverage.Complete {
		t.Errorf("coverage complete despite risk-ranked cuts")
	}
}

func TestDeletedFileReviewedWithoutModelCall(t *testing.T) {
	in := baseInputs()
	in.Manifest = append(in.Manifest, scope.ManifestEntry{Status: "D", Path: "old/removed.go", Adds: 0, Dels: 12})
	c := &fakeClient{fn: defaultScript()}
	rec, err := runOnce(t, in, baseOptions(c))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	n := 0
	for _, fo := range rec.Files {
		if fo.Path == "old/removed.go" {
			n++
			if fo.State != model.FileReviewed || fo.Reviewed != true || fo.Findings != 0 {
				t.Errorf("deleted file outcome = %+v", fo)
			}
		}
	}
	if n != 1 {
		t.Fatalf("deleted file outcomes = %d, want 1", n)
	}
	if !rec.Coverage.Complete {
		t.Errorf("coverage incomplete: %+v", rec.Coverage)
	}
}
