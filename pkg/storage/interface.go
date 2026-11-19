package storage

import (
	"context"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// PageStore handles page visitation state
type PageStore interface {
	// MarkPageVisited marks a page as visited (pending) under its key, recording
	// the URL to fetch and the depth it was enqueued at. Returns true if the key
	// was newly added.
	MarkPageVisited(normalizedPageURL, fetchURL string, depth int) (bool, error)

	// CheckPageStatus retrieves the status and details of a page URL
	// Returns status (PageStatusSuccess, PageStatusFailure, PageStatusPending, PageStatusNotFound, PageStatusDBError),
	// the PageDBEntry if found and parsed, and any error
	CheckPageStatus(normalizedPageURL string) (status models.PageStatus, entry *models.PageDBEntry, err error)

	UpdatePageStatus(normalizedPageURL string, entry *models.PageDBEntry) error

	// GetPageContentHash returns the content hash stored for a page, whatever its
	// status: a failed page keeps the hash of its last good copy
	GetPageContentHash(normalizedPageURL string) (hash string, exists bool, err error)

	// DeletePage removes a page's entry; deleting a missing entry is not an error.
	DeletePage(normalizedPageURL string) error
}

// ImageStore handles image processing state
type ImageStore interface {
	// CheckImageStatus retrieves the status and details of an image URL
	// Returns status (ImageStatusSuccess, ImageStatusFailure, ImageStatusNotFound, ImageStatusDBError),
	// the ImageDBEntry if found and parsed, and any error
	CheckImageStatus(normalizedImgURL string) (status models.ImageStatus, entry *models.ImageDBEntry, err error)

	UpdateImageStatus(normalizedImgURL string, entry *models.ImageDBEntry) error
}

// StoreAdmin handles lifecycle and administrative operations
type StoreAdmin interface {
	// GetVisitedCount returns an approximate count of all keys in the store
	GetVisitedCount() (int, error)

	// RequeueIncomplete scans the DB and sends incomplete items (failed, pending, empty) to the provided channel.
	// When includeSuccess is true (incremental crawls), successfully-processed pages are also requeued so they can
	// be re-fetched and re-checked for content changes. Should be called only during resume.
	RequeueIncomplete(ctx context.Context, workChan chan<- models.WorkItem, includeSuccess bool) (requeuedCount int, scanErrors int, err error)

	// CanonicalizeKeys moves every page entry whose key canon changes to the
	// canonical key, keeping the old key as its fetch URL; when both exist the
	// better entry stays. Returns how many old keys were removed.
	CanonicalizeKeys(canon func(string) string) (moved int, err error)

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
