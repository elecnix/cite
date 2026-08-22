package instructions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// HashContent is the cache key for applicability triage: the sha256 of the
// file content (§5, "Applicability triage, cached by file hash").
func HashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ResolvedSection is one section that entered the review for a changed
// file, in source-precedence order.
type ResolvedSection struct {
	SourceFile string
	Heading    string
	Text       string
	Kind       Kind
}

type candidate struct {
	spec        sourceSpec
	matchedGlob string // scopeGlobs: the most specific glob that matched
}

type sourceResult struct {
	spec     sourceSpec
	matched  string
	sections []ClassifiedSection
}

// ResolvedInstructions answers, per changed file, which instruction
// sections apply and in what order — and reports usage so the behaviour is
// inspectable rather than silent (PLAN.md §5).
type ResolvedInstructions struct {
	paths    []string
	byPath   map[string][]ResolvedSection
	sources  map[string][]sourceResult
	usage    []model.InstructionUsage
	warnings []Warning
}

// For returns the ordered applicable sections for one changed file. Only
// reviewable sections enter the review; authoring and ignore sections are
// reported through Usage instead.
func (r *ResolvedInstructions) For(p string) []ResolvedSection {
	out := r.byPath[p]
	cp := make([]ResolvedSection, len(out))
	copy(cp, out)
	return cp
}

// Usage reports, per source file that applied to at least one changed path:
// total sections, sections that survived triage, and authoring sections.
func (r *ResolvedInstructions) Usage() []model.InstructionUsage {
	out := make([]model.InstructionUsage, len(r.usage))
	copy(out, r.usage)
	return out
}

// Warnings returns the disclosed observations gathered during resolution,
// including truncation disclosures.
func (r *ResolvedInstructions) Warnings() []Warning {
	out := make([]Warning, len(r.warnings))
	copy(out, r.warnings)
	return out
}

// Paths returns the changed paths resolution covered, sorted.
func (r *ResolvedInstructions) Paths() []string {
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

// Resolve discovers instruction sources from tree (the BASE ref), matches
// them against the changed paths, classifies every section via cls (cached
// by content hash), and produces the per-path section lists plus usage.
// A nil classifier means NoopClassifier.
func Resolve(tree Tree, changed []string, cls Classifier) (*ResolvedInstructions, []Warning, error) {
	if cls == nil {
		cls = NoopClassifier{}
	}
	specs, warns, err := discover(tree)
	if err != nil {
		return nil, warns, err
	}

	dedup := make([]string, 0, len(changed))
	seen := map[string]bool{}
	for _, p := range changed {
		p = strings.TrimPrefix(path.Clean("/"+p), "/")
		if !seen[p] {
			seen[p] = true
			dedup = append(dedup, p)
		}
	}
	sort.Strings(dedup)

	cache := make(map[string][]ClassifiedSection) // sha256 -> classification
	classify := func(sp sourceSpec) ([]ClassifiedSection, error) {
		if cs, ok := cache[sp.hash]; ok {
			return cs, nil
		}
		secs := splitSections(sp.content)
		in := make([]Section, len(secs))
		for i, s := range secs {
			in[i] = s.Section
		}
		cs, err := cls.Classify(sp.path, in)
		if err != nil {
			return nil, fmt.Errorf("classify %s: %w", sp.path, err)
		}
		if len(cs) != len(secs) {
			return nil, fmt.Errorf("classify %s: classifier returned %d verdicts for %d sections", sp.path, len(cs), len(secs))
		}
		for i := range cs {
			if secs[i].forced { // `## Review` wins wholesale, skips triage
				cs[i].Kind = KindReviewable
			}
		}
		cache[sp.hash] = cs
		return cs, nil
	}

	ri := &ResolvedInstructions{
		paths:    dedup,
		byPath:   make(map[string][]ResolvedSection, len(dedup)),
		sources:  make(map[string][]sourceResult, len(dedup)),
		warnings: warns,
	}
	type tally struct {
		total     int
		used      map[int]bool
		authoring map[int]bool
	}
	usageByFile := map[string]*tally{}

	for _, p := range dedup {
		cands := matchSpecs(specs, p)
		var rs []ResolvedSection
		var results []sourceResult
		for _, c := range cands {
			cs, err := classify(c.spec)
			if err != nil {
				return nil, ri.warnings, err
			}
			results = append(results, sourceResult{spec: c.spec, matched: c.matchedGlob, sections: cs})
			t := usageByFile[c.spec.path]
			if t == nil {
				t = &tally{used: map[int]bool{}, authoring: map[int]bool{}}
				usageByFile[c.spec.path] = t
			}
			t.total = len(cs)
			for i, c2 := range cs {
				switch c2.Kind {
				case KindReviewable:
					t.used[i] = true
					rs = append(rs, ResolvedSection{
						SourceFile: c.spec.path, Heading: c2.Heading, Text: c2.Text, Kind: KindReviewable,
					})
				case KindAuthoring:
					t.authoring[i] = true
				}
			}
		}
		ri.sources[p] = results
		ri.byPath[p] = rs
	}

	files := make([]string, 0, len(usageByFile))
	for f := range usageByFile {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		t := usageByFile[f]
		ri.usage = append(ri.usage, model.InstructionUsage{
			File:              f,
			TotalSections:     t.total,
			UsedSections:      len(t.used),
			AuthoringSections: len(t.authoring),
		})
	}
	return ri, ri.warnings, nil
}

// matchSpecs picks, for one changed path, the applicable specs in
// precedence order. applyTo matches THE CHANGED FILE, not the tree (§5).
// Ordering between two *.instructions.md whose applyTo both match: most
// specific glob first (fewest wildcards, then longest pattern), then
// lexical path — documented best-effort (§5).
func matchSpecs(specs []sourceSpec, p string) []candidate {
	dir := path.Dir(p)
	if dir == "." {
		dir = ""
	}

	var nearest *sourceSpec
	var cands []candidate
	for i := range specs {
		sp := &specs[i]
		switch sp.scope {
		case scopeRepo:
			cands = append(cands, candidate{spec: *sp})
		case scopeSubtree:
			if sp.dir == "" || dir == sp.dir || strings.HasPrefix(dir, sp.dir+"/") {
				if nearest == nil || len(sp.dir) > len(nearest.dir) {
					nearest = sp
				}
			}
		case scopeGlobs:
			best := ""
			for _, g := range sp.globs {
				if !Match(g, p) {
					continue
				}
				if best == "" || moreSpecific(g, best) {
					best = g
				}
			}
			if best != "" {
				cands = append(cands, candidate{spec: *sp, matchedGlob: best})
			}
		}
	}
	if nearest != nil {
		cands = append(cands, candidate{spec: *nearest})
	}

	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.spec.rank != b.spec.rank {
			return a.spec.rank < b.spec.rank
		}
		ka, kb := orderKey(a), orderKey(b)
		if ka.wild != kb.wild {
			return ka.wild < kb.wild
		}
		if ka.length != kb.length {
			return ka.length > kb.length // longer pattern = more specific
		}
		return a.spec.path < b.spec.path
	})
	return cands
}

type orderKeyT struct{ wild, length int }

func orderKey(c candidate) orderKeyT {
	if c.spec.scope == scopeGlobs && c.matchedGlob != "" {
		return orderKeyT{wildcards(c.matchedGlob), len(c.matchedGlob)}
	}
	return orderKeyT{-1, -1} // repo-wide / subtree candidates never compete across scopes
}

// moreSpecific reports whether a should sort before b: fewer wildcards,
// then longer pattern (lexical tiebreak happens on the path in the caller).
func moreSpecific(a, b string) bool {
	wa, wb := wildcards(a), wildcards(b)
	if wa != wb {
		return wa < wb
	}
	return len(a) > len(b)
}
