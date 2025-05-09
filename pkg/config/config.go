package config

import "time"

// SiteConfig holds configuration specific to a single website crawl
type SiteConfig struct {
	StartURLs               []string      `yaml:"start_urls"`
	AllowedDomain           string        `yaml:"allowed_domain"`
	AllowedPathPrefix       string        `yaml:"allowed_path_prefix"`
	ContentSelector         string        `yaml:"content_selector"`
	LinkExtractionSelectors []string      `yaml:"link_extraction_selectors,omitempty"`
	DisallowedPathPatterns  []string      `yaml:"disallowed_path_patterns,omitempty"` // Regex patterns for paths to exclude
	RespectNofollow         bool          `yaml:"respect_nofollow,omitempty"`
	UserAgent               string        `yaml:"user_agent,omitempty"`
	DelayPerHost            time.Duration `yaml:"delay_per_host,omitempty"`
	MaxDepth                int           `yaml:"max_depth"`
	SkipImages              *bool         `yaml:"skip_images,omitempty"`
	MaxImageSizeBytes       *int64        `yaml:"max_image_size_bytes,omitempty"`
	AllowedImageDomains     []string      `yaml:"allowed_image_domains,omitempty"`
	DisallowedImageDomains  []string      `yaml:"disallowed_image_domains,omitempty"`
}

// AppConfig holds the global application configuration
type AppConfig struct {
	DefaultUserAgent        string                `yaml:"default_user_agent"`
	DefaultDelayPerHost     time.Duration         `yaml:"default_delay_per_host"`
	NumWorkers              int                   `yaml:"num_workers"`
	NumImageWorkers         int                   `yaml:"num_image_workers,omitempty"`
	MaxRequests             int                   `yaml:"max_requests"`
	MaxRequestsPerHost      int                   `yaml:"max_requests_per_host"`
	OutputBaseDir           string                `yaml:"output_base_dir"`
	StateDir                string                `yaml:"state_dir"`
	MaxRetries              int                   `yaml:"max_retries,omitempty"`
	InitialRetryDelay       time.Duration         `yaml:"initial_retry_delay,omitempty"`
	MaxRetryDelay           time.Duration         `yaml:"max_retry_delay,omitempty"`
	SemaphoreAcquireTimeout time.Duration         `yaml:"semaphore_acquire_timeout,omitempty"`
	GlobalCrawlTimeout      time.Duration         `yaml:"global_crawl_timeout,omitempty"`
	SkipImages              bool                  `yaml:"skip_images,omitempty"`
	MaxImageSizeBytes       int64                 `yaml:"max_image_size_bytes,omitempty"`
	HTTPClientSettings      HTTPClientConfig      `yaml:"http_client_settings,omitempty"`
	Sites                   map[string]SiteConfig `yaml:"sites"`
}

// HTTPClientConfig holds settings for the shared HTTP client
type HTTPClientConfig struct {
	Timeout               time.Duration `yaml:"timeout,omitempty"`                 // Overall request timeout
	MaxIdleConns          int           `yaml:"max_idle_conns,omitempty"`          // Max total idle connections
	MaxIdleConnsPerHost   int           `yaml:"max_idle_conns_per_host,omitempty"` // Max idle connections per host
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout,omitempty"`       // Timeout for idle connections
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout,omitempty"`   // Timeout for TLS handshake
	ExpectContinueTimeout time.Duration `yaml:"expect_continue_timeout,omitempty"` // Timeout for 100-continue
	ForceAttemptHTTP2     *bool         `yaml:"force_attempt_http2,omitempty"`     // Explicitly enable/disable HTTP/2 attempt (use pointer for tri-state: nil=default, true=force, false=disable)
	DialerTimeout         time.Duration `yaml:"dialer_timeout,omitempty"`          // Connection dial timeout
	DialerKeepAlive       time.Duration `yaml:"dialer_keep_alive,omitempty"`       // TCP keep-alive interval
}

// GetEffectiveSkipImages determines the effective skip setting
func GetEffectiveSkipImages(siteCfg SiteConfig, appCfg AppConfig) bool {
	if siteCfg.SkipImages != nil {
		return *siteCfg.SkipImages
	}
	return appCfg.SkipImages
}

// GetEffectiveMaxImageSize determines the effective max image size
func GetEffectiveMaxImageSize(siteCfg SiteConfig, appCfg AppConfig) int64 {
	if siteCfg.MaxImageSizeBytes != nil {
		return *siteCfg.MaxImageSizeBytes
	}
	return appCfg.MaxImageSizeBytes
}
