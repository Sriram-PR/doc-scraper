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

// JSONL record types. The page-level JSONL output file carries both per-page
// records and a single crawl-level summary; the record_type field lets a
// consumer tell them apart while streaming the file line by line.
const (
	RecordTypePage      = "page"
	RecordTypeCrawlMeta = "crawl_meta"
)

// PageJSONL represents a single crawled page in the JSONL output (RAG pipeline
// ingestion). RecordType is always RecordTypePage.
type PageJSONL struct {
	RecordType  string   `json:"record_type"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Headings    []string `json:"headings"`
	Links       []string `json:"links"`
	Images      []string `json:"images"`
	ContentHash string   `json:"content_hash"`
	CrawledAt   string   `json:"crawled_at"`
	Depth       int      `json:"depth"`
}

// CrawlMetaJSONL is the crawl-level summary record. Exactly one is appended as
// the final line of the JSONL file when a crawl finishes. A resumed crawl
// appends a fresh record rather than rewriting the original, so consumers
// should treat the last crawl_meta record in the file as authoritative.
// RecordType is always RecordTypeCrawlMeta.
type CrawlMetaJSONL struct {
	RecordType     string `json:"record_type"`
	SiteKey        string `json:"site_key"`
	AllowedDomain  string `json:"allowed_domain"`
	CrawlStartedAt string `json:"crawl_started_at"`
	CrawlEndedAt   string `json:"crawl_ended_at"`
	TotalPages     int    `json:"total_pages"`
}

