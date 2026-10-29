// Layered configuration loading: one schema, an ordered list of files, and
// the provenance that makes the layering visible.
//
// The schema is the one Cite already has. A machine-level file and the
// repository file are the same kind of file; a later file overrides an earlier
// one. Merge rules, so "later wins" is never ambiguous:
//
//   - scalars: the later value replaces the earlier one;
//   - mappings: merged per entry and per field within an entry, so a later file
//     that declares one provider does not erase another;
//   - sequences: replaced wholesale, because a file that cannot shorten a list
//     cannot undo an earlier one.
//
// Validation runs twice by necessity: each file is checked against the schema
// on its own (unknown keys, duplicates and type errors name the file and the
// line), and the merged result is validated once more, because a combination
// can be invalid while each part is fine — a fallback entry whose provider only
// the other file declares, for instance.
package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

// Override is one verdict-affecting key changed by a file after the first one.
// The change is applied, not refused: the file is the operator's own. It is
// recorded because a local run that judges a diff differently from CI must say
// so out loud.
type Override struct {
	Key    string // dotted key path
	File   string // the file that changed it
	Before string // the value it replaced, as rendered
	After  string // the value it sets, as rendered
}

func (o Override) String() string {
	return fmt.Sprintf("%s: %s -> %s (from %s; this run judges the diff differently from a run that does not read that file)",
		o.Key, o.Before, o.After, o.File)
}

// Loaded is the merged configuration plus the provenance behind it.
type Loaded struct {
	*Config

	// Read lists the files that existed and were read, in the order given.
	Read []string
	// Missing lists the files that were not on disk, in the order given.
	Missing []string
	// Sources maps a dotted key path (the same path a Problem reports) to the
	// file the value came from.
	Sources map[string]string
	// Overrides records every verdict-affecting key a file after the first one
	// changed.
	Overrides []Override

	tree map[string]interface{}
	// where remembers, per dotted path, which file and line last wrote it, so
	// a problem found on the merged tree still names the file to fix.
	where map[string]source
}

type source struct {
	file string
	line int
}

// LoadFiles reads the configuration from the ordered list of files. A file
// that is not on disk is skipped: a missing or empty file contributes nothing,
// which is the documented "no configuration" case. Every file that does exist
// is parsed strictly, and the merged result is validated once more.
func LoadFiles(paths []string) (*Loaded, error) {
	out := &Loaded{Sources: map[string]string{}, where: map[string]source{}}
	var probs []Problem
	var tree map[string]interface{}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				out.Missing = append(out.Missing, path)
				continue
			}
			return nil, err
		}
		out.Read = append(out.Read, path)
		fileTree, lines, err := parseYAMLLines(data)
		if err != nil {
			if se, ok := err.(*SyntaxError); ok && se.File == "" {
				se.File = path
			}
			return nil, err
		}
		if fileTree == nil {
			continue // empty file, or only comments
		}
		m, ok := fileTree.(map[string]interface{})
		if !ok {
			probs = append(probs, Problem{File: path, Line: lines[""],
				Message: fmt.Sprintf("top level of the configuration must be a mapping, got %s", describe(fileTree))})
			continue
		}
		var fileProbs []Problem
		schemaCheckFile(fileTree, &fileProbs)
		locator{file: path, lines: lines}.attach(fileProbs)
		probs = append(probs, fileProbs...)

		// A file after the first one may change a key that decides the
		// verdict. Say so once per key, before merging it in.
		if len(out.Read) > 1 {
			out.Overrides = append(out.Overrides, verdictOverrides(tree, m, path)...)
		}
		for _, leaf := range leafPaths(m, "") {
			out.Sources[leaf] = path
			out.where[leaf] = source{file: path, line: lines[leaf]}
		}
		tree = mergeTrees(tree, m)
	}

	if len(probs) > 0 {
		return nil, &ValidationError{Problems: probs}
	}

	cfg := Default()
	if tree != nil {
		var post []Problem
		cfg = buildConfig(tree, &post)
		cfg.validate(&post)
		out.attachSources(post)
		probs = append(probs, post...)
	}
	if len(probs) > 0 {
		return nil, &ValidationError{Problems: probs}
	}
	out.Config = cfg
	out.tree = tree
	return out, nil
}

// attachSources names the file (and line) behind each problem found on the
// merged tree. The path a problem reports is the key path, which is exactly the
// key the merge recorded a source for; a path the schema invented (a missing
// required field, say) falls back to the nearest enclosing key that does exist.
func (l *Loaded) attachSources(probs []Problem) {
	for i := range probs {
		src, ok := l.lookupSource(probs[i].Path)
		if !ok {
			if len(l.Read) == 1 {
				src, ok = source{file: l.Read[0]}, true
			}
		}
		if !ok {
			continue
		}
		probs[i].File = src.file
		probs[i].Line = src.line
	}
}

func (l *Loaded) lookupSource(path string) (source, bool) {
	p := path
	for {
		if s, ok := l.where[p]; ok {
			return s, true
		}
		if p == "" {
			return source{}, false
		}
		if i := strings.LastIndexAny(p, ".["); i > 0 {
			p = p[:i]
			continue
		}
		p = ""
	}
}

// locator names the file and line behind problems found while checking one
// file on its own.
type locator struct {
	file  string
	lines map[string]int
}

func (l locator) attach(probs []Problem) {
	for i := range probs {
		if probs[i].File != "" {
			continue
		}
		probs[i].File = l.file
		probs[i].Line = l.line(probs[i].Path)
	}
}

// line returns the line a dotted path starts on, falling back to the nearest
// enclosing key that the parser recorded.
func (l locator) line(path string) int {
	p := path
	for {
		if n, ok := l.lines[p]; ok {
			return n
		}
		if p == "" {
			return 0
		}
		if i := strings.LastIndexAny(p, ".["); i > 0 {
			p = p[:i]
			continue
		}
		p = ""
	}
}

// mergeTrees merges src into dst: mappings per entry and per field, scalars and
// sequences replaced. dst is never modified in place.
func mergeTrees(dst, src map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if sm, ok := v.(map[string]interface{}); ok {
			if dm, ok := out[k].(map[string]interface{}); ok {
				out[k] = mergeTrees(dm, sm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// leafPaths returns the dotted path of every value that is not a mapping — the
// scalars and the sequences, which are the units a later file replaces.
func leafPaths(m map[string]interface{}, prefix string) []string {
	var out []string
	for _, k := range sortedKeys(m) {
		p := joinPath(prefix, k)
		if sub, ok := m[k].(map[string]interface{}); ok {
			out = append(out, leafPaths(sub, p)...)
			continue
		}
		out = append(out, p)
	}
	return out
}

// verdictRoots are the keys that decide what a run concludes. A change to one
// of them by a file other than the first is what a silent local-versus-CI
// difference looks like.
var verdictRoots = []string{
	"blocking_categories",
	"compat_profile",
	"gate",
	"max_comments",
	"nits",
	"paths_ignore",
	"roles",
}

// verdictOverrides reports every verdict key in next that differs from what the
// files merged so far say, or from the built-in default where they say
// nothing.
func verdictOverrides(merged, next map[string]interface{}, file string) []Override {
	var out []Override
	for _, root := range verdictRoots {
		nv, ok := next[root]
		if !ok {
			continue
		}
		before := merged[root]
		for _, p := range leafPaths(map[string]interface{}{root: nv}, "") {
			sub := strings.TrimPrefix(p, root)
			sub = strings.TrimPrefix(sub, ".")
			nleaf := valueAt(nv, sub)
			bleaf := valueAt(before, sub)
			// Where the earlier files say nothing, the value the key replaces is
			// the built-in default, not "nothing". Restating a default is not a
			// change, and a line claiming the run "judges the diff differently"
			// because a file spelled out a default would be a false alarm on
			// every run that does it.
			b := defaultRender(p)
			if bleaf != nil {
				b = renderValue(bleaf)
			}
			after := renderValue(nleaf)
			if b == after {
				continue
			}
			out = append(out, Override{Key: p, File: file, Before: b, After: after})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// valueAt walks sub ("a.b[0].c") into v.
func valueAt(v interface{}, sub string) interface{} {
	if sub == "" {
		return v
	}
	cur := v
	for sub != "" {
		key := sub
		rest := ""
		if i := strings.IndexByte(sub, '.'); i >= 0 {
			key, rest = sub[:i], sub[i+1:]
		}
		idx := -1
		if i := strings.IndexByte(key, '['); i >= 0 {
			if _, perr := fmt.Sscanf(key[i:], "[%d]", &idx); perr == nil {
				key = key[:i]
			}
		}
		m, ok := cur.(map[string]interface{})
		if key != "" {
			if !ok {
				return nil
			}
			cur, ok = m[key]
			if !ok {
				return nil
			}
		}
		if idx >= 0 {
			arr, ok := cur.([]interface{})
			if !ok || idx >= len(arr) {
				return nil
			}
			cur = arr[idx]
		}
		sub = rest
	}
	return cur
}

func sameValue(a, b interface{}) bool { return reflect.DeepEqual(a, b) }

// renderValue renders one configuration value for a message or for
// --effective.
func renderValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "(unset)"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		if t == "" {
			return "(unset)"
		}
		return t
	case int:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%v", t)
	case []interface{}:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = renderValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return describe(v)
}

// builtinDefaults renders the built-in value of a key a file may or may not
// set, so an override can state what it replaced.
var builtinDefaults = map[string]string{
	"max_comments":        fmt.Sprintf("%d", DefaultMaxComments),
	"paths_ignore":        "[]",
	"nits":                "false",
	"gate":                "(unset)",
	"compat_profile":      DefaultCompatProfile,
	"require_parameters":  "false",
	"pr_context":          "comment",
	"blocking_categories": renderCategories(DefaultBlockingCategories()),
	"verify":              "true",
	"related_code":        "true",
	"debug":               "false",
	"capture_wire":        "false",
	"capture_dir":         "(unset)",
	"capture_max_bytes":   "(unset)",
	"truncated_out":       "(unset)",
	"record_out":          "(unset)",
	"structured_output":   "response_format",
	"reasoning_effort":    "(unset)",
	"model_id":            "(unset)",
	"model_base_url":      "(unset)",
	"model_api_key":       "(unset)",
	"model":               DefaultModel,
	"fallback":            "[]",
	"fallback_model_ids":  "[]",
}

func renderCategories(cats []model.Category) string {
	parts := make([]string, len(cats))
	for i, c := range cats {
		parts[i] = string(c)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func defaultRender(path string) string {
	if v, ok := builtinDefaults[path]; ok {
		return v
	}
	return "(unset)"
}

// EffectiveEntry is one key of the merged configuration, its value, and where
// the value came from.
type EffectiveEntry struct {
	Key    string
	Value  string
	Source string // a file path, or BuiltinSource
}

// BuiltinSource is the source of a value no file set.
const BuiltinSource = "built-in default"

// Effective renders every value the merged configuration carries, one key per
// line, each naming the file it came from. It is the answer to the one cost of
// layering: a value can come from a file nobody remembers writing.
//
// A credential is never printed: an api_key shows its reference or says it holds
// a literal, and a header value is masked, because --effective output is pasted
// into issues.
func (l *Loaded) Effective() []EffectiveEntry {
	var out []EffectiveEntry
	seen := map[string]bool{}
	var walk func(m map[string]interface{}, prefix string)
	walk = func(m map[string]interface{}, prefix string) {
		for _, k := range sortedKeys(m) {
			p := joinPath(prefix, k)
			if sub, ok := m[k].(map[string]interface{}); ok {
				walk(sub, p)
				continue
			}
			seen[p] = true
			out = append(out, EffectiveEntry{Key: p, Value: effectiveValue(p, m[k]), Source: l.sourceOf(p)})
		}
	}
	if l.tree != nil {
		walk(l.tree, "")
	}
	// Keys no file set still have a value, and --effective must name its
	// source like any other: "not from a file" is an answer.
	var defaults []string
	for k := range builtinDefaults {
		if !seen[k] {
			defaults = append(defaults, k)
		}
	}
	sort.Strings(defaults)
	for _, k := range defaults {
		out = append(out, EffectiveEntry{Key: k, Value: builtinDefaults[k], Source: BuiltinSource})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (l *Loaded) sourceOf(path string) string {
	if f, ok := l.Sources[path]; ok {
		return f
	}
	if s, ok := l.lookupSource(path); ok && s.file != "" {
		return s.file
	}
	return BuiltinSource
}

// effectiveValue renders a value for --effective, masking credentials.
func effectiveValue(path string, v interface{}) string {
	switch {
	case strings.HasSuffix(path, ".api_key") || path == "model_api_key":
		s := renderValue(v)
		if strings.HasPrefix(s, "$") || strings.HasPrefix(s, "!") {
			return s // a reference names where the secret lives, not the secret
		}
		return "[literal, masked]"
	case strings.Contains(path, ".headers."):
		return "[masked]"
	}
	return renderValue(v)
}
