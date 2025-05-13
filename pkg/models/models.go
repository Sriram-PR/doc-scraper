package models

import "time"

// WorkItem represents a URL and its depth to be processed by a worker
type WorkItem struct {
	URL   string
	Depth int
}

// PageDBEntry stores the result of processing a page URL in the database
type PageDBEntry struct {
	Status      string    `json:"status"`                 // "success" or "failure"
	ErrorType   string    `json:"error_type,omitempty"`   // Error category (on failure)
	ProcessedAt time.Time `json:"processed_at,omitempty"` // Timestamp of successful processing
	LastAttempt time.Time `json:"last_attempt"`           // Timestamp of the last processing attempt
	Depth       int       `json:"depth"`                  // Depth at which this page was processed/attempted
}

// ImageDBEntry stores the result of processing an image URL in the database
type ImageDBEntry struct {
	Status      string    `json:"status"`               // "success" or "failure"
	LocalPath   string    `json:"local_path,omitempty"` // Relative path from site output dir (on success)
	Caption     string    `json:"caption,omitempty"`    // Captured caption/alt (on success)
	ErrorType   string    `json:"error_type,omitempty"` // Error category (on failure)
	LastAttempt time.Time `json:"last_attempt"`         // Timestamp of the last processing attempt
}

// ImageData stores information about a successfully downloaded image
type ImageData struct {
	OriginalURL string
	LocalPath   string // Relative path from site output dir
	Caption     string // Image caption/alt text
}

// CrawlMetadata holds all metadata for a single crawl session of a site.
type CrawlMetadata struct {
	SiteKey           string                 `yaml:"site_key"`
	AllowedDomain     string                 `yaml:"allowed_domain"`
	CrawlStartTime    time.Time              `yaml:"crawl_start_time"`
	CrawlEndTime      time.Time              `yaml:"crawl_end_time"`
	TotalPagesSaved   int                    `yaml:"total_pages_saved"`
	SiteConfiguration map[string]interface{} `yaml:"site_configuration,omitempty"` // For a flexible dump of SiteConfig
	Pages             []PageMetadata         `yaml:"pages"`
}

// PageMetadata holds metadata for a single scraped page.
type PageMetadata struct {
	OriginalURL   string    `yaml:"original_url"`
	NormalizedURL string    `yaml:"normalized_url"`
	LocalFilePath string    `yaml:"local_file_path"` // Relative to site_output_dir
	Title         string    `yaml:"title,omitempty"`
	Depth         int       `yaml:"depth"`
	ProcessedAt   time.Time `yaml:"processed_at"`
	ContentHash   string    `yaml:"content_hash,omitempty"` // MD5 or SHA256 hex string
	ImageCount    int       `yaml:"image_count,omitempty"`  // Count of images processed for this page
	// LinkedFrom    []string  `yaml:"linked_from,omitempty"` // Deferring for now
}
