package main

// Operator settings (issue #217). Every knob a run's behaviour depends on has a
// flag, an environment variable and a configuration key, and they resolve by one
// ladder, strongest first:
//
//  1. the command-line flag;
//  2. the environment variable;
//  3. the configuration files, in the order given, later overriding earlier;
//  4. the built-in default.
//
// Environment sits above the files so CI and one-off `FOO=bar cite …` runs behave
// as they always have, even on a machine that has a configuration file.
//
// A setting that comes from no file, no variable and no flag is left zero here
// and the built-in default applies where the value is used, so "unset" and "set
// to the default" stay distinguishable all the way down.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// Environment variables. The GitHub Actions runtime's own GITHUB_* variables are
// out of scope: they describe the job, not the operator's choices.
const (
	configListEnvVar = "CITE_CONFIG"

	modelIDEnvVar        = "MODEL_ID"
	modelBaseURLEnvVar   = "MODEL_BASE_URL"
	modelAPIKeyEnvVar    = "MODEL_API_KEY"
	fallbackModelsEnvVar = "CITE_FALLBACK_MODELS"

	captureWireEnvVar   = "CITE_CAPTURE_WIRE"
	captureDirEnvVar    = "CITE_CAPTURE_DIR"
	captureMaxEnvVar    = "CITE_CAPTURE_MAX_BYTES"
	truncatedOutEnvVar  = "CITE_TRUNCATED_OUT"
	recordOutEnvVar     = "CITE_RECORD_OUT"
	structuredOutEnvVar = "CITE_STRUCTURED_OUTPUT"
	reasoningEffortEnv  = "CITE_REASONING_EFFORT"
	requireParamsEnvVar = "CITE_REQUIRE_PARAMETERS"
	verifyEnvVar        = "CITE_VERIFY"
	relatedCodeEnvVar   = "CITE_RELATED_CODE"
	debugEnvVar         = "CITE_DEBUG"
)

// defaultConfigPath is the repository file. It is read first, so it is the file
// a later one overrides.
const defaultConfigPath = ".github/cite.yml"

// userConfigPath is the machine-level file, read after the repository one when
// the operator names no file. On a runner it simply does not exist, so the
// GitHub Action's behaviour is what it was.
func userConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "cite", "config.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "cite", "config.yml")
}

// defaultConfigPaths is the list read when no --config and no CITE_CONFIG say
// otherwise: the repository file, then the machine file if it exists.
func defaultConfigPaths() []string {
	if p := userConfigPath(); p != "" {
		return []string{defaultConfigPath, p}
	}
	return []string{defaultConfigPath}
}

// flagGroup selects which operator flags a command registers. A command
// registers only the settings it can act on, so `cite validate -h` does not
// advertise knobs it ignores.
type flagGroup uint

const (
	groupConfig flagGroup = 1 << iota
	groupEndpoint
	groupOutput
	groupDiagnostics
	groupBehaviour
)

// configFlag is a repeatable --config. Passing it at all replaces the default
// list: a wrapper that names its files means exactly those, so nothing sneaks in
// from the home directory.
type configFlag struct {
	paths []string
}

func (c *configFlag) String() string { return strings.Join(c.paths, string(os.PathListSeparator)) }

func (c *configFlag) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("--config needs a file path")
	}
	c.paths = append(c.paths, v)
	return nil
}

// resolve returns the files to read, in order, and where the list came from.
func (c *configFlag) resolve() ([]string, string) {
	if len(c.paths) > 0 {
		return c.paths, "flag --config"
	}
	if v := strings.TrimSpace(os.Getenv(configListEnvVar)); v != "" {
		return filepath.SplitList(v), "env:" + configListEnvVar
	}
	return defaultConfigPaths(), "default"
}

// stringFlag is a string setting whose flag default is its environment
// variable, so -h shows the value in force, and whose explicit use is still
// distinguishable from that default.
type stringFlag struct {
	name  string
	value *string
	env   string
	// secret keeps the environment value out of the flag's displayed default:
	// -h prints a default, and a credential must never be printed. The value
	// still resolves from the variable, just without being shown.
	secret bool
}

func (s *stringFlag) register(fs *flag.FlagSet, name, usage, env string) {
	s.name, s.env = name, env
	def := strings.TrimSpace(os.Getenv(env))
	if s.secret {
		def = ""
	}
	s.value = fs.String(name, def, usage)
}

func (s *stringFlag) resolve(given map[string]bool, file string) (value, source string) {
	switch {
	case given[s.name]:
		return *s.value, "flag --" + s.name
	case strings.TrimSpace(os.Getenv(s.env)) != "":
		return os.Getenv(s.env), "env:" + s.env
	case file != "":
		return file, "config"
	}
	return "", "default"
}

// triBool is a boolean setting with three states: unset, true and false. Two
// flags write it — --x and --no-x — because Go's flag package has no negated
// boolean, and a file may set the value the command line wants to overturn.
type triBool struct {
	name    string
	env     string
	on, off *bool
	// strictTrue reads the variable the way a boolean variable has always
	// been read: only the true spellings are on, and any other non-empty value
	// is off. Every other boolean counts an unrecognised value as on, which is
	// how its variable behaved before the flag existed.
	strictTrue bool
}

func (t *triBool) register(fs *flag.FlagSet, name, usage, env string) {
	t.name, t.env = name, env
	t.on = fs.Bool(name, false, usage)
	t.off = fs.Bool("no-"+t.name, false, "turn --"+t.name+" off")
}

func (t *triBool) flagValue(given map[string]bool) (value, set bool) {
	switch {
	case given[t.name]:
		return *t.on, true
	case given["no-"+t.name]:
		return !*t.off, true
	}
	return false, false
}

// resolve applies the ladder to one boolean. offSpellings lists the values that
// mean off; every other non-empty value means on, so a variable that is present
// but misspelled still changes something the way it always did.
func (t *triBool) resolve(given map[string]bool, file *bool, def bool, offSpellings []string) (value bool, source string) {
	if v, set := t.flagValue(given); set {
		return v, "flag --" + t.name
	}
	if raw, ok := os.LookupEnv(t.env); ok && strings.TrimSpace(raw) != "" {
		if t.strictTrue {
			return envTrue(raw), "env:" + t.env
		}
		v := true
		for _, off := range offSpellings {
			if strings.EqualFold(strings.TrimSpace(raw), off) {
				v = false
				break
			}
		}
		return v, "env:" + t.env
	}
	if file != nil {
		return *file, "config"
	}
	return def, "default"
}

// operatorFlags is every operator setting this command can be told about.
type operatorFlags struct {
	config configFlag

	modelID        stringFlag
	modelBaseURL   stringFlag
	modelAPIKey    stringFlag
	fallbackModels stringFlag

	recordOut        stringFlag
	structuredOutput stringFlag
	reasoningEffort  stringFlag
	requireParams    triBool

	captureDir   stringFlag
	captureMax   stringFlag
	truncatedOut stringFlag
	captureWire  triBool
	verify       triBool
	relatedCode  triBool
	debug        triBool
}

// newOperatorFlags registers the flags of the requested groups on fs.
func newOperatorFlags(fs *flag.FlagSet, groups flagGroup) *operatorFlags {
	o := &operatorFlags{}
	if groups&groupConfig != 0 {
		fs.Var(&o.config, "config",
			"configuration file; repeatable, and later files override earlier ones (default "+defaultConfigPath+", then the machine file if it exists; $"+configListEnvVar+" carries the list)")
	}
	if groups&groupEndpoint != 0 {
		o.modelID.register(fs, "model-id", "the model id to send (defaults to $"+modelIDEnvVar+")", modelIDEnvVar)
		o.modelBaseURL.register(fs, "model-base-url", "the endpoint to call (defaults to $"+modelBaseURLEnvVar+")", modelBaseURLEnvVar)
		o.modelAPIKey.secret = true
		o.modelAPIKey.register(fs, "model-api-key", "a credential reference ($VAR or !command), or the key itself (defaults to $"+modelAPIKeyEnvVar+", never printed)", modelAPIKeyEnvVar)
		o.fallbackModels.register(fs, "fallback-models", "comma-separated model ids at the same endpoint (defaults to $"+fallbackModelsEnvVar+")", fallbackModelsEnvVar)
	}
	if groups&groupOutput != 0 {
		o.structuredOutput.register(fs, "structured-output", "how the model returns schema-shaped JSON: response_format (default) or tools (defaults to $"+structuredOutEnvVar+")", structuredOutEnvVar)
		o.reasoningEffort.register(fs, "reasoning-effort", "reasoning_effort sent to the provider; empty omits the field (defaults to $"+reasoningEffortEnv+")", reasoningEffortEnv)
		o.requireParams.strictTrue = true
		o.requireParams.register(fs, "require-parameters", "send the router-only provider.require_parameters field, so a router refuses endpoints that drop the request parameters (defaults to $"+requireParamsEnvVar+")", requireParamsEnvVar)
	}
	if groups&groupDiagnostics != 0 {
		o.debug.register(fs, "debug", "write request and response dumps to /tmp for a provider 400 or a bad body (defaults to $"+debugEnvVar+")", debugEnvVar)
		o.truncatedOut.register(fs, "truncated-out", "where to keep the raw response of a token-cap truncation (defaults to $"+truncatedOutEnvVar+")", truncatedOutEnvVar)
		o.captureWire.register(fs, "capture-wire", "write one masked document per model call (defaults to $"+captureWireEnvVar+")", captureWireEnvVar)
		o.captureDir.register(fs, "capture-dir", "directory for the wire capture (defaults to $"+captureDirEnvVar+")", captureDirEnvVar)
		o.captureMax.register(fs, "capture-max-bytes", "per-body ceiling for the wire capture (defaults to $"+captureMaxEnvVar+")", captureMaxEnvVar)
	}
	if groups&groupBehaviour != 0 {
		o.verify.register(fs, "verify", "run the verifier pass over each finding (defaults to $"+verifyEnvVar+")", verifyEnvVar)
		o.relatedCode.register(fs, "related-code", "carry the definitions a changed line uses and the call sites of what it changes (defaults to $"+relatedCodeEnvVar+")", relatedCodeEnvVar)
	}
	return o
}

// settings is the resolved answer: what this run will do, and where each choice
// came from.
type settings struct {
	Files []string

	ModelID        string
	ModelBaseURL   string
	ModelAPIKey    string
	FallbackModels string

	CaptureWire      bool
	CaptureDir       string
	CaptureMaxBytes  int
	TruncatedOut     string
	RecordOut        string
	StructuredOutput string
	ReasoningEffort  string
	RequireParams    bool
	Verify           bool
	RelatedCode      bool
	Debug            bool

	// Config is the merged configuration. It is nil only for the zero settings a
	// failed load produces, which review's check-run path carries as an error
	// rather than a run, so read it through cfg().
	Config *config.Loaded
	// Sources maps a setting key to where its value came from.
	Sources map[string]string
}

// resolve applies the ladder, loading the configuration files first because
// they are the weakest rung above the built-in defaults.
func (o *operatorFlags) resolve(given map[string]bool) (*settings, error) {
	s := &settings{Sources: map[string]string{}}

	paths, listSource := o.config.resolve()
	if listSource != "default" {
		s.Sources["config"] = listSource
	}
	loaded, err := config.LoadFiles(paths)
	if err != nil {
		return nil, err
	}
	s.Config = loaded
	s.Files = loaded.Read
	for _, p := range loaded.Read {
		logToStderr("config %s read", p)
	}
	for _, p := range loaded.Missing {
		logToStderr("config %s not found", p)
	}
	for _, o := range loaded.Overrides {
		logToStderr("config override: %s", o)
	}
	if len(loaded.Read) == 0 {
		logToStderr("no configuration file found; using built-in defaults")
	}
	cfg := loaded.Config

	var src string
	s.ModelID, src = o.modelID.resolve(given, cfg.ModelID)
	s.Sources["model_id"] = src
	s.ModelBaseURL, src = o.modelBaseURL.resolve(given, cfg.ModelBaseURL)
	s.Sources["model_base_url"] = src
	s.ModelAPIKey, src = o.modelAPIKey.resolve(given, cfg.ModelAPIKey)
	s.Sources["model_api_key"] = src
	s.FallbackModels, src = o.fallbackModels.resolve(given, strings.Join(cfg.FallbackModels, ","))
	s.Sources["fallback_models"] = src

	s.RecordOut, src = o.recordOut.resolve(given, cfg.RecordOut)
	s.Sources["record_out"] = src
	s.StructuredOutput, src = o.structuredOutput.resolve(given, cfg.StructuredOutput)
	s.Sources["structured_output"] = src
	s.ReasoningEffort, src = o.reasoningEffort.resolve(given, cfg.ReasoningEffort)
	s.Sources["reasoning_effort"] = src
	// The file's require_parameters key reaches the reviewer as
	// cfg.RequireParameters, which it ORs with this value: a repository that
	// sets the key keeps it when the action passes its own default-off input.
	s.RequireParams, src = o.requireParams.resolve(given, nil, false, nil)
	s.Sources["require_parameters"] = src

	s.CaptureWire, src = o.captureWire.resolve(given, cfg.CaptureWire, false, []string{"0", "false", "off", "no"})
	s.Sources["capture_wire"] = src
	s.CaptureDir, src = o.captureDir.resolve(given, cfg.CaptureDir)
	s.Sources["capture_dir"] = src
	// A value that is not a byte count falls through to the next rung rather
	// than to the default: a typo in a variable must not discard the ceiling the
	// configuration file states.
	raw, src := o.captureMax.resolve(given, "")
	if raw != "" {
		n, perr := strconv.Atoi(strings.TrimSpace(raw))
		if perr != nil {
			logToStderr("cite: capture_max_bytes=%q is not a byte count; falling back to the configuration file", raw)
			raw = ""
		} else {
			s.CaptureMaxBytes = n
		}
	}
	if raw == "" && cfg.CaptureMaxBytes != 0 {
		s.CaptureMaxBytes = cfg.CaptureMaxBytes
		src = "config"
	}
	s.Sources["capture_max_bytes"] = src
	s.TruncatedOut, src = o.truncatedOut.resolve(given, cfg.TruncatedOut)
	s.Sources["truncated_out"] = src

	s.Verify, src = o.verify.resolve(given, cfg.Verify, true, []string{"0", "false", "off", "no"})
	s.Sources["verify"] = src
	s.RelatedCode, src = o.relatedCode.resolve(given, cfg.RelatedCode, true, []string{"0", "false", "off", "no"})
	s.Sources["related_code"] = src
	s.Debug, src = o.debug.resolve(given, cfg.Debug, false, nil)
	s.Sources["debug"] = src

	if _, err := model.ParseStructuredOutputMode(s.StructuredOutput); err != nil {
		return nil, fmt.Errorf("structured_output (from %s): %w", s.Sources["structured_output"], err)
	}
	return s, nil
}

// cfg is the merged configuration, or nil when the load failed. Every reader
// goes through here, so no site has to guess whether Config is set.
func (s *settings) cfg() *config.Config {
	if s == nil || s.Config == nil {
		return nil
	}
	return s.Config.Config
}

// apply installs the two settings that live in the model package for the life of
// the process: the debug surface and the wire capture.
func (s *settings) apply() {
	model.SetDiagnostics(model.Diagnostics{Debug: s.Debug, TruncatedOut: s.TruncatedOut})
	model.SetActiveCapture(model.CaptureFromSettings(model.CaptureSettings{
		On:       s.CaptureWire,
		Dir:      s.CaptureDir,
		MaxBytes: s.CaptureMaxBytes,
	}))
}

// endpoint resolves the model key and builds the endpoint description the client
// takes. A reference is resolved here and nowhere else, so a key never travels
// further than it must.
func (s *settings) endpoint() (model.Endpoint, error) {
	ep := model.Endpoint{BaseURL: s.ModelBaseURL, ModelID: s.ModelID}
	key := s.ModelAPIKey
	if strings.HasPrefix(key, "$") || strings.HasPrefix(key, "!") {
		resolved, err := model.CredentialExpr(key).Resolve()
		if err != nil {
			return ep, fmt.Errorf("model_api_key (from %s): %w", s.Sources["model_api_key"], err)
		}
		key = resolved
	}
	ep.APIKey = key
	return ep, nil
}

// fallbackModelIDs splits the comma-separated list the flag and the variable
// carry. The file carries a sequence, which the string form already joined.
func (s *settings) fallbackModelIDs() []string {
	var out []string
	for _, id := range strings.Split(s.FallbackModels, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// envTrue reads a boolean environment value with the true spellings only, so a
// typo cannot turn a setting on by accident.
func envTrue(v string) bool {
	switch v {
	case "1", "true", "TRUE", "True":
		return true
	}
	return false
}

// givenFlags collects the flag names the command line actually set, which is
// what makes "flag not given" different from "flag given its default".
func givenFlags(fs *flag.FlagSet) map[string]bool {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	return given
}
