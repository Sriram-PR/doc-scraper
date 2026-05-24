package config

import "time"

// SiteConfig holds configuration specific to a single website crawl.
type SiteConfig struct {
	StartURLs               []string      `yaml:"start_urls"`
	AllowedDomain           string        `yaml:"allowed_domain"`
	AllowedPathPrefix       string        `yaml:"allowed_path_prefix"`
	ContentSelector         string        `yaml:"content_selector"`
	LinkExtractionSelectors []string      `yaml:"link_extraction_selectors,omitempty"`
	DisallowedPathPatterns  []string      `yaml:"disallowed_path_patterns,omitempty"`
	RespectNofollow         bool          `yaml:"respect_nofollow,omitempty"`
	UserAgent               string        `yaml:"user_agent,omitempty"`
	DelayPerHost            time.Duration `yaml:"delay_per_host,omitempty"`
	// MaxDepth is the exclusive upper bound on crawl depth. Seed pages are
	// depth 0, so MaxDepth=2 fetches depth 0 and 1. 0 means unlimited.
	MaxDepth                int           `yaml:"max_depth"`
	SkipImages              *bool         `yaml:"skip_images,omitempty"`
	MaxImageSizeBytes       *int64        `yaml:"max_image_size_bytes,omitempty"`
	AllowedImageDomains     []string      `yaml:"allowed_image_domains,omitempty"`
	DisallowedImageDomains  []string      `yaml:"disallowed_image_domains,omitempty"`
	EnableJSONLOutput       *bool         `yaml:"enable_jsonl_output,omitempty"`
	JSONLOutputFilename     string        `yaml:"jsonl_output_filename,omitempty"`
}

// AppConfig holds the global application configuration.
type AppConfig struct {
	DefaultUserAgent    string                 `yaml:"default_user_agent"`
	DefaultDelayPerHost time.Duration          `yaml:"default_delay_per_host"`
	NumWorkers          int                    `yaml:"num_workers"`
	NumImageWorkers     int                    `yaml:"num_image_workers,omitempty"`
	MaxRequests         int                    `yaml:"max_requests"`
	MaxRequestsPerHost  int                    `yaml:"max_requests_per_host"`
	OutputBaseDir       string                 `yaml:"output_base_dir"`
	StateDir            string                 `yaml:"state_dir"`
	MaxRetries          int                    `yaml:"max_retries,omitempty"`
	InitialRetryDelay   time.Duration          `yaml:"initial_retry_delay,omitempty"`
	MaxRetryDelay       time.Duration          `yaml:"max_retry_delay,omitempty"`
	GlobalCrawlTimeout  time.Duration          `yaml:"global_crawl_timeout,omitempty"`
	PerPageTimeout      time.Duration          `yaml:"per_page_timeout,omitempty"`       // 0 = no timeout
	SkipImages          *bool                  `yaml:"skip_images,omitempty"`            // nil = skip (text-first default)
	MaxImageSizeBytes   int64                  `yaml:"max_image_size_bytes,omitempty"`
	MaxPageSizeBytes    int64                  `yaml:"max_page_size_bytes,omitempty"`    // 0 = 50 MB default
	HTTPClientSettings  HTTPClientConfig       `yaml:"http_client_settings,omitempty"`
	Sites               map[string]*SiteConfig `yaml:"sites"`
	EnableJSONLOutput   bool                   `yaml:"enable_jsonl_output,omitempty"`
	JSONLOutputFilename string                 `yaml:"jsonl_output_filename,omitempty"`
	EnableIncremental   bool                   `yaml:"enable_incremental,omitempty"`
	// CrawlHistoryRetention is the number of past crawls per site kept in the
	// SQLite history index (state_dir/index.db) that powers get_freshness and
	// diff_crawl. Anything older is pruned at write time. 0 means use the
	// default in pkg/storage/index (10).
	CrawlHistoryRetention int `yaml:"crawl_history_retention,omitempty"`
}

// HTTPClientConfig holds settings for the shared HTTP client. The Go-default
// connection-pool, dialer, and TLS timings are baked into pkg/fetch and not
// exposed here; only the three knobs that real users actually tune remain.
type HTTPClientConfig struct {
	Timeout             time.Duration `yaml:"timeout,omitempty"`                 // Overall request timeout (default 45s)
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host,omitempty"` // Max idle connections per host (default 2)
	// AllowPrivateNetworks disables the SSRF guard that blocks outbound
	// connections to private/loopback/link-local/CGNAT/multicast addresses.
	// Default false. Set to true only if you intentionally crawl internal
	// documentation servers reachable via private IPs.
	AllowPrivateNetworks bool `yaml:"allow_private_networks,omitempty"`
}

const (
	// DefaultMaxPageSizeBytes is the default maximum page body size (50 MB).
	DefaultMaxPageSizeBytes int64 = 50 * 1024 * 1024

	// DefaultSemaphoreAcquireTimeout is how long crawler/fetch/sitemap/image
	// code waits to acquire a global or per-host semaphore before giving up.
	// Previously exposed via the semaphore_acquire_timeout config key; that
	// key was an internal mechanic with no realistic reason to tune.
	DefaultSemaphoreAcquireTimeout = 30 * time.Second
)

// GetEffectiveMaxPageSize returns the configured max page size, falling back to DefaultMaxPageSizeBytes.
func GetEffectiveMaxPageSize(appCfg *AppConfig) int64 {
	if appCfg.MaxPageSizeBytes > 0 {
		return appCfg.MaxPageSizeBytes
	}
	return DefaultMaxPageSizeBytes
}

// GetEffectiveUserAgent returns the site-specific user agent, falling back to the global default.
func GetEffectiveUserAgent(siteCfg *SiteConfig, appCfg *AppConfig) string {
	if siteCfg.UserAgent != "" {
		return siteCfg.UserAgent
	}
	return appCfg.DefaultUserAgent
}

// GetEffectiveSkipImages returns the skip_images setting. Image downloading is opt-in: when
// neither the site nor the global config sets skip_images, images are skipped (text-first default).
func GetEffectiveSkipImages(siteCfg *SiteConfig, appCfg *AppConfig) bool {
	if siteCfg.SkipImages != nil {
		return *siteCfg.SkipImages
	}
	if appCfg.SkipImages != nil {
		return *appCfg.SkipImages
	}
	return true
}

// GetEffectiveMaxImageSize returns the effective max image size (site overrides global).
func GetEffectiveMaxImageSize(siteCfg *SiteConfig, appCfg *AppConfig) int64 {
	if siteCfg.MaxImageSizeBytes != nil {
		return *siteCfg.MaxImageSizeBytes
	}
	return appCfg.MaxImageSizeBytes
}

// GetEffectiveEnableJSONLOutput returns the effective JSONL output setting (site overrides global).
func GetEffectiveEnableJSONLOutput(siteCfg *SiteConfig, appCfg *AppConfig) bool {
	if siteCfg.EnableJSONLOutput != nil {
		return *siteCfg.EnableJSONLOutput
	}
	return appCfg.EnableJSONLOutput
}

// GetEffectiveJSONLOutputFilename returns the JSONL output filename (site > global > default).
func GetEffectiveJSONLOutputFilename(siteCfg *SiteConfig, appCfg *AppConfig) string {
	if siteCfg.JSONLOutputFilename != "" {
		return siteCfg.JSONLOutputFilename
	}
	if appCfg.JSONLOutputFilename != "" {
		return appCfg.JSONLOutputFilename
	}
	return "pages.jsonl"
}

func getEffectiveDelayPerHost(siteCfg *SiteConfig, appCfg *AppConfig) time.Duration {
	if siteCfg.DelayPerHost > 0 {
		return siteCfg.DelayPerHost
	}
	return appCfg.DefaultDelayPerHost
}

// ResolvedSiteConfig holds effective configuration for a site, resolved once at crawl start.
type ResolvedSiteConfig struct {
	UserAgent           string
	JSONLOutputFilename string
	DelayPerHost        time.Duration
	MaxPageSizeBytes    int64
	MaxImageSizeBytes   int64
	SkipImages          bool
	EnableJSONLOutput   bool
}

// NewResolvedSiteConfig resolves all effective configuration values for a site at crawl start.
func NewResolvedSiteConfig(siteCfg *SiteConfig, appCfg *AppConfig) *ResolvedSiteConfig {
	return &ResolvedSiteConfig{
		UserAgent:           GetEffectiveUserAgent(siteCfg, appCfg),
		JSONLOutputFilename: GetEffectiveJSONLOutputFilename(siteCfg, appCfg),
		DelayPerHost:        getEffectiveDelayPerHost(siteCfg, appCfg),
		MaxPageSizeBytes:    GetEffectiveMaxPageSize(appCfg),
		MaxImageSizeBytes:   GetEffectiveMaxImageSize(siteCfg, appCfg),
		SkipImages:          GetEffectiveSkipImages(siteCfg, appCfg),
		EnableJSONLOutput:   GetEffectiveEnableJSONLOutput(siteCfg, appCfg),
	}
}
