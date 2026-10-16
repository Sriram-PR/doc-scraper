package fetch

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPool(limit int) *HostSemaphorePool {
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewHostSemaphorePool(limit, log)
}

func TestHostSemaphore_AcquireRelease_Basic(t *testing.T) {
	pool := newTestPool(2)

	// Two acquires should succeed
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))

	// Third should time out (all 2 slots held)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, pool.Acquire(ctx, "host-a"), "third acquire should fail")

	// Release one, then acquire should succeed again
	pool.Release("host-a")
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))

	// Cleanup
	pool.Release("host-a")
	pool.Release("host-a")
}

func TestHostSemaphore_MultipleHosts(t *testing.T) {
	pool := newTestPool(1)

	// Acquire on two different hosts should not interfere
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))
	require.NoError(t, pool.Acquire(context.Background(), "host-b"))

	assert.Equal(t, 2, pool.Len())

	pool.Release("host-a")
	pool.Release("host-b")
}

func TestHostSemaphore_EvictIdle_RemovesIdleEntries(t *testing.T) {
	pool := newTestPool(1)

	for _, host := range []string{"a.com", "b.com", "c.com"} {
		require.NoError(t, pool.Acquire(context.Background(), host))
		pool.Release(host)
	}

	require.Equal(t, 3, pool.Len())

	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)

	assert.Equal(t, 0, pool.Len())
}

func TestHostSemaphore_EvictIdle_PreservesActiveEntries(t *testing.T) {
	pool := newTestPool(1)

	// host-a: acquired and held
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))

	// host-b: acquired and released
	require.NoError(t, pool.Acquire(context.Background(), "host-b"))
	pool.Release("host-b")

	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)

	assert.Equal(t, 1, pool.Len(), "host-a should be preserved")

	pool.Release("host-a")
}

// A host released earlier carries a non-zero lastRelease, so only the active
// count protects it once it is held again.
func TestHostSemaphore_EvictIdle_PreservesReacquiredEntries(t *testing.T) {
	pool := newTestPool(1)

	require.NoError(t, pool.Acquire(context.Background(), "host-a"))
	pool.Release("host-a")
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))

	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)

	assert.Equal(t, 1, pool.Len(), "held host must not be evicted")

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
	require.NoError(t, pool.Acquire(context.Background(), "host-a"))

	// Second acquire with cancelled context should fail
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, pool.Acquire(ctx, "host-a"))

	// Release the held slot
	pool.Release("host-a")

	// After release, eviction should be able to clean up
	time.Sleep(5 * time.Millisecond)
	pool.evictIdle(1 * time.Millisecond)
	assert.Equal(t, 0, pool.Len())
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
			if !assert.NoError(t, pool.Acquire(context.Background(), host)) {
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
	assert.Equal(t, 0, pool.Len(), "all slots released")
}
