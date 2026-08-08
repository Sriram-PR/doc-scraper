package fetch

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// RateLimiter enforces a minimum spacing between successive requests to the
// same host.
type RateLimiter struct {
	mu           sync.Mutex
	hostNextFree map[string]time.Time // host -> earliest time the next request may fire
	defaultDelay time.Duration        // fallback spacing when the caller passes an invalid one
	log          *slog.Logger
}

// NewRateLimiter creates a RateLimiter whose per-host spacing defaults to
// defaultDelay when a caller does not supply a positive one.
func NewRateLimiter(defaultDelay time.Duration, log *slog.Logger) *RateLimiter {
	return &RateLimiter{
		hostNextFree: make(map[string]time.Time),
		defaultDelay: defaultDelay,
		log:          log,
	}
}

// ApplyDelay reserves the next politeness slot for host under the lock, then
// blocks until it is due (or ctx is cancelled). Reserving before sleeping is
// what makes spacing hold under concurrency: without it, several goroutines
// racing in for the same host would all read one stale timestamp and fire
// together. The reservation advances the clock immediately, so there is no
// separate post-request update to remember.
func (rl *RateLimiter) ApplyDelay(ctx context.Context, host string, minDelay time.Duration) {
	if minDelay <= 0 {
		minDelay = rl.defaultDelay
	}
	if minDelay <= 0 {
		return
	}

	rl.mu.Lock()
	now := time.Now()
	target := now
	if next, ok := rl.hostNextFree[host]; ok && next.After(now) {
		target = next
	}
	rl.hostNextFree[host] = target.Add(withJitter(minDelay))
	rl.mu.Unlock()

	wait := time.Until(target)
	if wait <= 0 {
		return
	}

	rl.log.Debug("Rate limit applying sleep", "host", host, "sleep", wait, "required_delay", minDelay)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		rl.log.Debug("Rate limit sleep interrupted by context cancellation", "host", host)
	}
}

// withJitter returns d adjusted by a random +/-10% to desynchronize timers and
// avoid thundering-herd bursts. It returns d unchanged for durations too small
// to jitter, which also avoids a rand.Int63n(0) panic.
func withJitter(d time.Duration) time.Duration {
	span := int64(d) / 5 // full width = 20% of d, i.e. +/-10%
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(span)) - d/10
}
