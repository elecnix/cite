package config

import (
	"testing"

	"github.com/elecnix/cite/internal/model"
)

// TestSplitModelRef pins the one rule every model reference in Cite resolves
// by. The reference is split on its FIRST '/', so a gateway that carries a
// vendor-qualified model id keeps the whole id on the model side. Three
// separate copies of this rule existed; they agreed by hand, not by
// construction.
func TestSplitModelRef(t *testing.T) {
	cases := []struct {
		ref      string
		provider string
		id       string
		ok       bool
	}{
		{"gateway/vendor/model-x", "gateway", "vendor/model-x", true},
		{"gateway/cheap", "gateway", "cheap", true},
		{"gpt-5-mini", "", "gpt-5-mini", false},
		{"", "", "", false},
		{"/leading", "", "leading", true},
	}
	for _, c := range cases {
		provider, id, ok := SplitModelRef(c.ref)
		if provider != c.provider || id != c.id || ok != c.ok {
			t.Errorf("SplitModelRef(%q) = (%q, %q, %t), want (%q, %q, %t)",
				c.ref, provider, id, ok, c.provider, c.id, c.ok)
		}
	}
}

// TestProviderNamesIsSorted guards the ordering that makes any scan over the
// providers map reproducible. Go randomises map iteration per range statement,
// so an unsorted scan decides a value differently from one run to the next.
func TestProviderNamesIsSorted(t *testing.T) {
	c := &Config{Providers: map[string]*model.Provider{
		"zeta": {Name: "zeta"}, "alpha": {Name: "alpha"}, "mid": {Name: "mid"},
	}}
	for i := 0; i < 50; i++ {
		got := c.ProviderNames()
		want := []string{"alpha", "mid", "zeta"}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("ProviderNames() = %v, want %v", got, want)
			}
		}
	}
	var nilCfg *Config
	if nilCfg.ProviderNames() != nil {
		t.Errorf("ProviderNames() on a nil config = %v, want nil", nilCfg.ProviderNames())
	}
}
