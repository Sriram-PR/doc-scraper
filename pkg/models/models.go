package models

import "time"

// WorkItem represents a URL and its depth to be processed by a worker
type WorkItem struct {
	URL   string
	Depth int
}

// PageDBEntry stores the result of processing a page URL in the database
type PageDBEntry struct {
	Status      PageStatus `json:"status"`                 // Processing status (success, failure, pending)
	ErrorType   string     `json:"error_type,omitempty"`   // Error category (on failure)
	ProcessedAt time.Time  `json:"processed_at,omitempty"` // Timestamp of successful processing
	LastAttempt time.Time  `json:"last_attempt"`           // Timestamp of the last processing attempt
	Depth       int        `json:"depth"`                  // Depth at which this page was processed/attempted
	ContentHash string     `json:"content_hash,omitempty"` // Content hash for incremental crawling
}

// ImageDBEntry stores the result of processing an image URL in the database
type ImageDBEntry struct {
	Status      ImageStatus `json:"status"`               // Processing status (success, failure, skipped)
	LocalPath   string      `json:"local_path,omitempty"` // Relative path from site output dir (on success)
	Caption     string      `json:"caption,omitempty"`    // Captured caption/alt (on success)
	ErrorType   string      `json:"error_type,omitempty"` // Error category (on failure)
	LastAttempt time.Time   `json:"last_attempt"`         // Timestamp of the last processing attempt
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
	ContentHash   string    `yaml:"content_hash,omitempty"` // SHA-256 hex string
	ImageCount    int       `yaml:"image_count,omitempty"`  // Count of images processed for this page
	TokenCount    int       `yaml:"token_count,omitempty"`  // Token count for LLM context planning
	// LinkedFrom    []string  `yaml:"linked_from,omitempty"` // Deferring for now
}

// PageJSONL represents a single page for JSONL output (RAG pipeline ingestion).
type PageJSONL struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Headings    []string `json:"headings"`
	Links       []string `json:"links"`
	Images      []string `json:"images"`
	ContentHash string   `json:"content_hash"`
	CrawledAt   string   `json:"crawled_at"`
	Depth       int      `json:"depth"`
	TokenCount  int      `json:"token_count,omitempty"`
}

// ChunkJSONL represents a single chunk for JSONL output (RAG vector ingestion).
type ChunkJSONL struct {
	URL              string   `json:"url"`                // Source page URL
	ChunkIndex       int      `json:"chunk_index"`        // Index of this chunk within the page
	Content          string   `json:"content"`            // Chunk content (includes heading context)
	HeadingHierarchy []string `json:"heading_hierarchy"`  // Extracted heading hierarchy
	TokenCount       int      `json:"token_count"`        // Token count for this chunk
	PageTitle        string   `json:"page_title"`         // Title of the source page
	CrawledAt        string   `json:"crawled_at"`         // Timestamp of crawl
}
