package config

import (
	"fmt"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// Validate checks AppConfig fields, applies sensible defaults, and returns warnings and any fatal error.
// Modifies receiver in place.
func (c *AppConfig) Validate() (warnings []string, err error) {
	if c.NumWorkers <= 0 {
		warnings = append(warnings, "num_workers should be > 0, defaulting to 4")
		c.NumWorkers = 4
	}

	if c.NumImageWorkers <= 0 {
		warnings = append(warnings, fmt.Sprintf(
			"num_image_workers not specified or invalid, defaulting to num_workers (%d)",
			c.NumWorkers))
		c.NumImageWorkers = c.NumWorkers
	}

	if c.MaxRequests <= 0 {
		warnings = append(warnings, "max_requests should be > 0, defaulting to 10")
		c.MaxRequests = 10
	}

	if c.MaxRequestsPerHost <= 0 {
		warnings = append(warnings, "max_requests_per_host should be > 0, defaulting to 2")
		c.MaxRequestsPerHost = 2
	}

	if c.OutputBaseDir == "" {
		warnings = append(warnings, "output_base_dir is empty, defaulting to './crawled_docs'")
		c.OutputBaseDir = "./crawled_docs"
	}

	if c.StateDir == "" {
		warnings = append(warnings, "state_dir is empty, defaulting to './crawler_state'")
		c.StateDir = "./crawler_state"
	}

	if c.MaxRetries < 0 {
		warnings = append(warnings, "max_retries cannot be negative, setting to 0")
		c.MaxRetries = 0
	}
	if c.MaxRetries == 0 && c.InitialRetryDelay == 0 {
		c.MaxRetries = 3
	}

	if c.MaxRetries > 0 {
		if c.InitialRetryDelay <= 0 {
			c.InitialRetryDelay = 1 * time.Second
		}
		if c.MaxRetryDelay <= 0 {
			c.MaxRetryDelay = 30 * time.Second
		}
	}

	if c.InitialRetryDelay > c.MaxRetryDelay && c.MaxRetryDelay > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"initial_retry_delay (%v) > max_retry_delay (%v), using max_retry_delay for initial",
			c.InitialRetryDelay, c.MaxRetryDelay))
		c.InitialRetryDelay = c.MaxRetryDelay
	}

	if c.GlobalCrawlTimeout < 0 {
		warnings = append(warnings, "global_crawl_timeout cannot be negative, disabling timeout")
		c.GlobalCrawlTimeout = 0
	}

	if c.PerPageTimeout < 0 {
		warnings = append(warnings, "per_page_timeout cannot be negative, disabling timeout")
		c.PerPageTimeout = 0
	}

	if c.MaxImageSizeBytes < 0 {
		warnings = append(warnings, "max_image_size_bytes cannot be negative, setting to 0 (unlimited)")
		c.MaxImageSizeBytes = 0
	}

	c.validateHTTPClientSettings()

	return warnings, nil
}

func (c *AppConfig) validateHTTPClientSettings() {
	h := &c.HTTPClientSettings
	if h.Timeout <= 0 {
		h.Timeout = 45 * time.Second
	}
	if h.MaxIdleConnsPerHost <= 0 {
		h.MaxIdleConnsPerHost = 2
	}
}

// Validate checks SiteConfig fields, applies defaults, and returns warnings and any fatal error.
// Modifies receiver in place (e.g., path prefix normalization).
func (c *SiteConfig) Validate() (warnings []string, err error) {
	if len(c.StartURLs) == 0 {
		return nil, fmt.Errorf("%w: site has no start_urls", utils.ErrConfigValidation)
	}

	if c.AllowedDomain == "" {
		return nil, fmt.Errorf("%w: site needs allowed_domain", utils.ErrConfigValidation)
	}

	if c.AllowedPathPrefix == "" {
		c.AllowedPathPrefix = "/"
	} else if c.AllowedPathPrefix[0] != '/' {
		c.AllowedPathPrefix = "/" + c.AllowedPathPrefix
	}

	if c.ContentSelector == "" {
		return nil, fmt.Errorf("%w: site needs content_selector", utils.ErrConfigValidation)
	}

	if c.MaxDepth < 0 {
		warnings = append(warnings, "Site MaxDepth cannot be negative, setting to 0 (unlimited)")
		c.MaxDepth = 0
	}

	if c.MaxImageSizeBytes != nil && *c.MaxImageSizeBytes < 0 {
		warnings = append(warnings, "Site MaxImageSizeBytes cannot be negative, setting to 0 (unlimited override)")
		zero := int64(0)
		c.MaxImageSizeBytes = &zero
	}

	return warnings, nil
}
