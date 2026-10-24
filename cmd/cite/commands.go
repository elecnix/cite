package main

// Subcommand entry points wiring internal packages together.
//
// review  — local diff mode (--diff FILE) or pull-request mode (--pr owner/repo#N)
// doctor  — which instruction files reached which paths
// validate— schema-check .github/cite.yml
// soak    — pipeline regression harness over bench cases

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/elecnix/cite/internal/config"
)

func init() {
	registerCommand("review", runReview)
	registerCommand("doctor", runDoctor)
	registerCommand("validate", runValidate)
	registerCommand("soak", runSoak)
}

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	effective := fs.Bool("effective", false, "print the merged configuration, one key per line, naming the file each value came from")
	op := newOperatorFlags(fs, groupConfig|groupEndpoint|groupOutput|groupDiagnostics|groupBehaviour)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// A positional argument is a file, the form every existing invocation and
	// the CI check use: `cite validate .github/cite.yml`.
	for _, arg := range fs.Args() {
		if err := op.config.Set(arg); err != nil {
			return err
		}
	}
	st, err := op.resolve(givenFlags(fs))
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				fmt.Printf("error: %s\n", p)
			}
			return fmt.Errorf("%s is not valid (%d problem(s))", strings.Join(loadedNames(ve), ", "), len(ve.Problems))
		}
		return err
	}
	for _, p := range st.Config.Read {
		fmt.Printf("%s: OK\n", p)
	}
	for _, p := range st.Config.Missing {
		fmt.Printf("%s: not found (skipped)\n", p)
	}
	if *effective {
		fmt.Print(effectiveReport(st))
		return nil
	}
	cfg := st.cfg()
	fmt.Printf("merged from %s: OK (gate=%s max_comments=%d nits=%t compat_profile=%s)\n",
		strings.Join(namesOrNone(st.Config.Read), " + "), cfg.Gate, cfg.MaxComments, cfg.Nits, cfg.CompatProfile)
	return nil
}

// loadedNames names the files a validation error came from, so the summary line
// points at what the reader has to open.
func loadedNames(ve *config.ValidationError) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range ve.Problems {
		if p.File != "" && !seen[p.File] {
			seen[p.File] = true
			out = append(out, p.File)
		}
	}
	if len(out) == 0 {
		return []string{"configuration"}
	}
	return out
}

func namesOrNone(files []string) []string {
	if len(files) == 0 {
		return []string{"built-in defaults"}
	}
	return files
}

// effectiveReport renders the merged configuration with its provenance. A value
// that a flag or an environment variable set is named too: --effective answers
// "where did this come from" for every setting, not only the file-borne ones.
func effectiveReport(st *settings) string {
	// A flag or a variable outranks the files, so its value is the one that
	// applies: print it once, in place of the file's, never both.
	type row struct{ key, value, source string }
	overridden := map[string]row{}
	for key, src := range st.Sources {
		if strings.HasPrefix(src, "flag --") || strings.HasPrefix(src, "env:") {
			overridden[key] = row{key, sourceValue(st, key), src}
		}
	}
	var rows []row
	for _, e := range st.Config.Effective() {
		if _, ok := overridden[e.Key]; ok {
			continue
		}
		rows = append(rows, row{e.Key, e.Value, e.Source})
	}
	for _, r := range overridden {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "%-24s %-40s %s\n", r.key, r.value, r.source)
	}
	return sb.String()
}

// sourceValue renders one resolved setting, masking a literal credential the way
// --effective masks the file's.
func sourceValue(st *settings, key string) string {
	switch key {
	case "model_id":
		return st.ModelID
	case "model_base_url":
		return st.ModelBaseURL
	case "model_api_key":
		if strings.HasPrefix(st.ModelAPIKey, "$") || strings.HasPrefix(st.ModelAPIKey, "!") {
			return st.ModelAPIKey
		}
		if st.ModelAPIKey == "" {
			return "(unset)"
		}
		return "[literal, masked]"
	case "fallback_models":
		return "[" + strings.Join(st.fallbackModelIDs(), ", ") + "]"
	case "config":
		return "[" + strings.Join(st.Files, ", ") + "]"
	case "record_out":
		return orUnset(st.RecordOut)
	case "structured_output":
		return st.StructuredOutput
	case "reasoning_effort":
		return orUnset(st.ReasoningEffort)
	case "require_parameters":
		return fmt.Sprintf("%t", st.RequireParams)
	case "capture_wire":
		return fmt.Sprintf("%t", st.CaptureWire)
	case "capture_dir":
		return orUnset(st.CaptureDir)
	case "capture_max_bytes":
		if st.CaptureMaxBytes == 0 {
			return "(unset)"
		}
		return fmt.Sprintf("%d", st.CaptureMaxBytes)
	case "truncated_out":
		return orUnset(st.TruncatedOut)
	case "verify":
		return fmt.Sprintf("%t", st.Verify)
	case "related_code":
		return fmt.Sprintf("%t", st.RelatedCode)
	case "debug":
		return fmt.Sprintf("%t", st.Debug)
	}
	return "(unset)"
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func runSoak(args []string) error {
	fs := flag.NewFlagSet("soak", flag.ContinueOnError)
	dir := "."
	if fs.Parse(args) == nil && fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	rep, pass, err := runSoakDir(dir)
	if err != nil {
		return err
	}
	fmt.Println(soakHelpLine())
	fmt.Print(rep)
	if !pass {
		return fmt.Errorf("soak: failures present")
	}
	return nil
}

func runDoctor(args []string) error {
	root := "."
	paths := args
	if len(paths) > 0 && isDir(paths[0]) {
		root = paths[0]
		paths = paths[1:]
	}
	return doctorTree(root, paths)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// parsePRSpec parses "owner/repo#123".
func parsePRSpec(s string) (owner, repo string, num int, err error) {
	s = strings.TrimPrefix(s, "https://github.com/")
	i := strings.Index(s, "#")
	if i < 0 {
		parts := strings.Split(s, "/")
		if len(parts) == 3 {
			s = parts[0] + "/" + parts[2]
			i = strings.Index(s, "#")
		}
	}
	if i < 0 {
		return "", "", 0, fmt.Errorf("expected owner/repo#N, got %q", s)
	}
	repoPart := s[:i]
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", "", 0, fmt.Errorf("bad PR number in %q", s)
	}
	parts := strings.Split(repoPart, "/")
	if len(parts) != 2 {
		return "", "", 0, fmt.Errorf("expected owner/repo#N, got %q", s)
	}
	return parts[0], parts[1], n, nil
}
