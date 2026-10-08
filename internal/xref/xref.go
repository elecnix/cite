// Package xref finds the code outside the file under review that its changed
// lines depend on: the definitions of what they call, and the call sites of
// what they change.
//
// The review call sees one file. A claim about a function defined elsewhere
// was, before this package, a guess the prompt could only ask the model to
// declare: "if ParseMode does not accept the empty string, every run fails"
// was posted on a pull request whose ParseMode maps "" to the default two
// files away. Handing the model those few lines turns the guess into a read.
//
// It is deliberately not a language server. Definitions are found by
// per-language line patterns over a read-only snapshot of the repository at
// the pull request head, snippets are cut at the end of the enclosing block,
// and everything is bounded: identifiers per file, snippets per identifier,
// lines per snippet, lines per file. A name too common to resolve (more
// definitions than maxDefinitions) is skipped rather than guessed at.
package xref

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Snapshot is a read-only view of the repository at the pull request head.
type Snapshot interface {
	// Files lists every path in the snapshot, sorted.
	Files() []string
	// Read returns the content of path; ok is false when it is absent.
	Read(path string) (data []byte, ok bool)
}

// Kind says why a snippet was included.
type Kind string

const (
	// KindDefinition is the definition of a name the changed lines use.
	KindDefinition Kind = "definition"
	// KindCaller is a call site of a function the changed lines define.
	KindCaller Kind = "caller"
)

// Snippet is a contiguous excerpt of one file.
type Snippet struct {
	Path      string
	Symbol    string
	Kind      Kind
	StartLine int // 1-based line number of Lines[0]
	Lines     []string
}

// Limits bounds what Related returns. The zero value means the defaults.
type Limits struct {
	MaxSymbols        int // distinct names looked up per file
	MaxDefinitions    int // a name with more definitions than this is skipped
	MaxCallers        int // call sites per changed function
	MaxSnippetLines   int // lines per definition snippet
	MaxTotalLines     int // lines across every snippet of one file
	MaxFileBytes      int // files larger than this are not searched
	CallerWindowLines int // lines of context on each side of a call site
}

// DefaultLimits keep the related code to a few thousand tokens per file.
var DefaultLimits = Limits{
	MaxSymbols:        16,
	MaxDefinitions:    2,
	MaxCallers:        4,
	MaxSnippetLines:   40,
	MaxTotalLines:     240,
	MaxFileBytes:      256 << 10,
	CallerWindowLines: 2,
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	if l.MaxSymbols > 0 {
		d.MaxSymbols = l.MaxSymbols
	}
	if l.MaxDefinitions > 0 {
		d.MaxDefinitions = l.MaxDefinitions
	}
	if l.MaxCallers > 0 {
		d.MaxCallers = l.MaxCallers
	}
	if l.MaxSnippetLines > 0 {
		d.MaxSnippetLines = l.MaxSnippetLines
	}
	if l.MaxTotalLines > 0 {
		d.MaxTotalLines = l.MaxTotalLines
	}
	if l.MaxFileBytes > 0 {
		d.MaxFileBytes = l.MaxFileBytes
	}
	if l.CallerWindowLines > 0 {
		d.CallerWindowLines = l.CallerWindowLines
	}
	return d
}

// Index answers Related queries over one snapshot. Build it once per run:
// it splits every searchable file into lines up front.
type Index struct {
	snap  Snapshot
	lim   Limits
	files map[string][]string // searchable path -> lines
	paths []string            // sorted keys of files
}

// NewIndex indexes the source files of snap. Files that are not source code
// in a known language, generated, vendored or larger than the limit are left
// out, so they are never searched and never quoted.
func NewIndex(snap Snapshot, lim Limits) *Index {
	lim = lim.withDefaults()
	ix := &Index{snap: snap, lim: lim, files: map[string][]string{}}
	for _, p := range snap.Files() {
		if langOf(p) == nil || skipPath(p) {
			continue
		}
		b, ok := snap.Read(p)
		if !ok || len(b) > lim.MaxFileBytes || isBinary(b) {
			continue
		}
		ix.files[p] = splitLines(b)
		ix.paths = append(ix.paths, p)
	}
	sort.Strings(ix.paths)
	return ix
}

// Related returns the snippets the changed lines of path depend on. post is
// the post-change file; added holds the 1-based numbers of its changed lines.
// Definitions come first, nearest directory first, then callers; the total
// is cut at MaxTotalLines.
func (ix *Index) Related(filePath string, post []string, added map[int]bool) []Snippet {
	if ix == nil || len(added) == 0 {
		return nil
	}
	lang := langOf(filePath)
	if lang == nil {
		return nil
	}
	local := definedNames(lang, post)

	var out []Snippet
	budget := ix.lim.MaxTotalLines
	take := func(s Snippet) bool {
		if len(s.Lines) > budget {
			return false
		}
		budget -= len(s.Lines)
		out = append(out, s)
		return true
	}

	for _, name := range ix.usedNames(lang, post, added, local) {
		defs := ix.definitions(name, filePath)
		if len(defs) == 0 || len(defs) > ix.lim.MaxDefinitions {
			continue
		}
		for _, d := range defs {
			take(d)
		}
	}
	for _, name := range changedDefinitions(lang, post, added) {
		for _, c := range ix.callers(name, filePath) {
			take(c)
		}
	}
	return out
}

// usedNames lists, in order of first use, the names the changed lines call
// or select that this file does not itself define.
func (ix *Index) usedNames(lang *language, post []string, added map[int]bool, local map[string]bool) []string {
	var lines []int
	for n := range added {
		if n >= 1 && n <= len(post) {
			lines = append(lines, n)
		}
	}
	sort.Ints(lines)
	seen := map[string]bool{}
	var out []string
	for _, n := range lines {
		code := stripComment(lang, post[n-1])
		for _, m := range useRE.FindAllStringSubmatch(code, -1) {
			name := m[1]
			if m[2] != "" {
				name = m[2]
			}
			if len(name) < 3 || seen[name] || local[name] || lang.keywords[name] || commonNames[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
			if len(out) == ix.lim.MaxSymbols {
				return out
			}
		}
	}
	return out
}

// useRE matches a call `name(` or a selector `.name`. Group 1 is the called
// name, group 2 the selected one.
var useRE = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(|\.([A-Za-z_][A-Za-z0-9_]*)\b`)

// definitions finds name's definitions outside filePath, nearest directory
// first. More than MaxDefinitions+1 is enough to know the name is ambiguous.
func (ix *Index) definitions(name, filePath string) []Snippet {
	var out []Snippet
	for _, p := range byProximity(ix.paths, filePath) {
		if p == filePath {
			continue
		}
		lang := langOf(p)
		lines := ix.files[p]
		for i, l := range lines {
			if !strings.Contains(l, name) || !lang.defines(l, name) {
				continue
			}
			start := docStart(lang, lines, i)
			out = append(out, Snippet{
				Path: p, Symbol: name, Kind: KindDefinition,
				StartLine: start + 1,
				Lines:     lines[start:blockEnd(lang, lines, i, ix.lim.MaxSnippetLines)],
			})
			if len(out) > ix.lim.MaxDefinitions {
				return out
			}
		}
	}
	return out
}

// callers finds call sites of name outside filePath, nearest directory first.
func (ix *Index) callers(name, filePath string) []Snippet {
	call := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	var out []Snippet
	for _, p := range byProximity(ix.paths, filePath) {
		if p == filePath {
			continue
		}
		lang := langOf(p)
		lines := ix.files[p]
		for i, l := range lines {
			if !strings.Contains(l, name) || !call.MatchString(stripComment(lang, l)) || lang.defines(l, name) {
				continue
			}
			lo := max(0, i-ix.lim.CallerWindowLines)
			hi := min(len(lines), i+ix.lim.CallerWindowLines+1)
			out = append(out, Snippet{Path: p, Symbol: name, Kind: KindCaller, StartLine: lo + 1, Lines: lines[lo:hi]})
			if len(out) == ix.lim.MaxCallers {
				return out
			}
		}
	}
	return out
}

// changedDefinitions lists the names whose definition line was changed.
func changedDefinitions(lang *language, post []string, added map[int]bool) []string {
	var lines []int
	for n := range added {
		if n >= 1 && n <= len(post) {
			lines = append(lines, n)
		}
	}
	sort.Ints(lines)
	var out []string
	seen := map[string]bool{}
	for _, n := range lines {
		if name := lang.definedName(post[n-1]); name != "" && !seen[name] && len(name) >= 3 && !commonNames[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// definedNames is every name post defines, so a use of one is not looked up
// elsewhere: the model already sees it.
func definedNames(lang *language, post []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range post {
		if name := lang.definedName(l); name != "" {
			out[name] = true
		}
	}
	return out
}

// byProximity orders paths by how many leading directories they share with
// from, most first, then lexically: a definition in the same package beats
// one with the same name elsewhere.
func byProximity(paths []string, from string) []string {
	dir := path.Dir(from)
	out := append([]string(nil), paths...)
	sort.SliceStable(out, func(i, j int) bool {
		return sharedPrefix(path.Dir(out[i]), dir) > sharedPrefix(path.Dir(out[j]), dir)
	})
	return out
}

func sharedPrefix(a, b string) int {
	if a == b {
		return 1 << 20
	}
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}

// docStart walks back from a definition over the comment block above it, at
// most four lines, so a snippet carries the definition's documented contract.
func docStart(lang *language, lines []string, i int) int {
	j := i
	for j > 0 && i-j < 4 {
		t := strings.TrimSpace(lines[j-1])
		if t == "" || !lang.isComment(t) {
			break
		}
		j--
	}
	return j
}

// blockEnd returns the exclusive end of the block opened at line i: the line
// where braces balance for brace languages, the first line indented no deeper
// than the definition for indentation languages. It is capped at maxLines.
func blockEnd(lang *language, lines []string, i, maxLines int) int {
	limit := min(len(lines), i+maxLines)
	if lang.indentBlocks {
		base := indentOf(lines[i])
		for j := i + 1; j < limit; j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if indentOf(lines[j]) <= base {
				return j
			}
		}
		return limit
	}
	depth, opened := 0, false
	for j := i; j < limit; j++ {
		// Strings first: a "//" inside a string literal (a URL) is not a
		// comment, and cutting there would hide the line's brace.
		code := stripComment(lang, stripStrings(lines[j]))
		for _, r := range code {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth <= 0 {
			return j + 1
		}
		if !opened && j > i && strings.TrimSpace(lines[j]) == "" {
			// A declaration with no body (a type alias, a one-line const).
			return j
		}
	}
	if !opened {
		return min(len(lines), i+1)
	}
	return limit
}

func indentOf(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

var stringRE = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)*\"|'(?:[^'\\\\]|\\\\.)*'|`[^`]*`")

func stripStrings(s string) string { return stringRE.ReplaceAllString(s, `""`) }

func stripComment(lang *language, s string) string {
	if lang.lineComment == "" {
		return s
	}
	if i := strings.Index(s, lang.lineComment); i >= 0 {
		return s[:i]
	}
	return s
}

func splitLines(b []byte) []string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func isBinary(b []byte) bool {
	n := min(len(b), 8000)
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

// skipPath leaves out trees whose definitions are not the repository's own
// or are not written by hand.
func skipPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "vendor", "node_modules", "third_party", "dist", "build", ".git", "__pycache__", "target":
			return true
		}
	}
	base := path.Base(p)
	return strings.Contains(base, ".min.") || strings.HasSuffix(base, ".pb.go") ||
		strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, ".gen.go") || strings.HasSuffix(base, ".d.ts")
}

// commonNames are resolved nowhere: they are defined in many places in any
// repository of size, so a lookup returns noise or nothing.
var commonNames = map[string]bool{
	"New": true, "new": true, "Run": true, "run": true, "main": true, "init": true, "Error": true,
	"String": true, "Close": true, "Read": true, "Write": true, "Get": true, "Set": true, "get": true,
	"set": true, "Len": true, "len": true, "append": true, "make": true, "copy": true, "delete": true,
	"panic": true, "print": true, "println": true, "Printf": true, "Sprintf": true, "Errorf": true,
	"Println": true, "Fprintf": true, "Fatal": true, "Fatalf": true, "Logf": true, "Helper": true,
	"push": true, "pop": true, "map": true, "filter": true, "then": true, "catch": true, "log": true,
	"join": true, "split": true, "trim": true, "self": true, "this": true, "super": true, "test": true,
	"Equal": true, "equal": true, "ok": true, "err": true, "has": true, "add": true, "Add": true,
	"Contains": true, "HasPrefix": true, "HasSuffix": true, "TrimSpace": true, "Join": true, "Split": true,
	"format": true, "keys": true, "values": true, "items": true, "update": true, "append_": true,
	"range": true, "open": true, "Open": true, "call": true, "apply": true, "bind": true, "toString": true,
	"length": true, "size": true, "Lock": true, "Unlock": true, "Done": true, "Wait": true, "Do": true,
}
