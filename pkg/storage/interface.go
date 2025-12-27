package storage

import (
	"context"
	"time"

	"doc-scraper/pkg/models"
)

// PageStore handles page visitation state
type PageStore interface {
	// MarkPageVisited marks a page URL as visited (pending state)
	// Returns true if the URL was newly added, false if it already existed
	MarkPageVisited(normalizedPageURL string) (bool, error)

	// CheckPageStatus retrieves the status and details of a page URL
	// Returns status (PageStatusSuccess, PageStatusFailure, PageStatusPending, PageStatusNotFound, PageStatusDBError),
	// the PageDBEntry if found and parsed, and any error
	CheckPageStatus(normalizedPageURL string) (status models.PageStatus, entry *models.PageDBEntry, err error)

	// UpdatePageStatus updates the status and details for a page URL
	UpdatePageStatus(normalizedPageURL string, entry *models.PageDBEntry) error

	// GetPageContentHash retrieves the content hash for a previously crawled page
	// Returns the hash string, whether it exists, and any error
	GetPageContentHash(normalizedPageURL string) (hash string, exists bool, err error)
}

// ImageStore handles image processing state
type ImageStore interface {
	// CheckImageStatus retrieves the status and details of an image URL
	// Returns status (ImageStatusSuccess, ImageStatusFailure, ImageStatusNotFound, ImageStatusDBError),
	// the ImageDBEntry if found and parsed, and any error
	CheckImageStatus(normalizedImgURL string) (status models.ImageStatus, entry *models.ImageDBEntry, err error)

	// UpdateImageStatus updates the status and details for an image URL
	UpdateImageStatus(normalizedImgURL string, entry *models.ImageDBEntry) error
}

// StoreAdmin handles lifecycle and administrative operations
type StoreAdmin interface {
	// GetVisitedCount returns an approximate count of all keys in the store
	GetVisitedCount() (int, error)

	// RequeueIncomplete scans the DB and sends incomplete items (failed, pending, empty) to the provided channel
	// Should be called only during resume
	RequeueIncomplete(ctx context.Context, workChan chan<- models.WorkItem) (requeuedCount int, scanErrors int, err error)

	// WriteVisitedLog writes all page and image keys (URLs) to the specified file path
	WriteVisitedLog(filePath string) error

	// RunGC runs periodic garbage collection. Should be run in a goroutine
	RunGC(ctx context.Context, interval time.Duration)

	// Close cleanly closes the database connection
	Close() error
}

// VisitedStore combines all store interfaces for components that need full access
type VisitedStore interface {
	PageStore
	ImageStore
	StoreAdmin
}
