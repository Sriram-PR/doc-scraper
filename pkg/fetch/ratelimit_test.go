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

func newTestRateLimiter() *RateLimiter {
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewRateLimiter(100*time.Millisecond, log)
}

func TestApplyDelay_RespectsContextCancellation(t *testing.T) {
	rl := newTestRateLimiter()
	host := "example.com"

	// First call reserves the next slot; a second call would otherwise sleep.
	rl.ApplyDelay(context.Background(), host, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	rl.ApplyDelay(ctx, host, 5*time.Second)
	elapsed := time.Since(start)

	assert.LessOrEqual(t, elapsed, 100*time.Millisecond, "ApplyDelay with cancelled context should return promptly")
}

func TestApplyDelay_SleepsForExpectedDuration(t *testing.T) {
	rl := newTestRateLimiter()
	host := "example.com"

	// First call fires immediately and reserves the slot; the second must wait
	// out the spacing.
	rl.ApplyDelay(context.Background(), host, 100*time.Millisecond)

	start := time.Now()
	rl.ApplyDelay(context.Background(), host, 100*time.Millisecond)
	elapsed := time.Since(start)

	// Allow for jitter (+/- 10%) and timer imprecision
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "ApplyDelay returned too quickly")
	assert.LessOrEqual(t, elapsed, 300*time.Millisecond, "ApplyDelay took too long")
}

func TestApplyDelay_NoDelayOnFirstRequest(t *testing.T) {
	rl := newTestRateLimiter()
	host := "fresh-host.com"

	start := time.Now()
	rl.ApplyDelay(context.Background(), host, 5*time.Second)
	elapsed := time.Since(start)

	assert.LessOrEqual(t, elapsed, 10*time.Millisecond, "ApplyDelay on first request should return instantly")
}

func TestApplyDelay_SpacesConcurrentSameHost(t *testing.T) {
	rl := newTestRateLimiter()
	host := "concurrent.example.com"
	const n = 5
	const delay = 50 * time.Millisecond

	var wg sync.WaitGroup
	start := time.Now()
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.ApplyDelay(context.Background(), host, delay)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// With slot reservation, n concurrent requests to one host are spaced by
	// ~delay each, so the last fires at ~(n-1)*delay. The old model (read a
	// stale timestamp, then sleep) let them all return within ~delay.
	minExpected := time.Duration(n-1) * delay * 8 / 10
	assert.GreaterOrEqual(t, elapsed, minExpected, "concurrent same-host requests not spaced")
}

func TestWithJitter_SmallDurationsDoNotPanic(t *testing.T) {
	for _, d := range []time.Duration{0, 1, 2, 4, time.Nanosecond} {
		_ = withJitter(d) // must not panic on rand.Int63n(0)
	}
}

func TestWithJitter_WithinTenPercent(t *testing.T) {
	d := time.Second
	for range 1000 {
		got := withJitter(d)
		require.GreaterOrEqual(t, got, d-d/10)
		require.Less(t, got, d+d/10)
	}
}
