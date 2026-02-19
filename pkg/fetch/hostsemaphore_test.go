package fetch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func newTestPool(limit int) *HostSemaphorePool {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetLevel(logrus.DebugLevel)
	return NewHostSemaphorePool(limit, log)
}

func TestHostSemaphore_AcquireRelease_Basic(t *testing.T) {
	pool := newTestPool(2)

	// Two acquires should succeed
	if err := pool.Acquire(context.Background(), "host-a"); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := pool.Acquire(context.Background(), "host-a"); err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}

	// Third should time out (all 2 slots held)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := pool.Acquire(ctx, "host-a"); err == nil {
		t.Fatal("expected third acquire to fail, but it succeeded")
	}

	// Release one, then acquire should succeed again
	pool.Release("host-a")
	if err := pool.Acquire(context.Background(), "host-a"); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}

	// Cleanup
	pool.Release("host-a")
	pool.Release("host-a")
}

func TestHostSemaphore_MultipleHosts(t *testing.T) {
	pool := newTestPool(1)

	// Acquire on two different hosts should not interfere
	if err := pool.Acquire(context.Background(), "host-a"); err != nil {
		t.Fatalf("host-a acquire failed: %v", err)
	}
	if err := pool.Acquire(context.Background(), "host-b"); err != nil {
		t.Fatalf("host-b acquire failed: %v", err)
	}

	if pool.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", pool.Len())
	}

	pool.Release("host-a")
	pool.Release("host-b")
}

func TestHostSemaphore_EvictIdle_RemovesIdleEntries(t *testing.T) {
	pool := newTestPool(1)

	for _, host := range []string{"a.com", "b.com", "c.com"} {
		if err := pool.Acquire(context.Background(), host); err != nil {
			t.Fatalf("acquire %s failed: %v", host, err)
		}
		pool.Release(host)
	}

	if pool.Len() != 3 {
		t.Fatalf("expected 3 entries before eviction, got %d", pool.Len())
	}

	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)

	if pool.Len() != 0 {
		t.Errorf("expected 0 entries after eviction, got %d", pool.Len())
	}
}

func TestHostSemaphore_EvictIdle_PreservesActiveEntries(t *testing.T) {
	pool := newTestPool(1)

	// host-a: acquired and held
	if err := pool.Acquire(context.Background(), "host-a"); err != nil {
		t.Fatalf("acquire host-a failed: %v", err)
	}

	// host-b: acquired and released
	if err := pool.Acquire(context.Background(), "host-b"); err != nil {
		t.Fatalf("acquire host-b failed: %v", err)
	}
	pool.Release("host-b")

	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)

	if pool.Len() != 1 {
		t.Errorf("expected 1 entry (host-a preserved), got %d", pool.Len())
	}

	pool.Release("host-a")
}

func TestHostSemaphore_RunEviction_RespectsContextCancellation(t *testing.T) {
	pool := newTestPool(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	done := make(chan struct{})
	go func() {
		pool.RunEviction(ctx, time.Minute)
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("RunEviction did not respect context cancellation")
	}
}

func TestHostSemaphore_Acquire_RollbackOnContextCancel(t *testing.T) {
	pool := newTestPool(1)

	// Hold the only slot
	if err := pool.Acquire(context.Background(), "host-a"); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Second acquire with cancelled context should fail
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pool.Acquire(ctx, "host-a"); err == nil {
		t.Fatal("expected acquire with cancelled context to fail")
	}

	// Release the held slot
	pool.Release("host-a")

	// After release, eviction should be able to clean up
	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)
	if pool.Len() != 0 {
		t.Errorf("expected 0 entries after eviction, got %d", pool.Len())
	}
}

func TestHostSemaphore_ConcurrentAcquireRelease(t *testing.T) {
	pool := newTestPool(5)
	host := "concurrent.com"
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			if err := pool.Acquire(context.Background(), host); err != nil {
				t.Errorf("acquire failed: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
			pool.Release(host)
		}()
	}

	wg.Wait()

	// All released, should be evictable
	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)
	if pool.Len() != 0 {
		t.Errorf("expected 0 entries after all released, got %d", pool.Len())
	}
}
