package main

import (
	"os"
	"strings"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// newModelClient is the client every review path calls: the primary model
// from MODEL_*, then the fallback chain. CITE_FALLBACK_MODELS (the action's
// fallback_model_ids input) names models at the primary's own endpoint,
// tried first; the config's fallback references follow. A leg whose key
// does not resolve is left out with a warning, never silently.
func newModelClient(cfg *config.Config) (model.Client, error) {
	primary, err := model.NewOpenAICompatClient()
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
	for _, id := range strings.Split(os.Getenv("CITE_FALLBACK_MODELS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			leg := *primary
			leg.Model = id
			addLeg(&leg)
		}
	}
	if cfg != nil {
		for _, l := range fallbackLegs(cfg) {
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
