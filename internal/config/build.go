// Decoding of the schema-checked generic tree into Config. Type assertions
// are safe because schemaCheck ran first, but the code stays defensive.
package config

import (
	"regexp"
	"strings"

	"github.com/elecnix/cite/internal/model"
)

func buildConfig(tree interface{}, probs *[]Problem) *Config {
	c := Default()
	m, ok := tree.(map[string]interface{})
	if !ok {
		addf(probs, "", "top level of the configuration must be a mapping, got %s", describe(tree))
		return c
	}

	// Defaults are only replaced by keys actually present.
	if v, ok := m["model"]; ok {
		c.Model = asString(v)
	}
	if v, ok := m["max_comments"]; ok {
		if n, ok := asInt(v); ok {
			c.MaxComments = int(n)
		}
	}
	if v, ok := m["paths_ignore"]; ok {
		c.PathsIgnore = stringSlice(v)
	}
	if v, ok := m["nits"]; ok {
		if b, ok := v.(bool); ok {
			c.Nits = b
		}
	}
	if v, ok := m["gate"]; ok {
		c.Gate = asString(v)
	}
	if v, ok := m["compat_profile"]; ok {
		c.CompatProfile = asString(v)
	}
	if v, ok := m["blocking_categories"]; ok {
		c.BlockingCategories = nil // replace the defaults with the explicit shrink
		for _, s := range stringSlice(v) {
			c.BlockingCategories = append(c.BlockingCategories, model.Category(s))
		}
	}
	if v, ok := m["fallback"]; ok {
		c.Fallback = stringSlice(v)
	}
	if v, ok := m["providers"]; ok {
		c.Providers = buildProviders(v, probs)
	}
	if v, ok := m["roles"]; ok {
		c.Roles = buildRoles(v, probs)
	}
	return c
}

func buildProviders(v interface{}, probs *[]Problem) map[string]*model.Provider {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	out := map[string]*model.Provider{}
	for _, name := range sortedKeys(m) {
		pm, ok := m[name].(map[string]interface{})
		if !ok {
			continue // schemaCheck already reported the type problem
		}
		p := &model.Provider{Name: name}
		if s, ok := pm["base_url"]; ok {
			p.BaseURL = asString(s)
		}
		if s, ok := pm["api"]; ok {
			p.API = model.APIStyle(asString(s))
		}
		if s, ok := pm["api_key"]; ok {
			p.APIKey = model.CredentialExpr(asString(s))
		}
		if h, ok := pm["headers"].(map[string]interface{}); ok {
			p.Headers = map[string]string{}
			for _, k := range sortedKeys(h) {
				p.Headers[k] = asString(h[k])
			}
		}
		if models, ok := pm["models"].([]interface{}); ok {
			for i, mv := range models {
				mm, ok := mv.(map[string]interface{})
				if !ok {
					continue
				}
				entry := model.ModelEntry{ID: asString(mm["id"])}
				if n, ok := asInt(mm["context_window"]); ok {
					entry.ContextWindow = int(n)
				}
				if n, ok := asInt(mm["max_tokens"]); ok {
					entry.MaxTokens = int(n)
				}
				if cv, ok := mm["cost"].(map[string]interface{}); ok {
					entry.Cost = &model.Cost{
						Input:      asFloatOrZero(cv["input"]),
						Output:     asFloatOrZero(cv["output"]),
						CacheRead:  asFloatOrZero(cv["cache_read"]),
						CacheWrite: asFloatOrZero(cv["cache_write"]),
					}
				}
				_ = i
				p.Models = append(p.Models, entry)
			}
		}
		out[name] = p
	}
	return out
}

func buildRoles(v interface{}, probs *[]Problem) map[model.Role]RoleSpec {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	out := map[model.Role]RoleSpec{}
	for _, k := range sortedKeys(m) {
		rm, ok := m[k].(map[string]interface{})
		if !ok {
			continue // schemaCheck already reported the type problem
		}
		spec := RoleSpec{}
		if s, ok := rm["model"]; ok {
			spec.Model = asString(s)
		}
		if s, ok := rm["timeout"]; ok {
			spec.Timeout = asString(s)
		}
		if n, ok := asInt(rm["max_output_tokens"]); ok {
			spec.MaxOutputTokens = int(n)
		}
		if n, ok := asInt(rm["concurrency"]); ok {
			spec.Concurrency = int(n)
		}
		out[model.Role(k)] = spec
	}
	return out
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func asFloatOrZero(v interface{}) float64 {
	f, _ := asFloat(v)
	return f
}

func stringSlice(v interface{}) []string {
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

// ---- credential expression shapes (§6): literal | $VAR | ${VAR} | !command ----

var (
	envVarRe    = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*$`)
	envVarBrace = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
)

// credentialShapeProblem returns "" when expr is a well-formed credential
// expression, or a message describing the problem.
func credentialShapeProblem(expr string) string {
	if expr == "" {
		return "credential expression must not be empty"
	}
	switch {
	case strings.HasPrefix(expr, "!"):
		if strings.TrimSpace(expr[1:]) == "" {
			return `credential command after "!" must not be empty`
		}
		return ""
	case strings.HasPrefix(expr, "${"):
		if !envVarBrace.MatchString(expr) {
			return `must be ${VAR} with VAR matching [A-Za-z_][A-Za-z0-9_]*`
		}
		return ""
	case strings.HasPrefix(expr, "$"):
		if !envVarRe.MatchString(expr) {
			return `must be $VAR with VAR matching [A-Za-z_][A-Za-z0-9_]*`
		}
		return ""
	default:
		return "" // literal
	}
}
