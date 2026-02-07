package config

import (
	"fmt"
	"time"

	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// Validate checks AppConfig fields and applies sensible defaults.
// Returns collected warnings and any fatal error.
// Modifies receiver in place to apply defaults.
func (c *AppConfig) Validate() (warnings []string, err error) {
	// NumWorkers
	if c.NumWorkers <= 0 {
		warnings = append(warnings, "num_workers should be > 0, defaulting to 4")
		c.NumWorkers = 4
	}

	// NumImageWorkers
	if c.NumImageWorkers <= 0 {
		warnings = append(warnings, fmt.Sprintf(
			"num_image_workers not specified or invalid, defaulting to num_workers (%d)",
			c.NumWorkers))
		c.NumImageWorkers = c.NumWorkers
	}

	// MaxRequests
	if c.MaxRequests <= 0 {
		warnings = append(warnings, "max_requests should be > 0, defaulting to 10")
		c.MaxRequests = 10
	}

	// MaxRequestsPerHost
	if c.MaxRequestsPerHost <= 0 {
		warnings = append(warnings, "max_requests_per_host should be > 0, defaulting to 2")
		c.MaxRequestsPerHost = 2
	}

	// OutputBaseDir
	if c.OutputBaseDir == "" {
		warnings = append(warnings, "output_base_dir is empty, defaulting to './crawled_docs'")
		c.OutputBaseDir = "./crawled_docs"
	}

	// StateDir
	if c.StateDir == "" {
		warnings = append(warnings, "state_dir is empty, defaulting to './crawler_state'")
		c.StateDir = "./crawler_state"
	}

	// MaxRetries
	if c.MaxRetries < 0 {
		warnings = append(warnings, "max_retries cannot be negative, setting to 0")
		c.MaxRetries = 0
	}
	if c.MaxRetries == 0 && c.InitialRetryDelay == 0 {
		c.MaxRetries = 3
	}

	// Retry delays (only if retries enabled)
	if c.MaxRetries > 0 {
		if c.InitialRetryDelay <= 0 {
			c.InitialRetryDelay = 1 * time.Second
		}
		if c.MaxRetryDelay <= 0 {
			c.MaxRetryDelay = 30 * time.Second
		}
	}

	// InitialRetryDelay > MaxRetryDelay check
	if c.InitialRetryDelay > c.MaxRetryDelay && c.MaxRetryDelay > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"initial_retry_delay (%v) > max_retry_delay (%v), using max_retry_delay for initial",
			c.InitialRetryDelay, c.MaxRetryDelay))
		c.InitialRetryDelay = c.MaxRetryDelay
	}

	// SemaphoreAcquireTimeout
	if c.SemaphoreAcquireTimeout <= 0 {
		c.SemaphoreAcquireTimeout = 30 * time.Second
	}

	// GlobalCrawlTimeout
	if c.GlobalCrawlTimeout < 0 {
		warnings = append(warnings, "global_crawl_timeout cannot be negative, disabling timeout")
		c.GlobalCrawlTimeout = 0
	}

	// PerPageTimeout
	if c.PerPageTimeout < 0 {
		warnings = append(warnings, "per_page_timeout cannot be negative, disabling timeout")
		c.PerPageTimeout = 0
	}

	// MaxImageSizeBytes
	if c.MaxImageSizeBytes < 0 {
		warnings = append(warnings, "max_image_size_bytes cannot be negative, setting to 0 (unlimited)")
		c.MaxImageSizeBytes = 0
	}

	// HTTPClientSettings defaults
	c.validateHTTPClientSettings()

	// Output mapping filename
	if c.EnableOutputMapping && c.OutputMappingFilename == "" {
		warnings = append(warnings,
			"Global 'enable_output_mapping' is true but 'output_mapping_filename' is empty. "+
				"Defaulting to 'url_to_file_map.tsv'")
		c.OutputMappingFilename = "url_to_file_map.tsv"
	}

	// Metadata YAML filename
	if c.EnableMetadataYAML && c.MetadataYAMLFilename == "" {
		warnings = append(warnings,
			"Global 'enable_metadata_yaml' is true but 'metadata_yaml_filename' is empty. "+
				"Defaulting to 'metadata.yaml'")
		c.MetadataYAMLFilename = "metadata.yaml"
	}

	return warnings, nil // AppConfig validation never fails fatally
}

// validateHTTPClientSettings applies defaults to HTTP client settings.
func (c *AppConfig) validateHTTPClientSettings() {
	h := &c.HTTPClientSettings
	if h.Timeout <= 0 {
		h.Timeout = 45 * time.Second
	}
	if h.MaxIdleConns <= 0 {
		h.MaxIdleConns = 100
	}
	if h.MaxIdleConnsPerHost <= 0 {
		h.MaxIdleConnsPerHost = 2
	}
	if h.IdleConnTimeout <= 0 {
		h.IdleConnTimeout = 90 * time.Second
	}
	if h.TLSHandshakeTimeout <= 0 {
		h.TLSHandshakeTimeout = 10 * time.Second
	}
	if h.ExpectContinueTimeout <= 0 {
		h.ExpectContinueTimeout = 1 * time.Second
	}
	if h.DialerTimeout <= 0 {
		h.DialerTimeout = 15 * time.Second
	}
	if h.DialerKeepAlive <= 0 {
		h.DialerKeepAlive = 30 * time.Second
	}
}

// Validate checks SiteConfig fields and applies defaults.
// Returns collected warnings and any fatal error.
// Modifies receiver in place (e.g., path prefix normalization).
func (c *SiteConfig) Validate() (warnings []string, err error) {
	// Required: StartURLs
	if len(c.StartURLs) == 0 {
		return nil, fmt.Errorf("%w: site has no start_urls", utils.ErrConfigValidation)
	}

	// Required: AllowedDomain
	if c.AllowedDomain == "" {
		return nil, fmt.Errorf("%w: site needs allowed_domain", utils.ErrConfigValidation)
	}

	// AllowedPathPrefix normalization
	if c.AllowedPathPrefix == "" {
		c.AllowedPathPrefix = "/"
	} else if c.AllowedPathPrefix[0] != '/' {
		c.AllowedPathPrefix = "/" + c.AllowedPathPrefix
	}

	// Required: ContentSelector
	if c.ContentSelector == "" {
		return nil, fmt.Errorf("%w: site needs content_selector", utils.ErrConfigValidation)
	}

	// MaxDepth
	if c.MaxDepth < 0 {
		warnings = append(warnings, "Site MaxDepth cannot be negative, setting to 0 (unlimited)")
		c.MaxDepth = 0
	}

	// MaxImageSizeBytes (pointer)
	if c.MaxImageSizeBytes != nil && *c.MaxImageSizeBytes < 0 {
		warnings = append(warnings, "Site MaxImageSizeBytes cannot be negative, setting to 0 (unlimited override)")
		zero := int64(0)
		c.MaxImageSizeBytes = &zero
	}

	return warnings, nil
}
