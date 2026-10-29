package main

import (
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// newModelClient is the client every review path calls: the primary model, then
// the fallback chain. --fallback-models (the action's fallback_model_ids input)
// names models at the primary's own endpoint, tried first; the configuration's
// fallback references follow. A leg whose key does not resolve is left out with a
// warning, never silently.
func newModelClient(st *settings) (model.Client, error) {
	ep, err := st.endpoint()
	if err != nil {
		return nil, err
	}
	primary, err := model.NewOpenAICompatClient(ep)
	if err != nil {
		return nil, err
	}
	var legs []model.Client
	seen := map[string]bool{primary.BaseURL + "\x00" + primary.Model: true}
	addLeg := func(c *model.OpenAICompatClient) {
		k := c.BaseURL + "\x00" + c.Model
		if c.Model == "" || seen[k] {
			return
		}
		seen[k] = true
		legs = append(legs, c)
	}
	for _, id := range st.fallbackModelIDs() {
		leg := *primary
		leg.Model = id
		addLeg(&leg)
	}
	if cfg := st.cfg(); cfg != nil {
		for _, l := range fallbackLegs(cfg, st) {
			key, kerr := l.apiKey.Resolve()
			if kerr != nil || string(key) == "" {
				logToStderr("warning: fallback %s left out: its credential does not resolve", l.name)
				continue
			}
			addLeg(&model.OpenAICompatClient{
				BaseURL: strings.TrimSuffix(l.baseURL, "/"), APIKey: string(key), ExtraHeaders: l.headers, Model: l.modelID,
			})
		}
	}
	if len(legs) > 0 {
		names := make([]string, len(legs))
		for i, l := range legs {
			names[i] = l.ModelID()
		}
		logToStderr("fallback chain after %s: %s", primary.ModelID(), strings.Join(names, ", "))
	}
	return model.NewFailoverClient(primary, legs, logToStderr), nil
}

// fallbackLegs resolves the configuration's fallback chain, in order: a
// qualified reference takes its provider's endpoint, key and headers; a bare
// model id takes the resolved primary endpoint and credential reference. The
// canary pings these legs and the review path walks them.
//
// A nil cfg is "no configuration file", which is a real state and means no
// configured chain: the two call sites reach cfg differently (the canary takes
// it from the settings it resolved, the review path asks st.cfg()), so the rule
// lives here instead of in each of them. st is never nil: it is what resolve
// returned.
func fallbackLegs(cfg *config.Config, st *settings) []canaryLeg {
	if cfg == nil {
		return nil
	}
	var legs []canaryLeg
	for _, ref := range cfg.Fallback {
		if l, ok := legFor(cfg, st, ref); ok {
			legs = append(legs, l)
		}
	}
	return legs
}

// legFor resolves one model reference to the endpoint that serves it: a
// qualified reference takes its provider's base URL, key and headers, and a
// bare id takes the resolved primary endpoint and credential reference. It
// reports false for an unknown provider while providers are declared; config
// validation rejects that earlier, so this is defensive.
func legFor(cfg *config.Config, st *settings, ref string) (canaryLeg, bool) {
	pn, mid, qualified := config.SplitModelRef(ref)
	base := st.ModelBaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	key := model.CredentialExpr(st.ModelAPIKey)
	var headers map[string]string
	if p, ok := cfg.Providers[pn]; ok && qualified {
		base, key, headers = p.BaseURL, p.APIKey, p.Headers
	} else if qualified {
		return canaryLeg{}, false
	}
	return canaryLeg{name: ref, baseURL: base, apiKey: key, headers: headers, modelID: mid}, true
}
