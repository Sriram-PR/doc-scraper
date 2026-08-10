package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	pkglog "github.com/Sriram-PR/doc-scraper/pkg/log"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

const (
	pageKeyPrefix  = "page:"
	imageKeyPrefix = "img:"
	visitedDBDir   = "visited_db"
)

// BadgerStore implements the VisitedStore interface using BadgerDB.
type BadgerStore struct {
	db       *badger.DB
	log      *slog.Logger
	ctx      context.Context
	keyCount atomic.Int64 // O(1) GetVisitedCount; maintained by atomic increments on writes
}

// NewBadgerStore initializes and returns a new BadgerStore.
func NewBadgerStore(ctx context.Context, stateDir, siteDomain string, resume bool, logger *slog.Logger) (*BadgerStore, error) {
	store := &BadgerStore{
		log: logger,
		ctx: ctx,
	}

	dbDirName := utils.SanitizeFilename(siteDomain) + "_" + visitedDBDir
	dbPath := filepath.Join(stateDir, dbDirName)

	if !resume {
		logger.Warn("Resume flag is false. REMOVING existing state directory", "path", dbPath)
		if err := os.RemoveAll(dbPath); err != nil {
			logger.Error("Failed to remove existing state directory", "path", dbPath, "err", err)
		}
	}

	logger.Info("Initializing visited URL database", "path", dbPath, "resume", resume)

	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return nil, fmt.Errorf("cannot create state directory %s: %w", dbPath, err)
	}

	badgerLogger := pkglog.NewBadgerSlogAdapter(logger.With("component", "badgerdb"))
	opts := badger.DefaultOptions(dbPath).
		WithLogger(badgerLogger).
		WithNumVersionsToKeep(1)

	var err error
	store.db, err = badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger database at %s: %w", dbPath, err)
	}

	if resume {
		count, err := store.countKeys()
		if err != nil {
			logger.Warn("Failed to count existing keys on resume", "err", err)
		} else {
			store.keyCount.Store(int64(count))
			logger.Info("Loaded existing key count on resume", "count", count)
		}
	}

	logger.Info("Visited URL database initialized successfully.")
	return store, nil
}

// countKeys performs a one-time full key scan used during initialization on resume.
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
		s.log.Debug("BadgerDB transaction conflict, retrying", "attempt", i+1, "max_attempts", maxConflictRetries)
	}
	return fmt.Errorf("%w: transaction conflict not resolved after %d retries", utils.ErrDatabase, maxConflictRetries)
}

func (s *BadgerStore) MarkPageVisited(normalizedPageURL string) (bool, error) {
	if s.db == nil {
		return false, errors.New("visitedDB not initialized")
	}
	added := false
	key := []byte(pageKeyPrefix + normalizedPageURL)

	err := s.dbUpdate(func(txn *badger.Txn) error {
		_, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			e := badger.NewEntry(key, []byte{})
			errSet := txn.SetEntry(e)
			if errSet == nil {
				added = true
			}
			return errSet
		}
		return errGet
	})

	if err != nil {
		s.log.Error("DB Update error in MarkPageVisited", "key", string(key), "err", err)
		return false, fmt.Errorf("%w: marking page key '%s': %w", utils.ErrDatabase, string(key), err)
	}
	if added {
		s.keyCount.Add(1)
	}

	return added, nil
}

func (s *BadgerStore) CheckPageStatus(normalizedPageURL string) (models.PageStatus, *models.PageDBEntry, error) {
	status := models.PageStatusNotFound
	var entry *models.PageDBEntry = nil
	key := []byte(pageKeyPrefix + normalizedPageURL)

	errView := s.db.View(func(txn *badger.Txn) error {
		item, errGet := txn.Get(key)
		if errors.Is(errGet, badger.ErrKeyNotFound) {
			status = models.PageStatusNotFound
			return nil
		}
		if errGet != nil {
			return fmt.Errorf("%w: failed getting page key '%s': %w", utils.ErrDatabase, string(key), errGet)
		}

		return item.Value(func(val []byte) error {
			if len(val) == 0 {
				status = models.PageStatusPending // key exists but has no data yet
				return nil
			}

			var decodedEntry models.PageDBEntry
			if errJson := json.Unmarshal(val, &decodedEntry); errJson != nil {
				s.log.Warn("Failed to unmarshal PageDBEntry, treating as 'pending'", "key", string(key), "err", errJson)
				status = models.PageStatusPending
				return nil
			}

			entry = &decodedEntry
			status = decodedEntry.Status
			s.log.Debug("Page key found", "key", string(key), "status", status)
			return nil
		})
	})

	if errView != nil {
		s.log.Error("DB View error in CheckPageStatus", "key", string(key), "err", errView)
		status = models.PageStatusDBError
		return status, nil, errView
	}

	return status, entry, nil
}

func (s *BadgerStore) UpdatePageStatus(normalizedPageURL string, entry *models.PageDBEntry) error {
	if s.db == nil {
		return errors.New("visitedDB not initialized")
	}
	key := []byte(pageKeyPrefix + normalizedPageURL)

	entryBytes, errJson := json.Marshal(entry)
	if errJson != nil {
		wrappedErr := fmt.Errorf("%w: failed to marshal PageDBEntry for key '%s': %w", utils.ErrParsing, string(key), errJson)
		s.log.Error("marshal failed", "key", string(key), "err", wrappedErr)
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
		s.log.Error("DB Update error in UpdatePageStatus", "key", string(key), "err", err)
		return fmt.Errorf("%w: failed setting page status for key '%s': %w", utils.ErrDatabase, string(key), err)
	}
	if isNew {
		s.keyCount.Add(1)
	}

	s.log.Debug("Successfully updated page status", "key", string(key), "status", entry.Status)
	return nil
}

// GetPageContentHash retrieves the content hash for a previously crawled page.
func (s *BadgerStore) GetPageContentHash(normalizedPageURL string) (hash string, exists bool, err error) {
	status, entry, checkErr := s.CheckPageStatus(normalizedPageURL)
	if checkErr != nil {
		return "", false, checkErr
	}

	if status == models.PageStatusSuccess && entry != nil && entry.ContentHash != "" {
		return entry.ContentHash, true, nil
	}

	return "", false, nil
}

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
			if len(val) == 0 { // image entries should always have a value
				s.log.Warn("Image key found with empty value, invalid state. Treating as 'not_found'.", "key", string(key))
				status = models.ImageStatusNotFound
				return nil
			}

			var decodedEntry models.ImageDBEntry
			if errJson := json.Unmarshal(val, &decodedEntry); errJson != nil {
				s.log.Warn("Failed to unmarshal ImageDBEntry, treating as 'not_found'", "key", string(key), "err", errJson)
				status = models.ImageStatusNotFound
				return nil
			}

			entry = &decodedEntry
			status = decodedEntry.Status
			return nil
		})
	})

	if errView != nil {
		s.log.Error("DB View error in CheckImageStatus", "key", string(key), "err", errView)
		status = models.ImageStatusDBError
		return status, nil, errView
	}

	return status, entry, nil
}

func (s *BadgerStore) UpdateImageStatus(normalizedImgURL string, entry *models.ImageDBEntry) error {
	if s.db == nil {
		return errors.New("visitedDB not initialized")
	}
	key := []byte(imageKeyPrefix + normalizedImgURL)

	entryBytes, errJson := json.Marshal(entry)
	if errJson != nil {
		wrappedErr := fmt.Errorf("%w: failed to marshal ImageDBEntry for key '%s': %w", utils.ErrParsing, string(key), errJson)
		s.log.Error("marshal failed", "key", string(key), "err", wrappedErr)
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
		s.log.Error("DB Update error in UpdateImageStatus", "key", string(key), "err", err)
		return fmt.Errorf("%w: failed setting image status for key '%s': %w", utils.ErrDatabase, string(key), err)
	}
	if isNew {
		s.keyCount.Add(1)
	}

	return nil
}

func (s *BadgerStore) GetVisitedCount() (int, error) {
	return int(s.keyCount.Load()), nil
}

// RunGC runs BadgerDB value-log garbage collection on a fixed interval.
func (s *BadgerStore) RunGC(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.log.Info("BadgerDB GC goroutine started.")

	for {
		select {
		case <-ticker.C:
			if s.db == nil || s.db.IsClosed() {
				s.log.Info("DB GC: Database is nil or closed, skipping GC cycle.")
				continue
			}

			s.log.Info("Running BadgerDB value log garbage collection...")
			var err error
			for {
				err = s.db.RunValueLogGC(0.5)
				if err == nil {
					s.log.Info("BadgerDB GC cycle completed.")
				} else {
					break
				}
			}

			if errors.Is(err, badger.ErrNoRewrite) {
				s.log.Info("BadgerDB GC finished (no rewrite needed).")
			} else {
				s.log.Error("BadgerDB GC error", "err", err)
			}

		case <-ctx.Done():
			s.log.Info("Stopping BadgerDB garbage collection goroutine due to context cancellation", "err", ctx.Err())
			return
		}
	}
}

func (s *BadgerStore) RequeueIncomplete(ctx context.Context, workChan chan<- models.WorkItem, includeSuccess bool) (int, int, error) {
	s.log.Info("Resume Mode: Scanning database for incomplete tasks to requeue...")
	requeuedCount := 0
	scanErrors := 0
	scanStartTime := time.Now()

	scanErr := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		keyPrefixBytes := []byte(pageKeyPrefix)

		for it.Seek(keyPrefixBytes); it.ValidForPrefix(keyPrefixBytes); it.Next() {
			// Check context cancellation within the loop
			select {
			case <-ctx.Done():
				s.log.Warn("Resume scan interrupted by context cancellation", "err", ctx.Err())
				return ctx.Err() // Stop iteration
			default:
			}

			item := it.Item()
			keyBytesWithPrefix := item.KeyCopy(nil)
			keyBytes := keyBytesWithPrefix[len(keyPrefixBytes):]
			urlToRequeue := string(keyBytes)

			errGetValue := item.Value(func(valBytes []byte) error {
				valCopy := make([]byte, len(valBytes))
				copy(valCopy, valBytes)
				shouldRequeue := false
				requeueDepth := 0

				if len(valCopy) == 0 {
					s.log.Debug("Resume scan: empty value, requeueing", "url", urlToRequeue, "depth", 0)
					shouldRequeue = true
					requeueDepth = 0
				} else {
					var entry models.PageDBEntry
					if errJson := json.Unmarshal(valCopy, &entry); errJson != nil {
						s.log.Error("Resume scan: failed to unmarshal PageDBEntry, skipping", "url", urlToRequeue, "err", errJson)
						scanErrors++
						return nil
					}
					if entry.Status == models.PageStatusFailure || entry.Status == models.PageStatusPending ||
						(includeSuccess && entry.Status == models.PageStatusSuccess) {
						s.log.Debug("Resume scan: requeueing", "url", urlToRequeue, "status", entry.Status, "depth", entry.Depth)
						shouldRequeue = true
						requeueDepth = entry.Depth
					}
				}

				if shouldRequeue {
					// Send to channel, respecting context cancellation
					select {
					case workChan <- models.WorkItem{URL: urlToRequeue, Depth: requeueDepth}:
						requeuedCount++
					case <-ctx.Done():
						s.log.Warn("Resume scan interrupted while sending to queue", "url", urlToRequeue, "err", ctx.Err())
						return ctx.Err() // Stop iteration
					}
				}
				return nil
			})

			if errGetValue != nil {
				if errors.Is(errGetValue, context.Canceled) || errors.Is(errGetValue, context.DeadlineExceeded) {
					return errGetValue
				}
				s.log.Error("Resume scan: error getting value", "url", urlToRequeue, "err", errGetValue)
				scanErrors++
			}
		}
		return nil
	})

	durationScan := time.Since(scanStartTime)
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		s.log.Error("Error during DB scan for resume", "err", scanErr)
	}
	s.log.Info("Resume scan complete", "requeued", requeuedCount, "duration", durationScan, "errors", scanErrors)

	return requeuedCount, scanErrors, scanErr
}

func (s *BadgerStore) Close() error {
	if s.db != nil && !s.db.IsClosed() {
		s.log.Info("Closing visited DB...")
		err := s.db.Close()
		if err != nil {
			s.log.Error("Error closing visited DB", "err", err)
			return err
		}
		s.log.Info("Visited DB closed.")
		return nil
	}
	s.log.Info("Visited DB already closed or was not initialized.")
	return nil
}
