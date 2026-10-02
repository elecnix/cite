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
// raising the cap to 32768 then (and 131072 now) allowed responses eight
// times longer — then thirty-two times — to finish, and they died at
// "context deadline exceeded" — surfacing as COULD_NOT_EVALUATE — before
// emitting a verdict.
//
// Triage and assemble keep fixed timeouts. History: 30s assumed fast hosted
// models; 120s still died in dogfooding CI — openrouter-hosted models queue
// for minutes before the first byte, and each deadline expiry used to cost a
// paid retry on top of the wait (retries after a deadline are now terminal:
// see Reviewer.completeWithRetry). The defaults now sit at 15 minutes: a
// wall-clock cap is a safety net for a hung call, not a tuning knob, and a
// hung call is rare while a slow-but-correct one is common. A run that
// genuinely exceeds these has its failure archived for forensics by the
// GitHub Action.
const (
	DefaultTriageTimeout     = 15 * time.Minute
	DefaultAssembleTimeout   = 15 * time.Minute
	DefaultReviewConcurrency = 8

	// DefaultReviewTimeout floors the derived review deadline: whatever the
	// token arithmetic below says, no default review call dies before 15
	// minutes. Small-cap configurations finish long before this anyway — the
	// floor only matters when a call is genuinely stuck.
	DefaultReviewTimeout = 15 * time.Minute
)

// Review-deadline calibration (issue #28). When no explicit
// roles.review.timeout is configured, the review deadline is derived from the
// same resolved output cap the call is bounded by:
//
//	deadline = max(ReviewTimeoutBase + maxOutputTokens / AssumedGenerationRate,
//	               DefaultReviewTimeout)
//
// The numbers must stay visible together because they are coupled: raise the
// token budget and the wall-clock budget moves with it. The floor keeps the
// historical failure mode — a correct-but-slow call killed by an aggressive
// deadline, then charged again for the retry — from returning.
const (
	// ReviewTimeoutBase covers the part of the call that does not scale with
	// output length: prompt upload, provider queueing and network overhead.
	ReviewTimeoutBase = 60 * time.Second

	// AssumedGenerationRate is the output throughput, in tokens per second,
	// assumed when sizing the deadline. It is deliberately conservative —
	// sized for a mid-tier hosted model, not a top-tier endpoint. A faster
	// provider simply finishes early; a deadline sized on a fast rate turns
	// slow-but-correct runs into deadline_exceeded failures. At 128 tok/s the
	// 131072-token default yields 60s + 1024s ≈ 1084s ≈ 18 minutes, already
	// past the DefaultReviewTimeout floor — the derivation only clamps for
	// caps at or below ~107000 tokens.
	AssumedGenerationRate = 128

	// DefaultReviewMaxOutputTokens is the built-in review output cap. It is
	// sized for the schema's worst case — MaxCommentsCap findings, each with
	// title, body, impact, quoted evidence and an optional fix (~600 tokens
	// each), plus reasoning tokens billed against the same budget. The old
	// 4096 could not hold ten such findings, and large files failed whole
	// runs with "output truncated at token cap (finish_reason=length)".
	// Raising it from 32768 to 131072 stopped reviews of big scenario-style
	// files from truncating at the cap: cite's own dogfood hit
	// finish_reason=length at the 32768 default on a large single-file diff
	// and pinned 65536 in .github/cite.yml, and a roughly 100 KB briefing
	// file still ran out of room at the old default. 131072 gives the worst
	// case four times the room it needs, and the derived review deadline
	// moves with it.
	DefaultReviewMaxOutputTokens = 131072
)

// DerivedReviewTimeout returns the review-role deadline implied by an output
// cap of maxOutputTokens tokens, floored at DefaultReviewTimeout. A cap that
// is unset (<= 0) falls back to DefaultReviewMaxOutputTokens. This is the
// default only: an explicit roles.review.timeout always wins over the
// derivation.
func DerivedReviewTimeout(maxOutputTokens int) time.Duration {
	if maxOutputTokens <= 0 {
		maxOutputTokens = DefaultReviewMaxOutputTokens
	}
	derived := ReviewTimeoutBase + time.Duration(maxOutputTokens)*time.Second/time.Duration(AssumedGenerationRate)
	if derived < DefaultReviewTimeout {
		return DefaultReviewTimeout
	}
	return derived
}

// TimeoutAdvisories returns one informational line per role whose explicitly
// configured timeout is SHORTER than the default that would apply if the pin
// were removed (triage and assemble: their fixed defaults; review: the
// derivation for the resolved output cap, floored like the real default).
// Timeouts predate the 15-minute defaults and terminal deadlines; many
// installs pinned a value that was generous then and is strict now. A pin is
// honoured — never overridden — so the advisory is the operator's only
// signal. (Unparsable pins cannot reach this check: Load rejects them.)
func (c *Config) TimeoutAdvisories() []string {
	if c == nil {
		return nil
	}
	// A slice, not a map: advisory order must be deterministic, and Go map
	// iteration is not (the head reviewer flagged exactly this).
	fixed := []struct {
		role model.Role
		def  time.Duration
	}{
		{model.RoleTriage, DefaultTriageTimeout},
		{model.RoleAssemble, DefaultAssembleTimeout},
	}
	var out []string
	for _, pair := range fixed {
		spec, ok := c.Roles[pair.role]
		if !ok || spec.Timeout == "" {
			continue
		}
		d, err := time.ParseDuration(spec.Timeout)
		if err != nil || d <= 0 {
			continue
		}
		if d < pair.def {
			out = append(out, fmt.Sprintf("roles.%s.timeout is pinned at %s, shorter than the %s default — the pin may be stricter than necessary; deadline expiry is never retried, so a slow-but-correct call dies at the pin (remove the pin or raise it)", pair.role, spec.Timeout, pair.def))
		}
	}
	if spec, ok := c.Roles[model.RoleReview]; ok && spec.Timeout != "" {
		if d, err := time.ParseDuration(spec.Timeout); err == nil && d > 0 {
			tokens := spec.MaxOutputTokens
			if tokens <= 0 {
				tokens = c.modelEntryMaxTokens(c.Role(model.RoleReview).Model)
			}
			if def := DerivedReviewTimeout(tokens); d < def {
				out = append(out, fmt.Sprintf("roles.review.timeout is pinned at %s, shorter than the derived %s default for the resolved output cap — the pin may be stricter than necessary; deadline expiry is never retried, so a slow-but-correct call dies at the pin (remove the pin or raise it)", spec.Timeout, def))
			}
		}
	}
	return out
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
		Gate:               "", // reserved key (issue #86): accepted, selects nothing
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
	// "" (unset) and both documented values are accepted. Issue #86: the
	// key is reserved on the decision path — FOUND always concludes failure
	// regardless of the value — so validation admits the historical surface
	// rather than rejecting configs that set it, and never implies a mode
	// that selects anything.
	switch c.Gate {
	case "", GateComment, GateBlock:
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

// SplitModelRef splits a "provider/id" model reference into its provider and
// its model id. The FIRST '/' is the separator, so "gateway/vendor/model-x" is
// provider "gateway" and model "vendor/model-x". A reference carrying no '/'
// is a bare model name: it comes back whole as the id, with an empty provider
// and ok false, so a caller that wants the model name has it rather than the
// empty string.
//
// Every reference Cite resolves goes through here: the output cap in
// modelEntryMaxTokens, the validation in checkModelRefs, and the canary's
// fallback legs. They once each carried their own copy of the rule and agreed
// only by hand.
func SplitModelRef(ref string) (provider, id string, ok bool) {
	provider, id, ok = strings.Cut(ref, "/")
	if !ok {
		return "", ref, false
	}
	return provider, id, true
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
		provider, id, hasSlash := SplitModelRef(ref)
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

// RoleDefaults carries the built-in values applied when the configuration
// specifies nothing. They are arguments rather than package constants
// because the built-in output cap is stated by the caller that owns the
// role: internal/reviewer sizes the review cap at
// DefaultReviewMaxOutputTokens and the triage cap at 8192, and a caller that
// states none gets no cap rather than a wrong one.
type RoleDefaults struct {
	Timeout     time.Duration
	Concurrency int
	MaxTokens   int
}

// resolveRole is the ONE ladder that resolves a role's model, deadline,
// output cap and concurrency. Every layer, most specific first:
//
//  1. roles.<role>.<field> — an explicit operator instruction. It wins
//     outright: silently shrinking a number someone wrote down would be the
//     kind of invisible behaviour change §6 forbids.
//  2. the resolved model entry's max_tokens, for the OUTPUT CAP only — what
//     the model says it can emit. This both raises the cap on a roomy model
//     and lowers it on a narrow one, which is the only way Cite can know a
//     ceiling it cannot query.
//  3. the caller's built-in default.
//
// The review deadline is the one field with no fixed default: it derives
// from the SAME resolved cap (issue #28) via DerivedReviewTimeout, so a
// larger token budget buys proportionally more wall clock instead of dying
// at "context deadline exceeded". Triage and assemble keep the fixed
// deadline in def.Timeout; they emit bounded output regardless of file
// size. An unset role model falls back to c.Model.
func (c *Config) resolveRole(role model.Role, def RoleDefaults) model.RoleConfig {
	if c == nil {
		// An absent configuration is the default configuration: that is
		// exactly what Load returns for a missing file, and it keeps the nil
		// guard on ModelMaxTokens from resolving to a different answer than
		// a written-out one.
		return Default().resolveRole(role, def)
	}
	rc := model.RoleConfig{MaxOutputTokens: def.MaxTokens}
	spec, hasSpec := c.Roles[role]
	if hasSpec {
		rc.Concurrency = spec.Concurrency
		if spec.MaxOutputTokens > 0 {
			rc.MaxOutputTokens = spec.MaxOutputTokens
		}
		if spec.Timeout != "" {
			// A pin that does not parse to a positive duration is not a
			// deadline; Load rejects it before a run starts.
			if d, err := time.ParseDuration(spec.Timeout); err == nil && d > 0 {
				rc.Timeout = d
				rc.TimeoutStr = spec.Timeout
			}
		}
	}
	rc.Model = c.roleModel(role)
	if rc.Concurrency <= 0 {
		rc.Concurrency = def.Concurrency
	}
	// Layer 2 applies only when the operator did not pin the cap themselves:
	// an explicit number outranks what the model advertises, in either
	// direction.
	if !hasSpec || spec.MaxOutputTokens <= 0 {
		if n := c.modelEntryMaxTokens(rc.Model); n > 0 {
			rc.MaxOutputTokens = n
		}
	}
	if rc.Timeout <= 0 {
		if role == model.RoleReview {
			rc.Timeout = DerivedReviewTimeout(rc.MaxOutputTokens)
		} else {
			rc.Timeout = def.Timeout
		}
		rc.TimeoutStr = fmt.Sprintf("%ds", int(rc.Timeout.Seconds()))
	}
	return rc
}

// roleModel resolves the model reference for one role: the role's own model
// when it declares one, else the top-level model.
func (c *Config) roleModel(role model.Role) string {
	if c == nil {
		return ""
	}
	if spec, ok := c.Roles[role]; ok && spec.Model != "" {
		return spec.Model
	}
	return c.Model
}

// Role resolves the effective settings for one of the three roles (review,
// triage, assemble), stating the built-in defaults this package owns:
// review concurrency DefaultReviewConcurrency, the review output cap
// DefaultReviewMaxOutputTokens, and the triage and assemble timeouts of 15
// minutes. The review timeout has no fixed default: it derives from the
// resolved output cap (ReviewTimeoutBase + tokens ÷ AssumedGenerationRate,
// floored at DefaultReviewTimeout, issue #28) unless an explicit
// roles.review.timeout is configured.
//
// It is the same ladder RoleSettings runs, with the package defaults; the
// reviewer calls RoleSettings with the built-in caps it sizes for itself.
// One ladder, two entry points.
func (c *Config) Role(role model.Role) model.RoleConfig {
	def := RoleDefaults{Timeout: configDefaultRoleTimeout(role)}
	if role == model.RoleReview {
		def.Concurrency = DefaultReviewConcurrency
		def.MaxTokens = DefaultReviewMaxOutputTokens
	}
	return c.resolveRole(role, def)
}

// configDefaultRoleTimeout is the fixed deadline default for the two roles
// that have one; the review role returns 0 because its deadline derives from
// the output cap instead.
func configDefaultRoleTimeout(role model.Role) time.Duration {
	switch role {
	case model.RoleTriage:
		return DefaultTriageTimeout
	case model.RoleAssemble:
		return DefaultAssembleTimeout
	}
	return 0
}

// RoleSettings resolves the same ladder as Role with the caller's built-in
// defaults supplied explicitly, for a call site whose built-in output cap is
// not this package's to state. Every field it returns is the effective one:
// the deadline the call carries, the cap it is bounded by, the concurrency
// the wave runs at.
func (c *Config) RoleSettings(role model.Role, def RoleDefaults) model.RoleConfig {
	return c.resolveRole(role, def)
}

// RoleTimeoutExplicit reports whether the configuration pins a literal,
// parsable, positive roles.<role>.timeout. Callers that must word an error
// differently for a pinned and a derived deadline ask here instead of
// re-parsing the timeout string themselves.
func (c *Config) RoleTimeoutExplicit(role model.Role) bool {
	if c == nil {
		return false
	}
	spec, ok := c.Roles[role]
	if !ok || spec.Timeout == "" {
		return false
	}
	d, err := time.ParseDuration(spec.Timeout)
	return err == nil && d > 0
}

// ModelMaxTokens returns the output cap advertised by the model a role
// resolves to, or 0 when no providers are declared or the entry omits
// max_tokens. docs/configuration.md calls max_tokens "default output cap for
// calls using this model"; this is where that promise is kept.
//
// Reference resolution matches checkModelRefs exactly, because both call
// SplitModelRef: the FIRST '/' separates provider from model id, so
// "gateway/vendor/model-x" is provider "gateway", id "vendor/model-x". A
// reference that fails to resolve yields 0 rather than an error: validation
// already rejects those, and a cap lookup must never be the thing that fails a
// run.
func (c *Config) ModelMaxTokens(role model.Role) int {
	if c == nil {
		return 0
	}
	return c.modelEntryMaxTokens(c.roleModel(role))
}

// DeadlineFormula renders the derived-deadline arithmetic for operator-facing
// prose, naming capKey as the configuration key that drives it. It exists so
// the constants are quoted once: a sentence that transcribes "60s +
// tokens/128" into a string literal starts lying the moment either constant
// moves, and no test catches a stale number the way a test catches a changed
// formula.
func DeadlineFormula(capKey string) string {
	return fmt.Sprintf("%ds + %s/%d tok/s, floored at %s",
		int(ReviewTimeoutBase/time.Second), capKey, AssumedGenerationRate, DefaultReviewTimeout)
}

// ReviewTimeoutFormula is DeadlineFormula for the review role's cap key.
func ReviewTimeoutFormula() string {
	return DeadlineFormula("roles.review.max_output_tokens")
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
	provider, id, hasSlash := SplitModelRef(modelRef)
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

// ProviderNames returns the declared provider names in sorted order. The map
// itself has no order, and any scan over it that settles a value — a cost rate,
// a leg to exercise — would otherwise make that value depend on Go's randomised
// map iteration.
func (c *Config) ProviderNames() []string {
	if c == nil {
		return nil
	}
	return sortedProviderNames(c.Providers)
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
