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
