package storage

import (
	"context"
	"time"

	"doc-scraper/pkg/models"
)

// VisitedStore defines the interface for storing and retrieving visited status for pages and images
type VisitedStore interface {
	// MarkPageVisited marks a page URL as visited (pending state)
	// Returns true if the URL was newly added, false if it already existed
	MarkPageVisited(normalizedPageURL string) (bool, error)

	// CheckPageStatus retrieves the status and details of a page URL
	// Returns status ("success", "failure", "pending", "not_found", "db_error"), the PageDBEntry if found and parsed, and any error
	CheckPageStatus(normalizedPageURL string) (status string, entry *models.PageDBEntry, err error)

	// UpdatePageStatus updates the status and details for a page URL
	UpdatePageStatus(normalizedPageURL string, entry *models.PageDBEntry) error

	// CheckImageStatus retrieves the status and details of an image URL
	// Returns status ("success", "failure", "not_found", "db_error"), the ImageDBEntry if found and parsed, and any error
	CheckImageStatus(normalizedImgURL string) (status string, entry *models.ImageDBEntry, err error)

	// UpdateImageStatus updates the status and details for an image URL
	UpdateImageStatus(normalizedImgURL string, entry *models.ImageDBEntry) error

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
