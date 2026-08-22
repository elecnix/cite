package instructions

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// The rank table from PLAN.md §5, verbatim paths (the compatibility
// contract). Ranks order sources for a given changed file.
const (
	rankInstructionsMD = 1 // .github/instructions/**/*.instructions.md
	rankCopilot        = 2 // .github/copilot-instructions.md
	rankAgents         = 3 // AGENTS.md nested, nearest file wins
	rankMemory         = 4 // CLAUDE.md, .claude/CLAUDE.md, GEMINI.md, REVIEW.md
	rankRules          = 5 // .claude/rules/*.md with frontmatter paths:
	rankSkills         = 6 // .github/skills/, .claude/skills/, .agents/skills/
	rankSettings       = 7 // .vscode/settings.json reviewSelection.instructions
)

// truncationLimit is the cap another tool applied to instruction files until
// mid-2026. Cite reads whole files but discloses when a file exceeds it,
// noting what that other tool would have seen (§5 divergence 2).
const truncationLimit = 4000

type scopeKind int

const (
	scopeRepo    scopeKind = iota // repository-wide
	scopeSubtree                  // AGENTS.md nearest-file-wins subtree
	scopeGlobs                    // applies to changed files matching globs
)

type sourceKind int

const (
	sourceFile    sourceKind = iota // regular markdown file, split into sections
	sourceVirtual                   // synthesised content (settings.json entries)
)

// sourceSpec is one discovered instruction source with its content loaded.
type sourceSpec struct {
	path    string
	rank    int
	scope   scopeKind
	dir     string   // scopeSubtree: directory the AGENTS.md roots
	globs   []string // scopeGlobs: applyTo / paths globs
	reason  string   // human-readable match explanation for `cite doctor`
	content []byte
	hash    string
	size    int
	kind    sourceKind
}

// discover walks the tree and collects every applicable source, honouring
// chat.instructionsFilesLocations boolean-false disabling from
// .vscode/settings.json (§5: "Cite honours the boolean rather than just
// reading the keys").
func discover(tree Tree) ([]sourceSpec, []Warning, error) {
	all, err := tree.List("")
	if err != nil {
		return nil, nil, fmt.Errorf("list tree: %w", err)
	}
	exists := make(map[string]bool, len(all))
	for _, p := range all {
		exists[p] = true
	}

	settings, warns := loadSettings(tree)

	var specs []sourceSpec

	read := func(p string) ([]byte, bool) {
		b, ok, err := tree.Read(p)
		if err != nil || !ok {
			warns = append(warns, Warning{File: p, Message: readErrMessage(p, ok, err)})
			return nil, false
		}
		if len(b) > truncationLimit {
			warns = append(warns, truncationWarning(p, len(b)))
		}
		return b, true
	}
	add := func(sp *sourceSpec) {
		specs = append(specs, *sp)
	}

	// Rank 1: .github/instructions/**/*.instructions.md
	for _, p := range all {
		if !strings.HasPrefix(p, ".github/instructions/") || !strings.HasSuffix(p, ".instructions.md") {
			continue
		}
		b, ok := read(p)
		if !ok {
			continue
		}
		fm := parseFrontmatter(b)
		if isExcludedAgent(fm.ExcludeAgent) {
			continue // excludeAgent: code-review means "not for me" (§5)
		}
		add(&sourceSpec{
			path: p, rank: rankInstructionsMD, scope: scopeGlobs, globs: fm.ApplyTo,
			reason:  "applyTo globs",
			content: b, hash: HashContent(b), size: len(b),
		})
	}

	// Rank 2: .github/copilot-instructions.md — repository-wide.
	if exists[".github/copilot-instructions.md"] {
		if b, ok := read(".github/copilot-instructions.md"); ok {
			add(&sourceSpec{
				path: ".github/copilot-instructions.md", rank: rankCopilot, scope: scopeRepo,
				reason: "repository-wide", content: b, hash: HashContent(b), size: len(b),
			})
		}
	}

	// Rank 3: AGENTS.md nested anywhere; nearest file wins for a path
	// subtree. The root file is repository-wide under the same rule;
	// .github/AGENTS.md applies only under .github/.
	for _, p := range all {
		if path.Base(p) != "AGENTS.md" {
			continue
		}
		b, ok := read(p)
		if !ok {
			continue
		}
		dir := path.Dir(p)
		if dir == "." {
			dir = ""
		}
		reason := fmt.Sprintf("nearest AGENTS.md for %s/", dir)
		if dir == "" {
			reason = "root AGENTS.md, repository-wide fallback (nearest-file rule)"
		}
		add(&sourceSpec{
			path: p, rank: rankAgents, scope: scopeSubtree, dir: dir, reason: reason,
			content: b, hash: HashContent(b), size: len(b),
		})
	}

	// Rank 4: CLAUDE.md, .claude/CLAUDE.md, GEMINI.md, REVIEW.md — verbatim,
	// repository-wide.
	for _, p := range []string{"CLAUDE.md", ".claude/CLAUDE.md", "GEMINI.md", "REVIEW.md"} {
		if !exists[p] {
			continue
		}
		b, ok := read(p)
		if !ok {
			continue
		}
		add(&sourceSpec{
			path: p, rank: rankMemory, scope: scopeRepo, reason: "repository-wide",
			content: b, hash: HashContent(b), size: len(b),
		})
	}

	// Rank 5: .claude/rules/*.md with frontmatter `paths:` globs. A rules
	// file without a usable paths value is best-effort treated as
	// repository-wide and says so.
	for _, p := range all {
		if !strings.HasPrefix(p, ".claude/rules/") || !strings.HasSuffix(p, ".md") ||
			path.Dir(p) != ".claude/rules" {
			continue
		}
		b, ok := read(p)
		if !ok {
			continue
		}
		fm := parseFrontmatter(b)
		scope, reason := scopeGlobs, fmt.Sprintf("paths `%s`", strings.Join(fm.Paths, ", "))
		if len(fm.Paths) == 0 {
			scope, reason = scopeRepo, "repository-wide (no frontmatter paths: declared)"
		}
		add(&sourceSpec{
			path: p, rank: rankRules, scope: scope, globs: fm.Paths, reason: reason,
			content: b, hash: HashContent(b), size: len(b),
		})
	}

	// Rank 6: skills, selected by description. Heuristic, disclosed: Cite
	// includes a skill when its frontmatter description mentions "review"
	// (case-insensitive — covers "code review", "PR review"); anything else
	// is treated as not review-relevant guidance.
	for _, p := range all {
		parts := strings.Split(p, "/")
		if len(parts) != 4 || parts[3] != "SKILL.md" || parts[1] != "skills" || parts[2] == "" {
			continue
		}
		switch parts[0] {
		case ".github", ".claude", ".agents":
		default:
			continue
		}
		b, ok := read(p)
		if !ok {
			continue
		}
		fm := parseFrontmatter(b)
		if !strings.Contains(strings.ToLower(fm.Description), "review") {
			continue
		}
		add(&sourceSpec{
			path: p, rank: rankSkills, scope: scopeRepo,
			reason:  fmt.Sprintf("skill selected by description (%q mentions review)", fm.Description),
			content: b, hash: HashContent(b), size: len(b),
		})
	}

	// Rank 7: .vscode/settings.json review-selection instructions.
	if settings.found && len(settings.entries) > 0 && !settings.disabledPath(".vscode/settings.json") {
		virt, entryWarns := settings.virtualContent(tree)
		warns = append(warns, entryWarns...)
		if virt != "" {
			add(&sourceSpec{
				path: ".vscode/settings.json", rank: rankSettings, scope: scopeRepo,
				reason: "github.copilot.chat.reviewSelection.instructions", kind: sourceVirtual,
				content: []byte(virt), hash: HashContent([]byte(virt)), size: len(virt),
			})
		}
	}

	// chat.instructionsFilesLocations: false disables a location. A disabled
	// path removes any source at or beneath it (best-effort prefix rule).
	if len(settings.disabled) > 0 {
		kept := specs[:0]
		for _, sp := range specs {
			disabled := false
			for _, d := range settings.disabled {
				if sp.path == d || strings.HasPrefix(sp.path, d+"/") {
					disabled = true
					break
				}
			}
			if !disabled {
				kept = append(kept, sp)
			}
		}
		specs = kept
	}

	sort.SliceStable(specs, func(i, j int) bool { return specs[i].path < specs[j].path })
	return specs, warns, nil
}

func readErrMessage(p string, ok bool, err error) string {
	if err != nil {
		return fmt.Sprintf("unreadable (%v); skipped", err)
	}
	if !ok {
		return "listed but missing when read; skipped"
	}
	return "unreadable; skipped"
}

func truncationWarning(p string, size int) Warning {
	return Warning{
		File:    p,
		Message: fmt.Sprintf("%s is %d characters; Cite reads the whole file, but another tool capped at %d would have seen only the first %d characters.", p, size, truncationLimit, truncationLimit),
	}
}

// settingsFile is the parsed subset of .vscode/settings.json Cite honours.
type settingsFile struct {
	found    bool
	entries  []reviewEntry
	disabled []string
}

// disabledPath reports whether the location was switched off with a
// boolean-false in chat.instructionsFilesLocations.
func (s *settingsFile) disabledPath(p string) bool {
	for _, d := range s.disabled {
		if d == p {
			return true
		}
	}
	return false
}

type reviewEntry struct {
	heading string
	body    string
	file    string
	index   int
}

// loadSettings parses the two keys Cite understands. A malformed file is a
// warning, not an error: the rest of resolution proceeds without it.
func loadSettings(tree Tree) (*settingsFile, []Warning) {
	sf := &settingsFile{}
	raw, ok, err := tree.Read(".vscode/settings.json")
	if err != nil || !ok {
		if err != nil {
			return sf, []Warning{{File: ".vscode/settings.json", Message: fmt.Sprintf("unreadable (%v); skipped", err)}}
		}
		return sf, nil
	}
	sf.found = true
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return sf, []Warning{{File: ".vscode/settings.json", Message: fmt.Sprintf("malformed JSON (%v); skipped", err)}}
	}

	const reviewKey = "github.copilot.chat.reviewSelection.instructions"
	if v, ok := top[reviewKey]; ok {
		var arr []json.RawMessage
		if err := json.Unmarshal(v, &arr); err != nil {
			var single json.RawMessage
			if err2 := json.Unmarshal(v, &single); err2 == nil {
				arr = []json.RawMessage{single}
			}
		}
		for i, e := range arr {
			var obj struct {
				Text        string `json:"text"`
				File        string `json:"file"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(e, &obj); err != nil {
				continue
			}
			heading := obj.Description
			if heading == "" {
				heading = fmt.Sprintf("%s[%d]", reviewKey, i)
			}
			sf.entries = append(sf.entries, reviewEntry{heading: heading, body: obj.Text, file: obj.File, index: i})
		}
	}

	if v, ok := top["chat.instructionsFilesLocations"]; ok {
		var m map[string]bool
		if err := json.Unmarshal(v, &m); err == nil {
			for loc, enabled := range m {
				if !enabled {
					sf.disabled = append(sf.disabled, loc)
				}
			}
		}
	}
	sort.Strings(sf.disabled)
	return sf, nil
}

// virtualContent renders entries as markdown sections so the normal
// splitting pipeline applies. {text} / {file} entries both become sections:
// a text entry contributes its literal guidance, a file entry the referenced
// repository file's content.
func (s *settingsFile) virtualContent(tree Tree) (string, []Warning) {
	var b strings.Builder
	var warns []Warning
	for _, e := range s.entries {
		body := e.body
		if e.file != "" {
			raw, ok, rerr := tree.Read(e.file)
			if rerr != nil || !ok {
				warns = append(warns, Warning{
					File:    ".vscode/settings.json",
					Message: fmt.Sprintf("reviewSelection.instructions[%d] references missing file %s; skipped", e.index, e.file),
				})
				continue
			}
			body = string(raw)
			if len(raw) > truncationLimit {
				warns = append(warns, truncationWarning(e.file, len(raw)))
			}
		}
		if body == "" {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", e.heading, body)
	}
	return b.String(), warns
}
