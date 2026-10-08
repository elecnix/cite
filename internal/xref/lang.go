package xref

import (
	"path"
	"regexp"
	"strings"
)

// language is what xref knows about one family of source files: which lines
// define a name, how a comment starts, and whether blocks are braces or
// indentation.
type language struct {
	name         string
	lineComment  string
	indentBlocks bool
	// defRE matches a line that defines something; the defined name is the
	// first non-empty capture group.
	defRE    *regexp.Regexp
	keywords map[string]bool
}

// definedName returns the name line defines, or "".
func (l *language) definedName(line string) string {
	m := l.defRE.FindStringSubmatch(line)
	for _, g := range m[min(1, len(m)):] {
		if g != "" {
			return g
		}
	}
	return ""
}

// defines reports whether line defines name.
func (l *language) defines(line, name string) bool { return l.definedName(line) == name }

func (l *language) isComment(trimmed string) bool {
	if l.lineComment != "" && strings.HasPrefix(trimmed, l.lineComment) {
		return true
	}
	return strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "\"\"\"")
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

var (
	langGo = &language{
		name: "go", lineComment: "//",
		defRE: regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\[(]|^\s*type\s+([A-Za-z_]\w*)\b|^(?:var|const)\s+([A-Za-z_]\w*)\b|^\t([A-Z]\w*)\s*(?:[\w.\[\]*]+\s*)?=[^=]`),
		keywords: words(`break case chan const continue default defer else fallthrough for func go goto if
			import interface map package range return select struct switch type var nil true false iota
			string int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr byte rune float32
			float64 complex64 complex128 bool error any cap close len new make append copy delete panic
			recover print println min max clear`),
	}
	langPython = &language{
		name: "python", lineComment: "#", indentBlocks: true,
		defRE: regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(|^\s*class\s+([A-Za-z_]\w*)\b|^([A-Z_][A-Z0-9_]*)\s*(?::[^=]*)?=`),
		keywords: words(`and as assert async await break class continue def del elif else except finally
			for from global if import in is lambda nonlocal not or pass raise return try while with yield
			None True False print len range str int float list dict set tuple isinstance super open type
			enumerate zip sorted reversed min max sum any all getattr setattr hasattr`),
	}
	langJS = &language{
		name: "javascript", lineComment: "//",
		defRE: regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*[<(]` +
			`|^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=` +
			`|^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)` +
			`|^\s*(?:export\s+)?(?:interface|type|enum)\s+([A-Za-z_$][\w$]*)` +
			`|^\s+(?:(?:public|private|protected|static|async|readonly|override)\s+)*([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*(?::[^{]+)?\{\s*$`),
		keywords: words(`break case catch class const continue debugger default delete do else export
			extends finally for function if import in instanceof new return super switch this throw try
			typeof var void while with yield let static async await of null undefined true false
			console require module exports Promise Object Array String Number Boolean JSON Math Date
			Error Map Set constructor`),
	}
	langRust = &language{
		name: "rust", lineComment: "//",
		defRE: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:const\s+)?(?:unsafe\s+)?fn\s+([A-Za-z_]\w*)\s*[<(]` +
			`|^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|type|union)\s+([A-Za-z_]\w*)` +
			`|^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const|static)\s+([A-Z_][A-Z0-9_]*)\s*:`),
		keywords: words(`as break const continue crate else enum extern false fn for if impl in let loop
			match mod move mut pub ref return self Self static struct super trait true type unsafe use
			where while async await dyn Some None Ok Err Box Vec String Option Result println format
			vec assert assert_eq unwrap expect clone into iter collect`),
	}
	langShell = &language{
		name: "shell", lineComment: "#",
		defRE:    regexp.MustCompile(`^\s*(?:function\s+)?([A-Za-z_][\w-]*)\s*\(\)\s*\{?`),
		keywords: words(`if then else elif fi for while do done case esac in function return local export echo printf set shift exit test`),
	}
	langJava = &language{
		name: "java", lineComment: "//",
		defRE: regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized|native|override|open|internal|suspend|inline)\s+)*(?:class|interface|enum|record|object|fun)\s+([A-Za-z_]\w*)` +
			`|^\s*(?:(?:public|private|protected|static|final|abstract|synchronized|native)\s+)+[\w<>\[\],.? ]+\s+([A-Za-z_]\w*)\s*\(`),
		keywords: words(`abstract assert boolean break byte case catch char class const continue default do
			double else enum extends final finally float for goto if implements import instanceof int
			interface long native new package private protected public return short static super switch
			synchronized this throw throws transient try void volatile while null true false String
			System Object List Map fun val var when`),
	}
	langC = &language{
		name: "c", lineComment: "//",
		defRE: regexp.MustCompile(`^(?:static\s+|inline\s+|extern\s+|const\s+|unsigned\s+|struct\s+)*[A-Za-z_][\w\s\*&:<>,]*?[\s\*&]([A-Za-z_]\w*)\s*\([^;]*$` +
			`|^\s*(?:typedef\s+)?(?:struct|class|enum|union)\s+([A-Za-z_]\w*)\s*\{?` +
			`|^#define\s+([A-Za-z_]\w*)`),
		keywords: words(`auto break case char const continue default do double else enum extern float for
			goto if inline int long register restrict return short signed sizeof static struct switch
			typedef union unsigned void volatile while NULL true false malloc free printf memcpy memset
			strlen class public private protected template typename namespace using virtual new delete`),
	}
)

var langByExt = map[string]*language{
	".go": langGo,
	".py": langPython, ".pyi": langPython,
	".js": langJS, ".jsx": langJS, ".mjs": langJS, ".cjs": langJS, ".ts": langJS, ".tsx": langJS, ".mts": langJS, ".cts": langJS,
	".rs": langRust,
	".sh": langShell, ".bash": langShell,
	".java": langJava, ".kt": langJava, ".kts": langJava, ".scala": langJava, ".cs": langJava,
	".c": langC, ".h": langC, ".cc": langC, ".cpp": langC, ".hpp": langC, ".cxx": langC,
}

// langOf returns the language of p, or nil for a file xref does not search.
func langOf(p string) *language { return langByExt[strings.ToLower(path.Ext(p))] }
