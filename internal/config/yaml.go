// A deliberately small YAML subset parser, sufficient for .github/cite.yml:
// block mappings, block sequences (including sequences of mappings), flow
// sequences [a, b], flow mappings {k: v}, single/double-quoted strings,
// comments, and plain scalars typed as bool / int / float / null / string.
//
// Limits (by design — the config file is not a programming language):
//   - no anchors, aliases, tags, or merge keys (<<)
//   - no multi-document streams (---)
//   - no block scalars (| and >)
//   - indentation with spaces only; a tab in indentation is an error
//   - comments must start at the beginning of a line or after whitespace
//   - flow collections do not nest block collections inside themselves
//   - duplicate keys are an error, not a silent overwrite
//   - a syntax error aborts the parse at the first offending line; only
//     semantic validation (validate.go) aggregates multiple problems
package config

import (
	"fmt"
	"strconv"
	"strings"
)

type yline struct {
	num    int    // 1-based source line number, for error messages
	indent int    // number of leading spaces
	text   string // content with comments stripped and surrounding space trimmed
}

type yamlParser struct {
	ls []yline
	i  int
	// lines maps the dotted path of every key and sequence element to the
	// source line it starts on, in the same path syntax schemaCheck reports
	// ("providers.gateway.models[0].id"). It is what lets a problem found
	// after parsing name the line that caused it.
	lines map[string]int
}

// SyntaxError is a YAML syntax error at one line of one file. File is empty
// when the bytes did not come from a named file; the message then says
// "cite.yml", the name the file has everywhere in the documentation.
type SyntaxError struct {
	File string
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string {
	name := e.File
	if name == "" {
		name = "cite.yml"
	}
	return fmt.Sprintf("%s line %d: %s", name, e.Line, e.Msg)
}

func yamlErrf(l yline, format string, args ...interface{}) error {
	return &SyntaxError{Line: l.num, Msg: fmt.Sprintf(format, args...)}
}

// parseYAML parses src into a generic tree of
// map[string]interface{} / []interface{} / string / int / float64 / bool / nil.
// An empty document (no content lines) returns (nil, nil).
func parseYAML(src []byte) (interface{}, error) {
	tree, _, err := parseYAMLLines(src)
	return tree, err
}

// parseYAMLLines is parseYAML that also returns the source line of every key
// and sequence element, keyed by dotted path.
func parseYAMLLines(src []byte) (interface{}, map[string]int, error) {
	p, err := newYAMLLines(src)
	if err != nil {
		return nil, nil, err
	}
	if len(p.ls) == 0 {
		return nil, p.lines, nil
	}
	root, err := p.node("")
	if err != nil {
		return nil, nil, err
	}
	if p.i < len(p.ls) {
		return nil, nil, yamlErrf(p.ls[p.i], "unexpected content (bad indentation?)")
	}
	return root, p.lines, nil
}

func newYAMLLines(src []byte) (*yamlParser, error) {
	p := &yamlParser{lines: map[string]int{}}
	for n, raw := range strings.Split(string(src), "\n") {
		raw = strings.TrimRight(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue // blank (possibly whitespace-only) line
		}
		j := 0
		for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t') {
			if raw[j] == '\t' {
				return nil, &SyntaxError{Line: n + 1, Msg: "tab in indentation (spaces only)"}
			}
			j++
		}
		content := strings.TrimRight(stripComment(raw[j:]), " ")
		if content == "" {
			continue // blank or comment-only line
		}
		p.ls = append(p.ls, yline{num: n + 1, indent: j, text: content})
	}
	return p, nil
}

// stripComment removes a trailing "# ..." comment, honouring quotes. A '#'
// only starts a comment at the start of the text or after whitespace.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' '):
			return s[:i]
		}
	}
	return s
}

// node parses the collection or scalar starting at the current line. path
// is the dotted path of the value being parsed, "" for the document root.
func (p *yamlParser) node(path string) (interface{}, error) {
	l := p.ls[p.i]
	if l.text == "-" || strings.HasPrefix(l.text, "- ") {
		return p.sequence(l.indent, path)
	}
	return p.mapping(l.indent, path)
}

func (p *yamlParser) mapping(indent int, path string) (interface{}, error) {
	m := map[string]interface{}{}
	for p.i < len(p.ls) {
		l := p.ls[p.i]
		if l.indent < indent || l.text == "-" || strings.HasPrefix(l.text, "- ") {
			break
		}
		if l.indent > indent {
			return nil, yamlErrf(l, "unexpected indentation")
		}
		key, rest, err := splitKey(l)
		if err != nil {
			return nil, err
		}
		if _, dup := m[key]; dup {
			return nil, yamlErrf(l, "duplicate key %q", key)
		}
		kp := joinPath(path, key)
		p.lines[kp] = l.num
		p.i++
		if rest == "" {
			// Value is a nested block (or nothing).
			if p.i < len(p.ls) && p.ls[p.i].indent > indent {
				v, err := p.node(kp)
				if err != nil {
					return nil, err
				}
				m[key] = v
			} else {
				m[key] = nil
			}
			continue
		}
		v, err := p.scalarOrFlow(rest, l, kp)
		if err != nil {
			return nil, err
		}
		m[key] = v
	}
	return m, nil
}

func (p *yamlParser) sequence(indent int, path string) (interface{}, error) {
	var s []interface{}
	for p.i < len(p.ls) {
		l := p.ls[p.i]
		if l.indent != indent || (l.text != "-" && !strings.HasPrefix(l.text, "- ")) {
			break
		}
		ep := fmt.Sprintf("%s[%d]", path, len(s))
		p.lines[ep] = l.num
		body := strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " ")
		if body == "" {
			// Nested block on following, deeper-indented lines.
			p.i++
			if p.i < len(p.ls) && p.ls[p.i].indent > indent {
				v, err := p.node(ep)
				if err != nil {
					return nil, err
				}
				s = append(s, v)
			} else {
				s = append(s, nil)
			}
			continue
		}
		// "- key: value" starts a mapping whose keys live two columns in.
		if _, _, err := splitKey(yline{num: l.num, text: body}); err == nil {
			keyIndent := l.indent + (len(l.text) - len(body))
			p.ls[p.i] = yline{num: l.num, indent: keyIndent, text: body}
			v, err := p.mapping(keyIndent, ep)
			if err != nil {
				return nil, err
			}
			s = append(s, v)
			continue
		}
		p.i++
		v, err := p.scalarOrFlow(body, l, ep)
		if err != nil {
			return nil, err
		}
		s = append(s, v)
	}
	return s, nil
}

// splitKey splits "key: rest" at the first ':' that is outside quotes and
// followed by a space or end of line (so "https://host" is not a key).
func splitKey(l yline) (key, rest string, err error) {
	var quote byte
	for i := 0; i < len(l.text); i++ {
		c := l.text[i]
		switch {
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ':' && (i+1 == len(l.text) || l.text[i+1] == ' '):
			rawKey := strings.TrimSpace(l.text[:i])
			key, err = unquote(rawKey)
			if err != nil {
				return "", "", yamlErrf(l, "%v", err)
			}
			if key == "" {
				return "", "", yamlErrf(l, "empty key")
			}
			return key, strings.TrimSpace(l.text[i+1:]), nil
		}
	}
	return "", "", yamlErrf(l, "expected \"key: value\", got %q", l.text)
}

func (p *yamlParser) scalarOrFlow(s string, l yline, path string) (interface{}, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	switch s[0] {
	case '[':
		v, rest, err := p.parseFlow(s, l, path)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rest) != "" {
			return nil, yamlErrf(l, "unexpected text after flow sequence: %q", rest)
		}
		return v, nil
	case '{':
		v, rest, err := p.parseFlow(s, l, path)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rest) != "" {
			return nil, yamlErrf(l, "unexpected text after flow mapping: %q", rest)
		}
		return v, nil
	default:
		return decodeScalar(s, l)
	}
}

// parseFlow parses a flow sequence or mapping, returning the value and the
// unconsumed remainder. A flow collection sits on one line, so every path
// inside it records that line.
func (p *yamlParser) parseFlow(s string, l yline, path string) (interface{}, string, error) {
	switch s[0] {
	case '[':
		var out []interface{}
		rest := strings.TrimSpace(s[1:])
		for {
			if rest == "" {
				return nil, "", yamlErrf(l, "unterminated flow sequence")
			}
			if rest[0] == ']' {
				return out, rest[1:], nil
			}
			ep := fmt.Sprintf("%s[%d]", path, len(out))
			p.lines[ep] = l.num
			v, r, err := p.parseFlowItem(rest, l, ep)
			if err != nil {
				return nil, "", err
			}
			out = append(out, v)
			rest = strings.TrimSpace(r)
			if rest != "" && rest[0] == ',' {
				rest = strings.TrimSpace(rest[1:])
			}
		}
	case '{':
		out := map[string]interface{}{}
		rest := strings.TrimSpace(s[1:])
		for {
			if rest == "" {
				return nil, "", yamlErrf(l, "unterminated flow mapping")
			}
			if rest[0] == '}' {
				return out, rest[1:], nil
			}
			k, r, err := splitFlowKey(rest, l)
			if err != nil {
				return nil, "", err
			}
			if _, dup := out[k]; dup {
				return nil, "", yamlErrf(l, "duplicate key %q in flow mapping", k)
			}
			kp := joinPath(path, k)
			p.lines[kp] = l.num
			v, r2, err := p.parseFlowItem(strings.TrimSpace(r), l, kp)
			if err != nil {
				return nil, "", err
			}
			out[k] = v
			rest = strings.TrimSpace(r2)
			if rest != "" && rest[0] == ',' {
				rest = strings.TrimSpace(rest[1:])
			}
		}
	default:
		return nil, "", yamlErrf(l, "internal: parseFlow on %q", s)
	}
}

func splitFlowKey(s string, l yline) (string, string, error) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ':':
			key, err := unquote(strings.TrimSpace(s[:i]))
			if err != nil {
				return "", "", yamlErrf(l, "%v", err)
			}
			return key, s[i+1:], nil
		}
	}
	return "", "", yamlErrf(l, "expected \"key: value\" in flow mapping, got %q", s)
}

func (p *yamlParser) parseFlowItem(s string, l yline, path string) (interface{}, string, error) {
	if s == "" {
		return nil, "", yamlErrf(l, "missing value in flow collection")
	}
	switch s[0] {
	case '[', '{':
		return p.parseFlow(s, l, path)
	case '"', '\'':
		str, rest, err := scanQuoted(s, l)
		if err != nil {
			return nil, "", err
		}
		return str, rest, nil
	}
	end := strings.IndexAny(s, ",]}")
	tok := s
	rest := ""
	if end >= 0 {
		tok, rest = s[:end], s[end:]
	}
	v, err := decodeScalar(strings.TrimSpace(tok), l)
	return v, rest, err
}

// scanQuoted consumes a quoted string at the start of s, returning the
// decoded value and the remainder.
func scanQuoted(s string, l yline) (string, string, error) {
	q := s[0]
	var sb strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if q == '\'' {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' { // '' escapes a quote
					sb.WriteByte('\'')
					i++
					continue
				}
				return sb.String(), s[i+1:], nil
			}
			sb.WriteByte(c)
			continue
		}
		// double quotes
		if c == '\\' {
			if i+1 >= len(s) {
				return "", "", yamlErrf(l, "trailing backslash in quoted string")
			}
			i++
			switch esc := s[i]; esc {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '"', '\\':
				sb.WriteByte(esc)
			default:
				sb.WriteByte(esc)
			}
			continue
		}
		if c == '"' {
			return sb.String(), s[i+1:], nil
		}
		sb.WriteByte(c)
	}
	return "", "", yamlErrf(l, "unterminated quoted string: %q", s)
}

func decodeScalar(s string, l yline) (interface{}, error) {
	if s == "" {
		return nil, nil
	}
	if s[0] == '"' || s[0] == '\'' {
		v, rest, err := scanQuoted(s, l)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rest) != "" {
			return nil, yamlErrf(l, "unexpected text after quoted string: %q", rest)
		}
		return v, nil
	}
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "~":
		return nil, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	return s, nil
}

func unquote(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if s[0] == '"' || s[0] == '\'' {
		v, rest, err := scanQuoted(s, yline{})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(rest) != "" {
			return "", fmt.Errorf("unexpected text after quoted key: %q", rest)
		}
		return v, nil
	}
	return s, nil
}
