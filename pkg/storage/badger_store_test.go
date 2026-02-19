package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return logrus.NewEntry(log)
}

func newTestStore(t *testing.T) *BadgerStore {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewBadgerStore(ctx, dir, "example.com", false, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestNewBadgerStore(t *testing.T) {
	t.Run("fresh start has zero count", func(t *testing.T) {
		store := newTestStore(t)
		count, err := store.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("resume preserves data", func(t *testing.T) {
		dir := t.TempDir()
		ctx := context.Background()
		logger := testLogger()

		// Create store and add data
		store1, err := NewBadgerStore(ctx, dir, "example.com", false, logger)
		require.NoError(t, err)
		_, err = store1.MarkPageVisited("https://example.com/page1")
		require.NoError(t, err)
		require.NoError(t, store1.Close())

		// Reopen with resume=true
		store2, err := NewBadgerStore(ctx, dir, "example.com", true, logger)
		require.NoError(t, err)
		t.Cleanup(func() { store2.Close() })

		count, err := store2.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("fresh start wipes data", func(t *testing.T) {
		dir := t.TempDir()
		ctx := context.Background()
		logger := testLogger()

		// Create store and add data
		store1, err := NewBadgerStore(ctx, dir, "example.com", false, logger)
		require.NoError(t, err)
		_, err = store1.MarkPageVisited("https://example.com/page1")
		require.NoError(t, err)
		require.NoError(t, store1.Close())

		// Reopen with resume=false
		store2, err := NewBadgerStore(ctx, dir, "example.com", false, logger)
		require.NoError(t, err)
		t.Cleanup(func() { store2.Close() })

		count, err := store2.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func TestMarkPageVisited(t *testing.T) {
	store := newTestStore(t)

	t.Run("new URL returns true", func(t *testing.T) {
		added, err := store.MarkPageVisited("https://example.com/page1")
		require.NoError(t, err)
		assert.True(t, added)
	})

	t.Run("duplicate returns false", func(t *testing.T) {
		added, err := store.MarkPageVisited("https://example.com/page1")
		require.NoError(t, err)
		assert.False(t, added)
	})

	t.Run("count tracks correctly", func(t *testing.T) {
		_, err := store.MarkPageVisited("https://example.com/page2")
		require.NoError(t, err)
		count, err := store.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

func TestCheckPageStatus(t *testing.T) {
	store := newTestStore(t)

	t.Run("not found", func(t *testing.T) {
		status, entry, err := store.CheckPageStatus("https://example.com/missing")
		require.NoError(t, err)
		assert.Equal(t, models.PageStatusNotFound, status)
		assert.Nil(t, entry)
	})

	t.Run("pending with empty value", func(t *testing.T) {
		_, err := store.MarkPageVisited("https://example.com/pending")
		require.NoError(t, err)

		status, entry, err := store.CheckPageStatus("https://example.com/pending")
		require.NoError(t, err)
		assert.Equal(t, models.PageStatusPending, status)
		assert.Nil(t, entry)
	})

	t.Run("success entry", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		dbEntry := &models.PageDBEntry{
			Status:      models.PageStatusSuccess,
			ProcessedAt: now,
			LastAttempt: now,
			Depth:       2,
			ContentHash: "abc123",
		}
		require.NoError(t, store.UpdatePageStatus("https://example.com/success", dbEntry))

		status, entry, err := store.CheckPageStatus("https://example.com/success")
		require.NoError(t, err)
		assert.Equal(t, models.PageStatusSuccess, status)
		require.NotNil(t, entry)
		assert.Equal(t, "abc123", entry.ContentHash)
	})

	t.Run("failure entry", func(t *testing.T) {
		dbEntry := &models.PageDBEntry{
			Status:      models.PageStatusFailure,
			ErrorType:   "timeout",
			LastAttempt: time.Now(),
			Depth:       1,
		}
		require.NoError(t, store.UpdatePageStatus("https://example.com/failed", dbEntry))

		status, entry, err := store.CheckPageStatus("https://example.com/failed")
		require.NoError(t, err)
		assert.Equal(t, models.PageStatusFailure, status)
		require.NotNil(t, entry)
		assert.Equal(t, "timeout", entry.ErrorType)
	})

	t.Run("corrupted JSON falls back to pending", func(t *testing.T) {
		// Write raw invalid JSON bytes directly
		key := []byte(pageKeyPrefix + "https://example.com/corrupt")
		err := store.db.Update(func(txn *badger.Txn) error {
			return txn.SetEntry(badger.NewEntry(key, []byte("{invalid json")))
		})
		require.NoError(t, err)

		status, entry, err := store.CheckPageStatus("https://example.com/corrupt")
		require.NoError(t, err)
		assert.Equal(t, models.PageStatusPending, status)
		assert.Nil(t, entry)
	})
}

func TestUpdatePageStatus(t *testing.T) {
	store := newTestStore(t)

	t.Run("new entry", func(t *testing.T) {
		entry := &models.PageDBEntry{
			Status:      models.PageStatusSuccess,
			LastAttempt: time.Now(),
			Depth:       0,
		}
		err := store.UpdatePageStatus("https://example.com/new", entry)
		require.NoError(t, err)

		count, _ := store.GetVisitedCount()
		assert.Equal(t, 1, count)
	})

	t.Run("overwrite existing", func(t *testing.T) {
		entry := &models.PageDBEntry{
			Status:      models.PageStatusFailure,
			ErrorType:   "http_500",
			LastAttempt: time.Now(),
			Depth:       0,
		}
		err := store.UpdatePageStatus("https://example.com/new", entry)
		require.NoError(t, err)

		// Count should not increase on overwrite
		count, _ := store.GetVisitedCount()
		assert.Equal(t, 1, count)

		// Verify updated value
		status, got, err := store.CheckPageStatus("https://example.com/new")
		require.NoError(t, err)
		assert.Equal(t, models.PageStatusFailure, status)
		assert.Equal(t, "http_500", got.ErrorType)
	})

	t.Run("full round-trip all fields survive", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		entry := &models.PageDBEntry{
			Status:      models.PageStatusSuccess,
			ErrorType:   "",
			ProcessedAt: now,
			LastAttempt: now,
			Depth:       5,
			ContentHash: "sha256:deadbeef",
		}
		require.NoError(t, store.UpdatePageStatus("https://example.com/roundtrip", entry))

		_, got, err := store.CheckPageStatus("https://example.com/roundtrip")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, models.PageStatusSuccess, got.Status)
		assert.Equal(t, now.UTC(), got.ProcessedAt.UTC())
		assert.Equal(t, now.UTC(), got.LastAttempt.UTC())
		assert.Equal(t, 5, got.Depth)
		assert.Equal(t, "sha256:deadbeef", got.ContentHash)
	})
}

func TestGetPageContentHash(t *testing.T) {
	store := newTestStore(t)

	t.Run("success with hash", func(t *testing.T) {
		entry := &models.PageDBEntry{
			Status:      models.PageStatusSuccess,
			ContentHash: "hash123",
			LastAttempt: time.Now(),
		}
		require.NoError(t, store.UpdatePageStatus("https://example.com/hashed", entry))

		hash, exists, err := store.GetPageContentHash("https://example.com/hashed")
		require.NoError(t, err)
		assert.True(t, exists)
		assert.Equal(t, "hash123", hash)
	})

	t.Run("success without hash", func(t *testing.T) {
		entry := &models.PageDBEntry{
			Status:      models.PageStatusSuccess,
			ContentHash: "",
			LastAttempt: time.Now(),
		}
		require.NoError(t, store.UpdatePageStatus("https://example.com/nohash", entry))

		hash, exists, err := store.GetPageContentHash("https://example.com/nohash")
		require.NoError(t, err)
		assert.False(t, exists)
		assert.Empty(t, hash)
	})

	t.Run("failure status", func(t *testing.T) {
		entry := &models.PageDBEntry{
			Status:      models.PageStatusFailure,
			LastAttempt: time.Now(),
		}
		require.NoError(t, store.UpdatePageStatus("https://example.com/fail", entry))

		hash, exists, err := store.GetPageContentHash("https://example.com/fail")
		require.NoError(t, err)
		assert.False(t, exists)
		assert.Empty(t, hash)
	})

	t.Run("not found", func(t *testing.T) {
		hash, exists, err := store.GetPageContentHash("https://example.com/nope")
		require.NoError(t, err)
		assert.False(t, exists)
		assert.Empty(t, hash)
	})
}

func TestCheckImageStatus(t *testing.T) {
	store := newTestStore(t)

	t.Run("not found", func(t *testing.T) {
		status, entry, err := store.CheckImageStatus("https://example.com/missing.png")
		require.NoError(t, err)
		assert.Equal(t, models.ImageStatusNotFound, status)
		assert.Nil(t, entry)
	})

	t.Run("success entry", func(t *testing.T) {
		imgEntry := &models.ImageDBEntry{
			Status:    models.ImageStatusSuccess,
			LocalPath: "images/pic.png",
			Caption:   "A nice picture",
		}
		require.NoError(t, store.UpdateImageStatus("https://example.com/pic.png", imgEntry))

		status, entry, err := store.CheckImageStatus("https://example.com/pic.png")
		require.NoError(t, err)
		assert.Equal(t, models.ImageStatusSuccess, status)
		require.NotNil(t, entry)
		assert.Equal(t, "images/pic.png", entry.LocalPath)
		assert.Equal(t, "A nice picture", entry.Caption)
	})

	t.Run("failure entry", func(t *testing.T) {
		imgEntry := &models.ImageDBEntry{
			Status:    models.ImageStatusFailure,
			ErrorType: "too_large",
		}
		require.NoError(t, store.UpdateImageStatus("https://example.com/big.png", imgEntry))

		status, entry, err := store.CheckImageStatus("https://example.com/big.png")
		require.NoError(t, err)
		assert.Equal(t, models.ImageStatusFailure, status)
		require.NotNil(t, entry)
		assert.Equal(t, "too_large", entry.ErrorType)
	})

	t.Run("skipped entry", func(t *testing.T) {
		imgEntry := &models.ImageDBEntry{
			Status:    models.ImageStatusSkipped,
			ErrorType: "domain_blocked",
		}
		require.NoError(t, store.UpdateImageStatus("https://example.com/skip.png", imgEntry))

		status, _, err := store.CheckImageStatus("https://example.com/skip.png")
		require.NoError(t, err)
		assert.Equal(t, models.ImageStatusSkipped, status)
	})

	t.Run("empty value treated as not found", func(t *testing.T) {
		key := []byte(imageKeyPrefix + "https://example.com/empty.png")
		err := store.db.Update(func(txn *badger.Txn) error {
			return txn.SetEntry(badger.NewEntry(key, []byte{}))
		})
		require.NoError(t, err)

		status, entry, err := store.CheckImageStatus("https://example.com/empty.png")
		require.NoError(t, err)
		assert.Equal(t, models.ImageStatusNotFound, status)
		assert.Nil(t, entry)
	})
}

func TestUpdateImageStatus(t *testing.T) {
	store := newTestStore(t)

	t.Run("new entry", func(t *testing.T) {
		entry := &models.ImageDBEntry{
			Status:    models.ImageStatusSuccess,
			LocalPath: "img/a.png",
		}
		err := store.UpdateImageStatus("https://example.com/a.png", entry)
		require.NoError(t, err)

		count, _ := store.GetVisitedCount()
		assert.Equal(t, 1, count)
	})

	t.Run("overwrite existing", func(t *testing.T) {
		entry := &models.ImageDBEntry{
			Status:    models.ImageStatusFailure,
			ErrorType: "network",
		}
		err := store.UpdateImageStatus("https://example.com/a.png", entry)
		require.NoError(t, err)

		// Count should not increase
		count, _ := store.GetVisitedCount()
		assert.Equal(t, 1, count)
	})

	t.Run("full round-trip", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		entry := &models.ImageDBEntry{
			Status:      models.ImageStatusSuccess,
			LocalPath:   "img/b.png",
			Caption:     "test caption",
			LastAttempt: now,
		}
		require.NoError(t, store.UpdateImageStatus("https://example.com/b.png", entry))

		_, got, err := store.CheckImageStatus("https://example.com/b.png")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, models.ImageStatusSuccess, got.Status)
		assert.Equal(t, "img/b.png", got.LocalPath)
		assert.Equal(t, "test caption", got.Caption)
		assert.Equal(t, now.UTC(), got.LastAttempt.UTC())
	})
}

func TestGetVisitedCount(t *testing.T) {
	store := newTestStore(t)

	t.Run("empty", func(t *testing.T) {
		count, err := store.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("after page marks", func(t *testing.T) {
		store.MarkPageVisited("https://example.com/1")
		store.MarkPageVisited("https://example.com/2")
		count, err := store.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("after image updates", func(t *testing.T) {
		store.UpdateImageStatus("https://example.com/img1.png", &models.ImageDBEntry{
			Status: models.ImageStatusSuccess,
		})
		count, err := store.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 3, count)
	})

	t.Run("duplicates not double-counted", func(t *testing.T) {
		store.MarkPageVisited("https://example.com/1") // duplicate
		store.UpdateImageStatus("https://example.com/img1.png", &models.ImageDBEntry{
			Status: models.ImageStatusFailure,
		}) // overwrite
		count, err := store.GetVisitedCount()
		require.NoError(t, err)
		assert.Equal(t, 3, count)
	})
}

func TestRequeueIncomplete(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		store := newTestStore(t)
		ch := make(chan models.WorkItem, 10)
		requeued, scanErrors, err := store.RequeueIncomplete(context.Background(), ch)
		require.NoError(t, err)
		assert.Equal(t, 0, requeued)
		assert.Equal(t, 0, scanErrors)
		assert.Empty(t, ch)
	})

	t.Run("all success nothing requeued", func(t *testing.T) {
		store := newTestStore(t)
		store.UpdatePageStatus("https://example.com/ok", &models.PageDBEntry{
			Status:      models.PageStatusSuccess,
			LastAttempt: time.Now(),
		})
		ch := make(chan models.WorkItem, 10)
		requeued, _, err := store.RequeueIncomplete(context.Background(), ch)
		require.NoError(t, err)
		assert.Equal(t, 0, requeued)
		assert.Empty(t, ch)
	})

	t.Run("pending pages requeued", func(t *testing.T) {
		store := newTestStore(t)
		// Mark page (creates empty value = pending)
		store.MarkPageVisited("https://example.com/pending1")
		ch := make(chan models.WorkItem, 10)
		requeued, _, err := store.RequeueIncomplete(context.Background(), ch)
		require.NoError(t, err)
		assert.Equal(t, 1, requeued)
		item := <-ch
		assert.Equal(t, "https://example.com/pending1", item.URL)
		assert.Equal(t, 0, item.Depth)
	})

	t.Run("failed pages requeued with correct depth", func(t *testing.T) {
		store := newTestStore(t)
		store.UpdatePageStatus("https://example.com/fail", &models.PageDBEntry{
			Status:      models.PageStatusFailure,
			Depth:       3,
			LastAttempt: time.Now(),
		})
		ch := make(chan models.WorkItem, 10)
		requeued, _, err := store.RequeueIncomplete(context.Background(), ch)
		require.NoError(t, err)
		assert.Equal(t, 1, requeued)
		item := <-ch
		assert.Equal(t, "https://example.com/fail", item.URL)
		assert.Equal(t, 3, item.Depth)
	})

	t.Run("images skipped", func(t *testing.T) {
		store := newTestStore(t)
		store.UpdateImageStatus("https://example.com/img.png", &models.ImageDBEntry{
			Status:    models.ImageStatusFailure,
			ErrorType: "network",
		})
		ch := make(chan models.WorkItem, 10)
		requeued, _, err := store.RequeueIncomplete(context.Background(), ch)
		require.NoError(t, err)
		assert.Equal(t, 0, requeued)
		assert.Empty(t, ch)
	})

	t.Run("context cancellation", func(t *testing.T) {
		store := newTestStore(t)
		store.MarkPageVisited("https://example.com/p1")
		store.MarkPageVisited("https://example.com/p2")

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		ch := make(chan models.WorkItem, 10)
		_, _, err := store.RequeueIncomplete(ctx, ch)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestWriteVisitedLog(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		store := newTestStore(t)
		outPath := filepath.Join(t.TempDir(), "visited.log")
		err := store.WriteVisitedLog(outPath)
		require.NoError(t, err)

		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Empty(t, string(data))
	})

	t.Run("pages and images written without prefix", func(t *testing.T) {
		store := newTestStore(t)
		store.MarkPageVisited("https://example.com/page1")
		store.UpdateImageStatus("https://example.com/img.png", &models.ImageDBEntry{
			Status: models.ImageStatusSuccess,
		})

		outPath := filepath.Join(t.TempDir(), "visited.log")
		err := store.WriteVisitedLog(outPath)
		require.NoError(t, err)

		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		content := string(data)
		assert.Contains(t, content, "https://example.com/page1")
		assert.Contains(t, content, "https://example.com/img.png")
		// Prefixes should be stripped
		assert.NotContains(t, content, "page:")
		assert.NotContains(t, content, "img:")
	})

	t.Run("invalid path returns error", func(t *testing.T) {
		store := newTestStore(t)
		err := store.WriteVisitedLog("/nonexistent/dir/file.log")
		assert.Error(t, err)
	})
}

func TestRunGC(t *testing.T) {
	t.Run("respects context cancellation", func(t *testing.T) {
		store := newTestStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		// Should return without panicking
		done := make(chan struct{})
		go func() {
			store.RunGC(ctx, 50*time.Millisecond)
			close(done)
		}()

		select {
		case <-done:
			// success
		case <-time.After(2 * time.Second):
			t.Fatal("RunGC did not respect context cancellation")
		}
	})
}

func TestClose(t *testing.T) {
	t.Run("normal close", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewBadgerStore(context.Background(), dir, "example.com", false, testLogger())
		require.NoError(t, err)
		assert.NoError(t, store.Close())
	})

	t.Run("double close does not panic", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewBadgerStore(context.Background(), dir, "example.com", false, testLogger())
		require.NoError(t, err)
		assert.NoError(t, store.Close())
		assert.NoError(t, store.Close()) // second close should be safe
	})
}

func TestDBUpdateConflictRetry(t *testing.T) {
	t.Run("succeeds after transient conflicts", func(t *testing.T) {
		store := newTestStore(t)
		attempts := 0
		err := store.dbUpdate(func(txn *badger.Txn) error {
			attempts++
			if attempts <= 3 {
				return badger.ErrConflict
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 4, attempts)
	})

	t.Run("gives up after max retries", func(t *testing.T) {
		store := newTestStore(t)
		attempts := 0
		err := store.dbUpdate(func(txn *badger.Txn) error {
			attempts++
			return badger.ErrConflict
		})
		require.Error(t, err)
		require.ErrorIs(t, err, utils.ErrDatabase)
		assert.Contains(t, err.Error(), "transaction conflict not resolved")
		assert.Equal(t, maxConflictRetries, attempts)
	})

	t.Run("non-conflict error returned immediately", func(t *testing.T) {
		store := newTestStore(t)
		attempts := 0
		sentinel := errors.New("some other error")
		err := store.dbUpdate(func(txn *badger.Txn) error {
			attempts++
			return sentinel
		})
		require.Error(t, err)
		require.ErrorIs(t, err, sentinel)
		assert.Equal(t, 1, attempts)
	})
}

