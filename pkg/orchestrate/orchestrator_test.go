package orchestrate

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
)

func testAppConfig(siteKeys ...string) *config.AppConfig {
	sites := make(map[string]*config.SiteConfig, len(siteKeys))
	for _, key := range siteKeys {
		sites[key] = &config.SiteConfig{
			AllowedDomain: key + ".example.com",
			MaxDepth:      2,
		}
	}
	return &config.AppConfig{
		Sites: sites,
	}
}

func TestValidateSiteKeys(t *testing.T) {
	t.Run("all valid", func(t *testing.T) {
		cfg := testAppConfig("docs", "blog")
		err := ValidateSiteKeys(cfg, []string{"docs", "blog"})
		assert.NoError(t, err)
	})

	t.Run("one invalid", func(t *testing.T) {
		cfg := testAppConfig("docs", "blog")
		err := ValidateSiteKeys(cfg, []string{"docs", "missing"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("empty keys no error", func(t *testing.T) {
		cfg := testAppConfig("docs")
		err := ValidateSiteKeys(cfg, []string{})
		assert.NoError(t, err)
	})

	t.Run("empty config", func(t *testing.T) {
		cfg := testAppConfig()
		err := ValidateSiteKeys(cfg, []string{"anything"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "anything")
	})
}

func TestGetAllSiteKeys(t *testing.T) {
	t.Run("multiple sites", func(t *testing.T) {
		cfg := testAppConfig("alpha", "beta", "gamma")
		keys := GetAllSiteKeys(cfg)
		sort.Strings(keys)
		assert.Equal(t, []string{"alpha", "beta", "gamma"}, keys)
	})

	t.Run("no sites", func(t *testing.T) {
		cfg := testAppConfig()
		keys := GetAllSiteKeys(cfg)
		assert.Empty(t, keys)
	})

	t.Run("single site", func(t *testing.T) {
		cfg := testAppConfig("only")
		keys := GetAllSiteKeys(cfg)
		assert.Equal(t, []string{"only"}, keys)
	})
}
