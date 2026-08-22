// Schema-driven cross-check of the parsed configuration tree. The JSON
// Schema (schema.json, embedded) is the source of truth for key names and
// types: unknown keys anywhere are errors, never silently ignored (§6).
// The checker implements the subset of draft-07 this schema uses: type,
// enum, required, additionalProperties (false or a schema), properties,
// items, $ref, minimum/maximum, minLength, pattern.
package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

//go:embed schema.json
var schemaJSON []byte

var compiledSchema map[string]interface{}

func rootSchema() map[string]interface{} {
	if compiledSchema == nil {
		if err := json.Unmarshal(schemaJSON, &compiledSchema); err != nil {
			panic("config: embedded schema.json is invalid: " + err.Error())
		}
	}
	return compiledSchema
}

func resolveRef(ref string) (map[string]interface{}, error) {
	if len(ref) < 3 || ref[:2] != "#/" {
		return nil, fmt.Errorf("config: unsupported schema $ref %q", ref)
	}
	node := interface{}(rootSchema())
	for _, part := range splitPathRefs(ref[2:]) {
		m, ok := node.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("config: bad $ref path %q", ref)
		}
		node, ok = m[part]
		if !ok {
			return nil, fmt.Errorf("config: bad $ref path %q", ref)
		}
	}
	sm, ok := node.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("config: bad $ref target %q", ref)
	}
	return sm, nil
}

func splitPathRefs(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// schemaCheck walks the parsed tree against the embedded schema and appends
// every key-name/type violation to probs.
func schemaCheck(tree interface{}, probs *[]Problem) {
	checkNode(tree, rootSchema(), "", probs)
}

func checkNode(v interface{}, sch map[string]interface{}, path string, probs *[]Problem) {
	if r, ok := sch["$ref"].(string); ok {
		resolved, err := resolveRef(r)
		if err != nil {
			panic(err)
		}
		sch = resolved
	}
	t, _ := sch["type"].(string)
	switch t {
	case "object":
		m, ok := v.(map[string]interface{})
		if !ok {
			addf(probs, path, "expected an object, got %s", describe(v))
			return
		}
		props, _ := sch["properties"].(map[string]interface{})
		switch ap := sch["additionalProperties"].(type) {
		case bool:
			if ap {
				break
			}
			for _, k := range sortedKeys(m) {
				if _, known := props[k]; !known {
					addf(probs, joinPath(path, k), "unknown key")
				}
			}
		case map[string]interface{}:
			for _, k := range sortedKeys(m) {
				if _, known := props[k]; known {
					continue
				}
				checkNode(m[k], ap, joinPath(path, k), probs)
			}
		}
		for _, k := range sortedKeys(m) {
			sub, known := props[k]
			if !known && sch["additionalProperties"] != nil {
				continue // already handled above
			}
			if !known {
				continue
			}
			sm, ok := sub.(map[string]interface{})
			if !ok {
				continue
			}
			checkNode(m[k], sm, joinPath(path, k), probs)
		}
		for _, req := range stringList(sch["required"]) {
			if _, ok := m[req]; !ok {
				addf(probs, joinPath(path, req), "required field is missing")
			}
		}
	case "array":
		arr, ok := v.([]interface{})
		if !ok {
			addf(probs, path, "expected a list, got %s", describe(v))
			return
		}
		items, _ := sch["items"].(map[string]interface{})
		if items == nil {
			return
		}
		for idx, el := range arr {
			checkNode(el, items, fmt.Sprintf("%s[%d]", path, idx), probs)
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			addf(probs, path, "expected a string, got %s", describe(v))
			return
		}
		checkStringConstraints(s, sch, path, probs)
	case "integer":
		n, ok := asInt(v)
		if !ok {
			addf(probs, path, "expected an integer, got %s", describe(v))
			return
		}
		checkNumeric(float64(n), sch, path, probs)
	case "number":
		f, ok := asFloat(v)
		if !ok {
			addf(probs, path, "expected a number, got %s", describe(v))
			return
		}
		checkNumeric(f, sch, path, probs)
	case "boolean":
		if _, ok := v.(bool); !ok {
			addf(probs, path, "expected true or false, got %s", describe(v))
		}
	}
}

func checkStringConstraints(s string, sch map[string]interface{}, path string, probs *[]Problem) {
	if enum, ok := sch["enum"].([]interface{}); ok {
		found := false
		for _, e := range enum {
			if es, ok := e.(string); ok && es == s {
				found = true
				break
			}
		}
		if !found {
			addf(probs, path, "%q is not one of: %s", s, enumList(enum))
			return
		}
	}
	if ml, ok := asFloat(sch["minLength"]); ok && float64(len(s)) < ml {
		addf(probs, path, "must not be empty")
	}
	if pat, ok := sch["pattern"].(string); ok {
		re, err := regexp.Compile(pat)
		if err == nil && !re.MatchString(s) {
			addf(probs, path, "%q does not match the required shape", s)
		}
	}
}

func checkNumeric(f float64, sch map[string]interface{}, path string, probs *[]Problem) {
	if enum, ok := sch["enum"].([]interface{}); ok {
		for _, e := range enum {
			if ef, ok := asFloat(e); ok && ef == f {
				return
			}
		}
		addf(probs, path, "%v is not one of: %s", f, enumList(enum))
		return
	}
	if min, ok := asFloat(sch["minimum"]); ok && f < min {
		addf(probs, path, "must be >= %v, got %v", min, f)
	}
	if max, ok := asFloat(sch["maximum"]); ok && f > max {
		addf(probs, path, "exceeds the hard cap of %v enforced by the schema (got %v)", max, f)
	}
}

// ---- small helpers over the generic tree ----

func describe(v interface{}) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "a string"
	case int:
		return "an integer"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []interface{}:
		return "a list"
	case map[string]interface{}:
		return "an object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func asInt(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

func asFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func stringList(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func enumList(enum []interface{}) string {
	out := ""
	for i, e := range enum {
		if i > 0 {
			out += ", "
		}
		if s, ok := e.(string); ok {
			out += `"` + s + `"`
		} else {
			out += fmt.Sprintf("%v", e)
		}
	}
	return out
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
