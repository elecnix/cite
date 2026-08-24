// Package config loads and validates .github/cite.yml (PLAN §6).
//
// Everything that controls the model call or the verdict is read from the
// base ref (§12, I3), so parsing is strict: unknown keys anywhere are errors,
// never silently ignored — a typo in a compatibility key must fail loudly
// rather than change behaviour invisibly.
//
// The YAML support is a deliberately small subset parser (yaml.go) sufficient
// for this file shape; see that file for its documented limits.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/model"
)

// Defaults for the v1 surface. A repository that never writes a config file
// gets sensible behaviour forever.
const (
	DefaultModel         = "openai/gpt-5-mini"
	DefaultMaxComments   = 10
	DefaultCompatProfile = "2026-08"

	GateComment = "comment"
	GateBlock   = "block"

	MaxCommentsCap = 20 // hard cap enforced by the schema
)

// Role defaults (§7): timeouts are per role, not global, because a slow local
// model and a fast hosted one cannot share one number.
//
// There is deliberately no fixed DefaultReviewTimeout any more: the review
// deadline is derived from the resolved output cap (issue #28). The old fixed
// 120s was calibrated when the review cap defaulted to 4096 output tokens;
// raising the cap to 32768 (DefaultReviewMaxOutputTokens below) allowed
// responses eight times longer to finish, and they died at "context deadline
// exceeded" — surfacing as COULD_NOT_EVALUATE — before emitting a verdict.
//
// Triage keeps a fixed timeout, but 30s proved too tight for real providers:
// that number assumed fast hosted models, while providers queue requests and
// stall long before the first byte — dogfooding CI saw triage calls against a
// large model die at "context deadline exceeded" twice in ~30s intervals. A
// call that would have succeeded at 35s instead drags its whole file to
// COULD_NOT_EVALUATE, so the budget covers provider queueing plus
// time-to-first-token.
const (
	DefaultTriageTimeout     = 120 * time.Second
	DefaultAssembleTimeout   = 60 * time.Second
	DefaultReviewConcurrency = 8
)

// Review-deadline calibration (issue #28). When no explicit
// roles.review.timeout is configured, the review deadline is derived from the
// same resolved output cap the call is bounded by:
//
//	deadline = ReviewTimeoutBase + maxOutputTokens / AssumedGenerationRate
//
// The numbers must stay visible together because they are coupled: raise the
// token budget and the wall-clock budget moves with it.
const (
	// ReviewTimeoutBase covers the part of the call that does not scale with
	// output length: prompt upload, provider queueing and network overhead.
	ReviewTimeoutBase = 60 * time.Second

	// AssumedGenerationRate is the output throughput, in tokens per second,
	// assumed when sizing the deadline. It is deliberately conservative —
	// sized for a mid-tier hosted model, not a top-tier endpoint. A faster
	// provider simply finishes early; a deadline sized on a fast rate turns
	// slow-but-correct runs into deadline_exceeded failures. At 128 tok/s the
	// 32768-token default yields 60s + 256s ≈ 316s, while the historical
	// 4096-token cap yielded 60s + 32s ≈ 92s — close to the old fixed 120s,
	// so small-cap configurations keep roughly today's behaviour.
	AssumedGenerationRate = 128

	// DefaultReviewMaxOutputTokens is the built-in review output cap. It is
	// sized for the schema's worst case — MaxCommentsCap findings, each with
	// title, body, impact, quoted evidence and an optional fix (~600 tokens
	// each), plus reasoning tokens billed against the same budget. The old
	// 4096 could not hold ten such findings, and large files failed whole
	// runs with "output truncated at token cap (finish_reason=length)".
	DefaultReviewMaxOutputTokens = 32768
)

// DerivedReviewTimeout returns the review-role deadline implied by an output
// cap of maxOutputTokens tokens. A cap that is unset (<= 0) falls back to
// DefaultReviewMaxOutputTokens. This is the default only: an explicit
// roles.review.timeout always wins over the derivation.
func DerivedReviewTimeout(maxOutputTokens int) time.Duration {
	if maxOutputTokens <= 0 {
		maxOutputTokens = DefaultReviewMaxOutputTokens
	}
	return ReviewTimeoutBase + time.Duration(maxOutputTokens)*time.Second/time.Duration(AssumedGenerationRate)
}

// RoleSpec is one entry of the roles block, before default resolution.
type RoleSpec struct {
	Model           string // "provider/modelid" when providers are declared
	Timeout         string // duration string, e.g. "90s"
	MaxOutputTokens int
	Concurrency     int
}

// Config is the parsed .github/cite.yml.
type Config struct {
	// The v1 surface: seven keys.
	Model         string
	MaxComments   int
	PathsIgnore   []string
	Nits          bool
	Gate          string
	CompatProfile string

	// RequireParameters asks OpenRouter-style routers to pick only endpoints
	// that support every request parameter (notably response_format
	// json_schema). Off by default; see docs/configuration.md.
	RequireParameters bool

	// BlockingCategories is the repository's shrinking of the default set
	// (§8): it may shrink from DefaultBlockingCategories() and may never
	// grow. convention can never block in any configuration.
	BlockingCategories []model.Category

	// The extended model block (§6).
	Providers map[string]*model.Provider
	Roles     map[model.Role]RoleSpec
	Fallback  []string // ordered model references, exercised by a canary
}

// DefaultBlockingCategories returns the categories eligible to block a merge
// out of the box. It is exactly the set for which Category.MayBlock() is true;
// configuration may shrink this set but never grow it, and convention is
// excluded permanently.
func DefaultBlockingCategories() []model.Category {
	return []model.Category{
		model.CategorySecretExposure,
		model.CategoryInjection,
		model.CategoryAuthBypass,
		model.CategoryDestructiveOp,
		model.CategoryCrash,
		model.CategoryLogicInversion,
	}
}

// Default returns the configuration a repository gets by writing nothing.
func Default() *Config {
	return &Config{
		Model:              DefaultModel,
		MaxComments:        DefaultMaxComments,
		Gate:               GateComment,
		CompatProfile:      DefaultCompatProfile,
		BlockingCategories: DefaultBlockingCategories(),
	}
}

// Load reads path and returns the parsed configuration. The file is optional:
// a missing or empty file yields Default(). Parsing is strict and validation
// aggregates every problem into a single typed error (*ValidationError).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, err
	}
	return Parse(data)
}

// Parse parses raw bytes as a Cite configuration.
func Parse(data []byte) (*Config, error) {
	tree, err := parseYAML(data)
	if err != nil {
		return nil, err
	}
	if tree == nil { // empty file, or only comments: sensible defaults
		return Default(), nil
	}

	var probs []Problem
	schemaCheck(tree, &probs)
	c := buildConfig(tree, &probs)
	c.validate(&probs)
	if len(probs) > 0 {
		return c, &ValidationError{Problems: probs}
	}
	return c, nil
}

// validate applies the semantic rules that the JSON Schema cross-check cannot
// express on its own (reference resolution, blocking-set monotonicity,
// credential expression shapes). Problems are appended, never returned early:
// the user fixes everything in one pass.
func (c *Config) validate(probs *[]Problem) {
	c.checkBudgetAndGate(probs)
	c.checkBlockingSet(probs)
	c.checkCredentialShapes(probs)
	c.checkModelRefs(probs)
	c.checkRoleSpecs(probs)
}

func (c *Config) checkBudgetAndGate(probs *[]Problem) {
	if c.MaxComments < 0 {
		addf(probs, "max_comments", "must not be negative")
	}
	if c.MaxComments > MaxCommentsCap {
		addf(probs, "max_comments", "%d exceeds the hard cap of %d enforced by the schema",
			c.MaxComments, MaxCommentsCap)
	}
	switch c.Gate {
	case GateComment, GateBlock:
	default:
		addf(probs, "gate", "%q must be %q or %q", c.Gate, GateComment, GateBlock)
	}
}

func (c *Config) checkBlockingSet(probs *[]Problem) {
	seen := map[model.Category]bool{}
	for _, cat := range c.BlockingCategories {
		if seen[cat] {
			addf(probs, "blocking_categories", "duplicate category %q", cat)
			continue
		}
		seen[cat] = true
		if !validCategory(cat) {
			addf(probs, "blocking_categories", "unknown category %q", cat)
			continue
		}
		if !cat.MayBlock() {
			addf(probs, "blocking_categories",
				"category %q may never block: the blocking set can only shrink from the default, and convention can never block", cat)
		}
	}
}

// validCategory reports whether s names an entry of the closed vocabulary.
func validCategory(s model.Category) bool {
	for _, d := range DefaultBlockingCategories() {
		if s == d {
			return true
		}
	}
	switch s {
	case model.CategoryResourceLeak, model.CategoryConcurrency,
		model.CategoryErrorSwallow, model.CategoryAPIContractBreak,
		model.CategoryConvention:
		return true
	}
	return false
}

// checkCredentialShapes validates api_key expressions against the four
// allowed shapes: literal | $VAR | ${VAR} | !command (§6).
func (c *Config) checkCredentialShapes(probs *[]Problem) {
	names := sortedProviderNames(c.Providers)
	for _, name := range names {
		p := c.Providers[name]
		if p.APIKey == "" {
			continue
		}
		if msg := credentialShapeProblem(string(p.APIKey)); msg != "" {
			addf(probs, "providers."+name+".api_key", "%s", msg)
		}
	}
}

func (c *Config) checkRoleSpecs(probs *[]Problem) {
	for _, role := range sortedRoles(c.Roles) {
		spec := c.Roles[role]
		path := "roles." + string(role)
		if spec.Timeout != "" {
			d, err := time.ParseDuration(spec.Timeout)
			if err != nil {
				addf(probs, path+".timeout", "%q is not a parseable duration: %v", spec.Timeout, err)
			} else if d <= 0 {
				addf(probs, path+".timeout", "must be positive, got %s", d)
			}
		}
		if spec.Concurrency < 0 {
			addf(probs, path+".concurrency", "must not be negative")
		}
		if spec.MaxOutputTokens < 0 {
			addf(probs, path+".max_output_tokens", "must not be negative")
		}
	}
}

// checkModelRefs resolves every model reference ("provider/id") against the
// declared providers. The top-level model key is a free-form name and is not
// resolved here. When no providers are declared, plain builtin model names
// are allowed; with providers declared, references must resolve.
func (c *Config) checkModelRefs(probs *[]Problem) {
	refs := map[string]string{} // path -> reference
	for _, role := range sortedRoles(c.Roles) {
		if m := c.Roles[role].Model; m != "" {
			refs["roles."+string(role)+".model"] = m
		}
	}
	for i, f := range c.Fallback {
		refs[fmt.Sprintf("fallback[%d]", i)] = f
	}

	for path, ref := range refs {
		if ref == "" {
			addf(probs, path, "model reference must not be empty")
			continue
		}
		provider, id, hasSlash := strings.Cut(ref, "/")
		if !hasSlash {
			if len(c.Providers) > 0 {
				addf(probs, path, "%q must be \"provider/modelid\" because providers are declared", ref)
			}
			continue
		}
		p, ok := c.Providers[provider]
		if !ok {
			addf(probs, path, "unknown provider %q in %q", provider, ref)
			continue
		}
		found := false
		for _, m := range p.Models {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			addf(probs, path, "provider %q declares no model with id %q", provider, id)
		}
	}
}

// Role resolves the effective settings for one of the three roles (review,
// triage, assemble). Defaults: review concurrency 8, triage timeout 120s
// (queueing + time-to-first-token headroom), assemble timeout 60s. The review timeout has no fixed default: it derives
// from the resolved output cap (60s base + tokens ÷ 128 tok/s, issue #28)
// unless an explicit roles.review.timeout is configured. An unset role model
// falls back to c.Model.
func (c *Config) Role(role model.Role) model.RoleConfig {
	rc := model.RoleConfig{}
	if spec, ok := c.Roles[role]; ok {
		rc.Model = spec.Model
		rc.MaxOutputTokens = spec.MaxOutputTokens
		rc.Concurrency = spec.Concurrency
		if spec.Timeout != "" {
			if d, err := time.ParseDuration(spec.Timeout); err == nil {
				rc.Timeout = d
				rc.TimeoutStr = spec.Timeout
			}
		}
	}
	if rc.Model == "" {
		rc.Model = c.Model
	}
	switch role {
	case model.RoleReview:
		if rc.Concurrency <= 0 {
			rc.Concurrency = DefaultReviewConcurrency
		}
		if rc.Timeout <= 0 {
			// Derive from the same resolved cap the call will carry (issue
			// #28). rc.MaxOutputTokens holds the roles.review.max_output_tokens
			// layer; fall back to the resolved model entry's max_tokens, then
			// to the built-in default inside DerivedReviewTimeout.
			tokens := rc.MaxOutputTokens
			if tokens <= 0 {
				tokens = c.modelEntryMaxTokens(rc.Model)
			}
			rc.Timeout = DerivedReviewTimeout(tokens)
			rc.TimeoutStr = fmt.Sprintf("%ds", int(rc.Timeout.Seconds()))
		}
	case model.RoleTriage:
		if rc.Timeout <= 0 {
			rc.Timeout = DefaultTriageTimeout
			rc.TimeoutStr = "120s"
		}
	case model.RoleAssemble:
		if rc.Timeout <= 0 {
			rc.Timeout = DefaultAssembleTimeout
			rc.TimeoutStr = "60s"
		}
	}
	return rc
}

// ModelMaxTokens returns the output cap advertised by the model a role
// resolves to, or 0 when no providers are declared or the entry omits
// max_tokens. docs/configuration.md calls max_tokens "default output cap for
// calls using this model"; this is where that promise is kept.
//
// Reference resolution matches checkModelRefs exactly — the FIRST '/'
// separates provider from model id, so "gateway/vendor/model-x" is provider
// "gateway", id "vendor/model-x". A reference that fails to resolve yields 0
// rather than an error: validation already rejects those, and a cap lookup
// must never be the thing that fails a run.
func (c *Config) ModelMaxTokens(role model.Role) int {
	if c == nil {
		return 0
	}
	return c.modelEntryMaxTokens(c.Role(role).Model)
}

// modelEntryMaxTokens resolves one "provider/id" reference against the
// declared providers and returns the entry's max_tokens, or 0 when providers
// are undeclared, the reference does not resolve, or the entry omits
// max_tokens. It takes an already-resolved model reference rather than a role
// so that Role() can consult it while resolving that very role without
// recursing.
func (c *Config) modelEntryMaxTokens(modelRef string) int {
	if c == nil || len(c.Providers) == 0 {
		return 0
	}
	provider, id, hasSlash := strings.Cut(modelRef, "/")
	if !hasSlash {
		return 0
	}
	p, ok := c.Providers[provider]
	if !ok || p == nil {
		return 0
	}
	for _, m := range p.Models {
		if m.ID == id {
			return m.MaxTokens
		}
	}
	return 0
}

func sortedProviderNames(m map[string]*model.Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRoles(m map[model.Role]RoleSpec) []model.Role {
	out := make([]model.Role, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
