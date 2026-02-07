package config

import (
	"strings"
	"testing"
	"time"

	"github.com/piratf/doc-scraper/pkg/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppConfig_Validate_Defaults(t *testing.T) {
	cfg := AppConfig{} // Zero value
	warnings, err := cfg.Validate()

	require.NoError(t, err)

	// Check defaults applied
	assert.Equal(t, 4, cfg.NumWorkers)
	assert.Equal(t, 4, cfg.NumImageWorkers)
	assert.Equal(t, 10, cfg.MaxRequests)
	assert.Equal(t, 2, cfg.MaxRequestsPerHost)
	assert.Equal(t, "./crawled_docs", cfg.OutputBaseDir)
	assert.Equal(t, "./crawler_state", cfg.StateDir)
	assert.Equal(t, 3, cfg.MaxRetries)
	assert.Equal(t, 1*time.Second, cfg.InitialRetryDelay)
	assert.Equal(t, 30*time.Second, cfg.MaxRetryDelay)
	assert.Equal(t, 30*time.Second, cfg.SemaphoreAcquireTimeout)

	// Check HTTP client defaults
	assert.Equal(t, 45*time.Second, cfg.HTTPClientSettings.Timeout)
	assert.Equal(t, 100, cfg.HTTPClientSettings.MaxIdleConns)
	assert.Equal(t, 2, cfg.HTTPClientSettings.MaxIdleConnsPerHost)
	assert.Equal(t, 90*time.Second, cfg.HTTPClientSettings.IdleConnTimeout)
	assert.Equal(t, 10*time.Second, cfg.HTTPClientSettings.TLSHandshakeTimeout)
	assert.Equal(t, 1*time.Second, cfg.HTTPClientSettings.ExpectContinueTimeout)
	assert.Equal(t, 15*time.Second, cfg.HTTPClientSettings.DialerTimeout)
	assert.Equal(t, 30*time.Second, cfg.HTTPClientSettings.DialerKeepAlive)

	// Check warnings generated
	assert.True(t, containsWarning(warnings, "num_workers should be > 0"))
	assert.True(t, containsWarning(warnings, "num_image_workers not specified"))
	assert.True(t, containsWarning(warnings, "max_requests should be > 0"))
	assert.True(t, containsWarning(warnings, "max_requests_per_host should be > 0"))
	assert.True(t, containsWarning(warnings, "output_base_dir is empty"))
	assert.True(t, containsWarning(warnings, "state_dir is empty"))
}

func TestAppConfig_Validate_ValidConfig(t *testing.T) {
	cfg := AppConfig{
		NumWorkers:         8,
		NumImageWorkers:    4,
		MaxRequests:        100,
		MaxRequestsPerHost: 10,
		OutputBaseDir:      "/output",
		StateDir:           "/state",
		MaxRetries:         5,
		InitialRetryDelay:  2 * time.Second,
		MaxRetryDelay:      60 * time.Second,
		HTTPClientSettings: HTTPClientConfig{
			Timeout:      30 * time.Second,
			MaxIdleConns: 50,
		},
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	// No warnings for valid numeric fields
	assert.False(t, containsWarning(warnings, "num_workers"))
	assert.False(t, containsWarning(warnings, "max_requests should"))
	assert.False(t, containsWarning(warnings, "output_base_dir"))
	assert.False(t, containsWarning(warnings, "state_dir"))

	// Values should be preserved
	assert.Equal(t, 8, cfg.NumWorkers)
	assert.Equal(t, 4, cfg.NumImageWorkers)
	assert.Equal(t, "/output", cfg.OutputBaseDir)
}

func TestAppConfig_Validate_NegativeValues(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*AppConfig)
		wantWarning string
		check       func(*testing.T, *AppConfig)
	}{
		{
			name: "negative max_retries",
			setup: func(c *AppConfig) {
				c.MaxRetries = -1
				c.InitialRetryDelay = 1 * time.Second // Prevent default of 3 retries
				c.NumWorkers = 1
				c.MaxRequests = 1
				c.MaxRequestsPerHost = 1
				c.OutputBaseDir = "/out"
				c.StateDir = "/state"
			},
			wantWarning: "max_retries cannot be negative",
			check: func(t *testing.T, c *AppConfig) {
				assert.Equal(t, 0, c.MaxRetries)
			},
		},
		{
			name: "negative global_crawl_timeout",
			setup: func(c *AppConfig) {
				c.GlobalCrawlTimeout = -1 * time.Second
				c.NumWorkers = 1
				c.MaxRequests = 1
				c.MaxRequestsPerHost = 1
				c.OutputBaseDir = "/out"
				c.StateDir = "/state"
			},
			wantWarning: "global_crawl_timeout cannot be negative",
			check: func(t *testing.T, c *AppConfig) {
				assert.Equal(t, time.Duration(0), c.GlobalCrawlTimeout)
			},
		},
		{
			name: "negative per_page_timeout",
			setup: func(c *AppConfig) {
				c.PerPageTimeout = -1 * time.Second
				c.NumWorkers = 1
				c.MaxRequests = 1
				c.MaxRequestsPerHost = 1
				c.OutputBaseDir = "/out"
				c.StateDir = "/state"
			},
			wantWarning: "per_page_timeout cannot be negative",
			check: func(t *testing.T, c *AppConfig) {
				assert.Equal(t, time.Duration(0), c.PerPageTimeout)
			},
		},
		{
			name: "negative max_image_size_bytes",
			setup: func(c *AppConfig) {
				c.MaxImageSizeBytes = -1
				c.NumWorkers = 1
				c.MaxRequests = 1
				c.MaxRequestsPerHost = 1
				c.OutputBaseDir = "/out"
				c.StateDir = "/state"
			},
			wantWarning: "max_image_size_bytes cannot be negative",
			check: func(t *testing.T, c *AppConfig) {
				assert.Equal(t, int64(0), c.MaxImageSizeBytes)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AppConfig{}
			tt.setup(&cfg)

			warnings, err := cfg.Validate()

			require.NoError(t, err)
			assert.True(t, containsWarning(warnings, tt.wantWarning),
				"expected warning containing %q, got %v", tt.wantWarning, warnings)
			tt.check(t, &cfg)
		})
	}
}

func TestAppConfig_Validate_RetryDelayInversion(t *testing.T) {
	cfg := AppConfig{
		NumWorkers:         1,
		MaxRequests:        1,
		MaxRequestsPerHost: 1,
		OutputBaseDir:      "/out",
		StateDir:           "/state",
		MaxRetries:         3,
		InitialRetryDelay:  60 * time.Second, // Greater than max
		MaxRetryDelay:      10 * time.Second,
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	assert.True(t, containsWarning(warnings, "initial_retry_delay"))
	assert.Equal(t, 10*time.Second, cfg.InitialRetryDelay) // Should be clamped
}

func TestAppConfig_Validate_OutputMappingFilename(t *testing.T) {
	cfg := AppConfig{
		NumWorkers:          1,
		MaxRequests:         1,
		MaxRequestsPerHost:  1,
		OutputBaseDir:       "/out",
		StateDir:            "/state",
		EnableOutputMapping: true,
		// OutputMappingFilename is empty
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	assert.True(t, containsWarning(warnings, "enable_output_mapping"))
	assert.Equal(t, "url_to_file_map.tsv", cfg.OutputMappingFilename)
}

func TestAppConfig_Validate_MetadataYAMLFilename(t *testing.T) {
	cfg := AppConfig{
		NumWorkers:         1,
		MaxRequests:        1,
		MaxRequestsPerHost: 1,
		OutputBaseDir:      "/out",
		StateDir:           "/state",
		EnableMetadataYAML: true,
		// MetadataYAMLFilename is empty
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	assert.True(t, containsWarning(warnings, "enable_metadata_yaml"))
	assert.Equal(t, "metadata.yaml", cfg.MetadataYAMLFilename)
}

func TestSiteConfig_Validate_RequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SiteConfig
		wantErr string
	}{
		{
			name:    "missing start_urls",
			cfg:     SiteConfig{},
			wantErr: "no start_urls",
		},
		{
			name: "missing allowed_domain",
			cfg: SiteConfig{
				StartURLs: []string{"http://example.com"},
			},
			wantErr: "needs allowed_domain",
		},
		{
			name: "missing content_selector",
			cfg: SiteConfig{
				StartURLs:     []string{"http://example.com"},
				AllowedDomain: "example.com",
			},
			wantErr: "needs content_selector",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.cfg.Validate()

			require.Error(t, err)
			assert.ErrorIs(t, err, utils.ErrConfigValidation)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSiteConfig_Validate_PathPrefixNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "/"},
		{"docs", "/docs"},
		{"/docs", "/docs"},
		{"api/v1", "/api/v1"},
		{"/api/v1", "/api/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cfg := SiteConfig{
				StartURLs:         []string{"http://example.com"},
				AllowedDomain:     "example.com",
				AllowedPathPrefix: tt.input,
				ContentSelector:   "main",
			}

			warnings, err := cfg.Validate()

			require.NoError(t, err)
			assert.Empty(t, warnings)
			assert.Equal(t, tt.expected, cfg.AllowedPathPrefix)
		})
	}
}

func TestSiteConfig_Validate_NegativeMaxDepth(t *testing.T) {
	cfg := SiteConfig{
		StartURLs:       []string{"http://example.com"},
		AllowedDomain:   "example.com",
		ContentSelector: "main",
		MaxDepth:        -5,
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	assert.True(t, containsWarning(warnings, "MaxDepth cannot be negative"))
	assert.Equal(t, 0, cfg.MaxDepth)
}

func TestSiteConfig_Validate_NegativeMaxImageSizeBytes(t *testing.T) {
	negativeSize := int64(-100)
	cfg := SiteConfig{
		StartURLs:         []string{"http://example.com"},
		AllowedDomain:     "example.com",
		ContentSelector:   "main",
		MaxImageSizeBytes: &negativeSize,
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	assert.True(t, containsWarning(warnings, "MaxImageSizeBytes cannot be negative"))
	assert.Equal(t, int64(0), *cfg.MaxImageSizeBytes)
}

func TestSiteConfig_Validate_ValidConfig(t *testing.T) {
	cfg := SiteConfig{
		StartURLs:       []string{"http://example.com", "http://example.com/docs"},
		AllowedDomain:   "example.com",
		ContentSelector: "article.content",
		MaxDepth:        10,
	}

	warnings, err := cfg.Validate()

	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, "/", cfg.AllowedPathPrefix) // Default applied
}

// containsWarning checks if any warning contains the substring.
func containsWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
