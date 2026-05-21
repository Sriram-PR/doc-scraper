package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeAppConfig(siteKeys ...string) *AppConfig {
	sites := make(map[string]*SiteConfig, len(siteKeys))
	for _, key := range siteKeys {
		sites[key] = &SiteConfig{
			AllowedDomain: key + ".example.com",
			MaxDepth:      2,
		}
	}
	return &AppConfig{Sites: sites}
}

func TestValidateSiteKeys(t *testing.T) {
	t.Run("all valid", func(t *testing.T) {
		cfg := makeAppConfig("docs", "blog")
		err := ValidateSiteKeys(cfg, []string{"docs", "blog"})
		assert.NoError(t, err)
	})

	t.Run("one invalid", func(t *testing.T) {
		cfg := makeAppConfig("docs", "blog")
		err := ValidateSiteKeys(cfg, []string{"docs", "missing"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("empty keys no error", func(t *testing.T) {
		cfg := makeAppConfig("docs")
		err := ValidateSiteKeys(cfg, []string{})
		assert.NoError(t, err)
	})

	t.Run("empty config", func(t *testing.T) {
		cfg := makeAppConfig()
		err := ValidateSiteKeys(cfg, []string{"anything"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "anything")
	})

	t.Run("available list in error is sorted", func(t *testing.T) {
		// Verify the sorted-on-error contract: shuffle input order, expect output sorted.
		cfg := makeAppConfig("zeta", "alpha", "mu")
		err := ValidateSiteKeys(cfg, []string{"missing"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "[alpha mu zeta]")
	})
}

func TestGetAllSiteKeys(t *testing.T) {
	t.Run("multiple sites returned sorted", func(t *testing.T) {
		// Pass keys in a non-sorted order; result must come back sorted so that
		// callers (e.g. --all-sites crawl order, error messages) are deterministic.
		cfg := makeAppConfig("gamma", "alpha", "beta")
		keys := GetAllSiteKeys(cfg)
		assert.Equal(t, []string{"alpha", "beta", "gamma"}, keys)
	})

	t.Run("no sites", func(t *testing.T) {
		cfg := makeAppConfig()
		keys := GetAllSiteKeys(cfg)
		assert.Empty(t, keys)
	})

	t.Run("single site", func(t *testing.T) {
		cfg := makeAppConfig("only")
		keys := GetAllSiteKeys(cfg)
		assert.Equal(t, []string{"only"}, keys)
	})
}
