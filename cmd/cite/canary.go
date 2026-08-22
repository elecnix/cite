package main

// cite canary — exercises every leg of the provider/fallback chain (§6).
//
// An untested fallback is not a fallback but a second outage that begins at
// the same moment as the first. The canary sends one tiny bounded completion
// per leg and reports each result loudly; skipped legs are reported too, so
// an untestable credential is never silent.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

func init() {
	registerCommand("canary", runCanary)
}

type canaryLeg struct {
	name    string // display name: provider/model or bare model id
	baseURL string
	apiKey  model.CredentialExpr
	headers map[string]string
	modelID string
	primary bool
}

// canaryLegs builds the ordered, de-duplicated leg list from declared
// providers plus the fallback chain.
func canaryLegs(cfg *config.Config) []canaryLeg {
	var legs []canaryLeg
	seen := map[string]bool{}
	add := func(l canaryLeg) {
		if l.modelID == "" || seen[l.name] {
			return
		}
		seen[l.name] = true
		legs = append(legs, l)
	}
	providerNames := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		providerNames = append(providerNames, n)
	}
	sort.Strings(providerNames)
	for _, pn := range providerNames {
		p := cfg.Providers[pn]
		if len(p.Models) == 0 {
			continue
		}
		m := p.Models[0] // one leg per provider: its first declared model
		add(canaryLeg{
			name: pn + "/" + m.ID, baseURL: p.BaseURL,
			apiKey: p.APIKey, headers: p.Headers, modelID: m.ID,
			primary: len(legs) == 0,
		})
	}
	for _, ref := range cfg.Fallback {
		pn, mid := splitModelRef(ref)
		base, key := "", model.CredentialExpr("")
		var headers map[string]string
		if p, ok := cfg.Providers[pn]; ok && pn != "" {
			base, key, headers = p.BaseURL, p.APIKey, p.Headers
		} else {
			if pn != "" {
				// Unknown provider while providers are declared: config
				// validation rejects this earlier; skip defensively.
				continue
			}
			base = os.Getenv("MODEL_BASE_URL")
			if base == "" {
				base = "https://api.openai.com/v1"
			}
			key = "$MODEL_API_KEY"
		}
		add(canaryLeg{name: ref, baseURL: base, apiKey: key, headers: headers, modelID: mid})
	}
	return legs
}

// splitModelRef splits "provider/model"; a bare name returns ("", name).
func splitModelRef(ref string) (string, string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:]
		}
	}
	return "", ref
}

func runCanary(args []string) error {
	fs := flag.NewFlagSet("canary", flag.ContinueOnError)
	cfgPath := fs.String("config", ".github/cite.yml", "config file with providers/fallback")
	timeout := fs.Duration("timeout", 15*time.Second, "per-leg deadline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := loadConfig(*cfgPath)
	legs := canaryLegs(cfg)
	if len(legs) == 0 {
		fmt.Println("canary: no providers or fallback chain configured — nothing to exercise")
		return nil
	}

	exercised, failed, skipped := 0, 0, 0
	primaryFailed := false
	for _, leg := range legs {
		key, err := leg.apiKey.Resolve()
		if err != nil || string(key) == "" {
			skipped++
			fmt.Printf("%-40s SKIPPED (credential unavailable: %v)\n", leg.name, err)
			continue
		}
		c := &model.OpenAICompatClient{
			BaseURL: leg.baseURL, APIKey: string(key), ExtraHeaders: leg.headers, Model: leg.modelID,
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		resp, cerr := c.Complete(ctx, model.CompletionRequest{
			System: "ping", User: "reply pong", MaxOutputTokens: 16, Temperature: 0,
		})
		cancel()
		if cerr != nil || resp == nil || resp.Text == "" {
			failed++
			reason := "empty response"
			if cerr != nil {
				reason = cerr.Error()
			}
			fmt.Printf("%-40s FAIL   %s\n", leg.name, reason)
			if leg.primary {
				primaryFailed = true
			}
			continue
		}
		exercised++
		fmt.Printf("%-40s ok\n", leg.name)
	}

	fmt.Printf("\ncanary: %d leg(s): %d exercised, %d failed, %d skipped\n",
		len(legs), exercised, failed, skipped)
	if primaryFailed {
		return fmt.Errorf("canary: PRIMARY leg failed")
	}
	if failed > 0 {
		// Loudly noted, not fatal: a down fallback is reported so it can be
		// repaired before it is needed.
		fmt.Println("note: a fallback leg is down — an untested (or broken) fallback is not a fallback")
	}
	return nil
}
