package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/log"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

const (
	pageKeyPrefix  = "page:"      // Prefix for page URL keys in DB
	imageKeyPrefix = "img:"       // Prefix for image URL keys in DB
	visitedDBDir   = "visited_db" // Subdirectory name within stateDir for Badger DB files
)

// BadgerStore implements the VisitedStore interface using BadgerDB
type BadgerStore struct {
	db       *badger.DB
	log      *logrus.Entry
	ctx      context.Context // Parent context
	keyCount atomic.Int64    // Cached key count for O(1) GetVisitedCount
}

// NewBadgerStore initializes and returns a new BadgerStore
func NewBadgerStore(ctx context.Context, stateDir, siteDomain string, resume bool, logger *logrus.Entry) (*BadgerStore, error) {
	store := &BadgerStore{
		log: logger,
		ctx: ctx,
	}

	// Create a unique directory path for this site's DB within the base state directory
	dbDirName := utils.SanitizeFilename(siteDomain) + "_" + visitedDBDir // Use sanitize func
	dbPath := filepath.Join(stateDir, dbDirName)

	if !resume {
		logger.Warnf("Resume flag is false. REMOVING existing state directory: %s", dbPath)
		if err := os.RemoveAll(dbPath); err != nil {
			// Log error but attempt to continue; Badger might recover or create new files
			logger.Errorf("Failed to remove existing state directory %s: %v", dbPath, err)
		}
	}

	logger.Infof("Initializing visited URL database at: %s (Resume: %v)", dbPath, resume)

	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return nil, fmt.Errorf("cannot create state directory %s: %w", dbPath, err)
	}

	// Configure Badger options
	badgerLogger := log.NewBadgerLogrusAdapter(logger.WithField("component", "badgerdb"))
	opts := badger.DefaultOptions(dbPath).
		WithLogger(badgerLogger). // Use custom logrus adapter
		WithNumVersionsToKeep(1)  // Only keep the latest state (visited or not)

	// Open the database
	var err error
	store.db, err = badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger database at %s: %w", dbPath, err)
	}

	// Initialize key count from existing data (matters for resume mode)
	if resume {
		count, err := store.countKeys()
		if err != nil {
			logger.Warnf("Failed to count existing keys on resume: %v", err)
		} else {
			store.keyCount.Store(int64(count))
			logger.Infof("Loaded existing key count on resume: %d", count)
		}
	}

	logger.Info("Visited URL database initialized successfully.")
	return store, nil
}

// countKeys performs a one-time full key scan (used only during initialization on resume).
func (s *BadgerStore) countKeys() (int, error) {
	count := 0
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			count++
		}
		return nil
	})
	return count, err
}

const maxConflictRetries = 10

// dbUpdate wraps db.Update with a retry loop for BadgerDB transaction conflicts.
// Concurrent MVCC transactions on overlapping keys can return badger.ErrConflict;
// these resolve in microseconds, so a tight retry loop is sufficient.
func (s *BadgerStore) dbUpdate(fn func(txn *badger.Txn) error) error {
	for i := range maxConflictRetries {
		err := s.db.Update(fn)
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
		s.log.Debugf("BadgerDB transaction conflict (attempt %d/%d), retrying", i+1, maxConflictRetries)
	}
	return fmt.Errorf("%w: transaction conflict not resolved after %d retries", utils.ErrDatabase, maxConflictRetries)
}

// MarkPageVisited implements the VisitedStore interface
func (s *BadgerStore) MarkPageVisited(normalizedPageURL string) (bool, error) {
	if s.db == nil {
		return false, errors.New("visitedDB not initialized")
	}
	added := false
	key := []byte(pageKeyPrefix + normalizedPageURL)

	err := s.dbUpdate(func(txn *badger.Txn) error {
		_, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			// Key doesn't exist, add it with an empty value.
			e := badger.NewEntry(key, []byte{})
			errSet := txn.SetEntry(e)
			if errSet == nil {
				added = true
			}
			return errSet
		}
		// Key already exists or another error occurred
		return errGet // Return the original error (could be nil if key exists)
	})

	if err != nil {
		s.log.WithField("key", string(key)).Errorf("DB Update error in MarkPageVisited: %v", err)
		return false, fmt.Errorf("%w: marking page key '%s': %w", utils.ErrDatabase, string(key), err)
	}
	if added {
		s.keyCount.Add(1)
	}

	return added, nil
}

// CheckPageStatus implements the VisitedStore interface
func (s *BadgerStore) CheckPageStatus(normalizedPageURL string) (models.PageStatus, *models.PageDBEntry, error) {
	status := models.PageStatusNotFound
	var entry *models.PageDBEntry = nil
	key := []byte(pageKeyPrefix + normalizedPageURL)

	errView := s.db.View(func(txn *badger.Txn) error {
		item, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			status = models.PageStatusNotFound // Explicitly set status
			return nil                         // Key not found is not an error for this function's purpose
		}
		if errGet != nil {
			return fmt.Errorf("%w: failed getting page key '%s': %w", utils.ErrDatabase, string(key), errGet)
		}

		// Key found, now get the value
		return item.Value(func(val []byte) error {
			if len(val) == 0 {
				status = models.PageStatusPending // Key exists but has no data yet
				return nil
			}

			// Value is not empty, try to decode
			var decodedEntry models.PageDBEntry
			if errJson := json.Unmarshal(val, &decodedEntry); errJson != nil {
				s.log.Warnf("Failed to unmarshal PageDBEntry for key '%s': %v. Treating as 'pending'.", string(key), errJson)
				status = models.PageStatusPending // Treat unmarshal error as pending state
				return nil                        // Return nil to continue View, status is set
			}

			// Successfully decoded
			entry = &decodedEntry
			status = decodedEntry.Status
			s.log.Debugf("Page key '%s' found, decoded status: %s", string(key), status)
			return nil
		})
	})

	if errView != nil {
		s.log.Errorf("DB View error in CheckPageStatus for key '%s': %v", string(key), errView)
		status = models.PageStatusDBError // Set status to indicate DB error
		return status, nil, errView       // Return the DB error
	}

	// No DB error occurred during View/Get/Value
	return status, entry, nil
}

// UpdatePageStatus implements the VisitedStore interface
func (s *BadgerStore) UpdatePageStatus(normalizedPageURL string, entry *models.PageDBEntry) error {
	if s.db == nil {
		return errors.New("visitedDB not initialized")
	}
	key := []byte(pageKeyPrefix + normalizedPageURL)

	entryBytes, errJson := json.Marshal(entry)
	if errJson != nil {
		wrappedErr := fmt.Errorf("%w: failed to marshal PageDBEntry for key '%s': %w", utils.ErrParsing, string(key), errJson)
		s.log.Error(wrappedErr)
		return wrappedErr
	}

	isNew := false
	err := s.dbUpdate(func(txn *badger.Txn) error {
		_, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			isNew = true
		}
		e := badger.NewEntry(key, entryBytes)
		return txn.SetEntry(e)
	})

	if err != nil {
		s.log.WithField("key", string(key)).Errorf("DB Update error in UpdatePageStatus: %v", err)
		return fmt.Errorf("%w: failed setting page status for key '%s': %w", utils.ErrDatabase, string(key), err)
	}
	if isNew {
		s.keyCount.Add(1)
	}

	s.log.Debugf("Successfully updated page status for key '%s' to '%s'", string(key), entry.Status)
	return nil
}

// GetPageContentHash retrieves the content hash for a previously crawled page.
// Returns the hash string, whether it exists, and any error.
func (s *BadgerStore) GetPageContentHash(normalizedPageURL string) (hash string, exists bool, err error) {
	status, entry, checkErr := s.CheckPageStatus(normalizedPageURL)
	if checkErr != nil {
		return "", false, checkErr
	}

	// Only return hash for successfully processed pages
	if status == models.PageStatusSuccess && entry != nil && entry.ContentHash != "" {
		return entry.ContentHash, true, nil
	}

	return "", false, nil
}

// CheckImageStatus implements the VisitedStore interface
func (s *BadgerStore) CheckImageStatus(normalizedImgURL string) (models.ImageStatus, *models.ImageDBEntry, error) {
	status := models.ImageStatusNotFound
	var entry *models.ImageDBEntry = nil
	key := []byte(imageKeyPrefix + normalizedImgURL)

	errView := s.db.View(func(txn *badger.Txn) error {
		item, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			status = models.ImageStatusNotFound
			return nil
		}
		if errGet != nil {
			return fmt.Errorf("%w: failed getting image key '%s': %w", utils.ErrDatabase, string(key), errGet)
		}

		return item.Value(func(val []byte) error {
			// Image entries should never be empty if written correctly
			if len(val) == 0 {
				s.log.Warnf("Image key '%s' found with empty value, invalid state. Treating as 'not_found'.", string(key))
				status = models.ImageStatusNotFound
				return nil
			}

			var decodedEntry models.ImageDBEntry
			if errJson := json.Unmarshal(val, &decodedEntry); errJson != nil {
				s.log.Warnf("Failed to unmarshal ImageDBEntry for key '%s': %v. Treating as 'not_found'.", string(key), errJson)
				status = models.ImageStatusNotFound
				return nil
			}

			entry = &decodedEntry
			status = decodedEntry.Status
			return nil
		})
	})

	if errView != nil {
		s.log.Errorf("DB View error in CheckImageStatus for key '%s': %v", string(key), errView)
		status = models.ImageStatusDBError
		return status, nil, errView
	}

	return status, entry, nil
}

// UpdateImageStatus implements the VisitedStore interface
func (s *BadgerStore) UpdateImageStatus(normalizedImgURL string, entry *models.ImageDBEntry) error {
	if s.db == nil {
		return errors.New("visitedDB not initialized")
	}
	key := []byte(imageKeyPrefix + normalizedImgURL)

	entryBytes, errJson := json.Marshal(entry)
	if errJson != nil {
		wrappedErr := fmt.Errorf("%w: failed to marshal ImageDBEntry for key '%s': %w", utils.ErrParsing, string(key), errJson)
		s.log.Error(wrappedErr)
		return wrappedErr
	}

	isNew := false
	err := s.dbUpdate(func(txn *badger.Txn) error {
		_, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			isNew = true
		}
		e := badger.NewEntry(key, entryBytes)
		return txn.SetEntry(e)
	})

	if err != nil {
		s.log.WithField("key", string(key)).Errorf("DB Update error in UpdateImageStatus: %v", err)
		return fmt.Errorf("%w: failed setting image status for key '%s': %w", utils.ErrDatabase, string(key), err)
	}
	if isNew {
		s.keyCount.Add(1)
	}

	return nil
}

// GetVisitedCount implements the VisitedStore interface.
// Returns the cached key count (O(1)) maintained by atomic increments on writes.
func (s *BadgerStore) GetVisitedCount() (int, error) {
	return int(s.keyCount.Load()), nil
}

// RunGC runs BadgerDB's garbage collection periodically
func (s *BadgerStore) RunGC(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute // Default interval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.log.Info("BadgerDB GC goroutine started.")

	for {
		select {
		case <-ticker.C:
			// Check if DB is valid before running GC
			if s.db == nil || s.db.IsClosed() {
				s.log.Info("DB GC: Database is nil or closed, skipping GC cycle.")
				continue
			}

			s.log.Info("Running BadgerDB value log garbage collection...")
			var err error
			// Loop GC until it returns ErrNoRewrite or another error
			for {
				// Run GC if log is at least 50% reclaimable space
				err = s.db.RunValueLogGC(0.5)
				if err == nil {
					s.log.Info("BadgerDB GC cycle completed.")
				} else {
					break // Exit loop if GC finished (ErrNoRewrite) or encountered an error
				}
			}

			// Log outcome
			if errors.Is(err, badger.ErrNoRewrite) {
				s.log.Info("BadgerDB GC finished (no rewrite needed).")
			} else {
				s.log.Errorf("BadgerDB GC error: %v", err)
			}

		case <-ctx.Done(): // Check if stop signal received via context cancellation
			s.log.Infof("Stopping BadgerDB garbage collection goroutine due to context cancellation: %v", ctx.Err())
			return
		}
	}
}

// RequeueIncomplete implements the VisitedStore interface
func (s *BadgerStore) RequeueIncomplete(ctx context.Context, workChan chan<- models.WorkItem) (int, int, error) {
	s.log.Info("Resume Mode: Scanning database for incomplete tasks to requeue...")
	requeuedCount := 0
	scanErrors := 0
	scanStartTime := time.Now()

	scanErr := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true // Need values to check status
		it := txn.NewIterator(opts)
		defer it.Close()

		keyPrefixBytes := []byte(pageKeyPrefix)

		for it.Seek(keyPrefixBytes); it.ValidForPrefix(keyPrefixBytes); it.Next() {
			// Check context cancellation within the loop
			select {
			case <-ctx.Done():
				s.log.Warnf("Resume scan interrupted by context cancellation: %v", ctx.Err())
				return ctx.Err() // Stop iteration
			default:
				// Continue processing item
			}

			item := it.Item()
			keyBytesWithPrefix := item.KeyCopy(nil)
			keyBytes := keyBytesWithPrefix[len(keyPrefixBytes):] // Strip prefix
			urlToRequeue := string(keyBytes)

			errGetValue := item.Value(func(valBytes []byte) error {
				valCopy := make([]byte, len(valBytes))
				copy(valCopy, valBytes)
				shouldRequeue := false
				requeueDepth := 0

				if len(valCopy) == 0 { // Case 1: Empty value (implicitly pending)
					s.log.Debugf("Resume Scan: Found empty value for '%s'. Requeueing (Depth 0).", urlToRequeue)
					shouldRequeue = true
					requeueDepth = 0 // Fallback depth
				} else { // Case 2: Decode PageDBEntry
					var entry models.PageDBEntry
					if errJson := json.Unmarshal(valCopy, &entry); errJson != nil {
						s.log.Errorf("Resume Scan: Failed unmarshal PageDBEntry for '%s': %v. Skipping.", urlToRequeue, errJson)
						scanErrors++
						return nil // Continue iteration
					}
					// Case 3: Check status
					if entry.Status == models.PageStatusFailure || entry.Status == models.PageStatusPending {
						s.log.Debugf("Resume Scan: Requeueing '%s' (Status: %s, Depth: %d)", urlToRequeue, entry.Status, entry.Depth)
						shouldRequeue = true
						requeueDepth = entry.Depth // Use stored depth
					}
				}

				if shouldRequeue {
					// Send to channel, respecting context cancellation
					select {
					case workChan <- models.WorkItem{URL: urlToRequeue, Depth: requeueDepth}:
						requeuedCount++
					case <-ctx.Done():
						s.log.Warnf("Resume scan interrupted while sending '%s' to queue: %v", urlToRequeue, ctx.Err())
						return ctx.Err() // Stop iteration
					}
				}
				return nil
			})

			if errGetValue != nil {
				// Check if the error was context cancellation propagated from Value func
				if errors.Is(errGetValue, context.Canceled) || errors.Is(errGetValue, context.DeadlineExceeded) {
					return errGetValue // Propagate context error to stop iteration
				}
				s.log.Errorf("Resume Scan: Error getting value for key '%s': %v", urlToRequeue, errGetValue)
				scanErrors++
				// Decide whether to continue or stop on other value errors
				// return errGetValue // Optionally stop iteration on error
			}
		}
		return nil
	})

	durationScan := time.Since(scanStartTime)
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		// Log scan error only if it wasn't a context cancellation
		s.log.Errorf("Error during DB scan for resume: %v.", scanErr)
	}
	s.log.Infof("Resume Scan Complete: Requeued %d tasks in %v. Errors: %d.", requeuedCount, durationScan, scanErrors)

	// Check if the error was cancellation, otherwise return the scan error
	if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
		return requeuedCount, scanErrors, scanErr // Return context error
	}
	return requeuedCount, scanErrors, scanErr // Return potential DB error
}

// WriteVisitedLog implements the VisitedStore interface.
func (s *BadgerStore) WriteVisitedLog(filePath string) error {
	s.log.Info("Writing list of visited page and image URLs (from DB)...")
	file, err := os.Create(filePath)
	if err != nil {
		s.log.Errorf("Failed create visited log '%s': %v", filePath, err)
		return fmt.Errorf("create visited log '%s': %w", filePath, err)
	}
	defer file.Close() // Ensure file is closed

	writer := bufio.NewWriter(file)
	s.log.Info("Iterating visited DB to write log file...")
	var dbErr error
	writtenCount := 0

	iterErr := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		pagePrefixBytes := []byte(pageKeyPrefix)
		imgPrefixBytes := []byte(imageKeyPrefix)

		for it.Rewind(); it.Valid(); it.Next() {
			// Check context cancellation within the loop
			select {
			case <-s.ctx.Done():
				s.log.Warnf("WriteVisitedLog scan interrupted by context cancellation: %v", s.ctx.Err())
				return s.ctx.Err() // Stop iteration
			default:
				// Continue processing item
			}

			item := it.Item()
			keyBytesWithPrefix := item.KeyCopy(nil) // Copy key with prefix
			var keyToWrite string
			// Check prefix and strip
			switch {
			case bytes.HasPrefix(keyBytesWithPrefix, pagePrefixBytes):
				keyToWrite = string(keyBytesWithPrefix[len(pagePrefixBytes):])
			case bytes.HasPrefix(keyBytesWithPrefix, imgPrefixBytes):
				keyToWrite = string(keyBytesWithPrefix[len(imgPrefixBytes):])
			default:
				s.log.Warnf("Skipping unexpected key in DB (no page/img prefix): %s", string(keyBytesWithPrefix))
				continue // Skip keys without expected prefixes
			}

			_, writeErr := writer.WriteString(keyToWrite + "\n") // Write stripped key
			if writeErr != nil {
				if dbErr == nil { // Store first write error
					dbErr = writeErr
				}
				s.log.Errorf("Error writing URL '%s' to visited log: %v", keyToWrite, writeErr)
				// Continue writing other URLs if possible
			}
			writtenCount++
			if writtenCount%5000 == 0 {
				s.log.Debugf("Flushing visited writer after %d entries...", writtenCount)
				if flushErr := writer.Flush(); flushErr != nil {
					if dbErr == nil { // Store first flush error
						dbErr = flushErr
					}
					s.log.Errorf("Error flushing visited writer: %v", flushErr)
					// Continue if possible
				}
			}
		}
		return nil
	})

	// Handle errors after iteration
	if iterErr != nil && !errors.Is(iterErr, context.Canceled) && !errors.Is(iterErr, context.DeadlineExceeded) {
		s.log.Errorf("Error during visited DB iteration for log: %v", iterErr)
		if dbErr == nil {
			dbErr = iterErr
		}
	}

	// Final flush
	if flushErr := writer.Flush(); flushErr != nil {
		s.log.Errorf("Failed final flush for visited log '%s': %v", filePath, flushErr)
		if dbErr == nil {
			dbErr = flushErr
		}
	}

	// Sync to disk before closing
	if syncErr := file.Sync(); syncErr != nil {
		s.log.Errorf("Failed to sync visited log '%s': %v", filePath, syncErr)
		if dbErr == nil {
			dbErr = syncErr
		}
	}

	if iterErr == nil && dbErr == nil {
		s.log.Infof("Finished writing %d URLs to visited log: %s", writtenCount, filePath)
	} else {
		s.log.Warnf("Finished writing visited log with errors. Wrote ~%d URLs to %s", writtenCount, filePath)
	}

	// Return context error if iteration was cancelled, otherwise return first IO/DB error
	if errors.Is(iterErr, context.Canceled) || errors.Is(iterErr, context.DeadlineExceeded) {
		return iterErr
	}
	return dbErr
}

// Close implements the VisitedStore interface
func (s *BadgerStore) Close() error {
	if s.db != nil && !s.db.IsClosed() {
		s.log.Info("Closing visited DB...")
		err := s.db.Close()
		if err != nil {
			s.log.Errorf("Error closing visited DB: %v", err)
			return err
		}
		s.log.Info("Visited DB closed.")
		return nil
	}
	s.log.Info("Visited DB already closed or was not initialized.")
	return nil
}
