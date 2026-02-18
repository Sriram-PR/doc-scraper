package fetch

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func newTestRateLimiter() *RateLimiter {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetLevel(logrus.DebugLevel)
	return NewRateLimiter(100*time.Millisecond, log)
}

func TestApplyDelay_RespectsContextCancellation(t *testing.T) {
	rl := newTestRateLimiter()
	host := "example.com"

	// Simulate a recent request so delay is needed
	rl.UpdateLastRequestTime(host)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	rl.ApplyDelay(ctx, host, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("ApplyDelay with cancelled context took %v, expected <100ms", elapsed)
	}
}

func TestApplyDelay_SleepsForExpectedDuration(t *testing.T) {
	rl := newTestRateLimiter()
	host := "example.com"

	// Simulate a recent request so delay is needed
	rl.UpdateLastRequestTime(host)

	start := time.Now()
	rl.ApplyDelay(context.Background(), host, 100*time.Millisecond)
	elapsed := time.Since(start)

	// Allow for jitter (+/- 10%) and timer imprecision
	if elapsed < 50*time.Millisecond {
		t.Errorf("ApplyDelay returned too quickly: %v, expected ~100ms", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("ApplyDelay took too long: %v, expected ~100ms", elapsed)
	}
}

func TestApplyDelay_NoDelayOnFirstRequest(t *testing.T) {
	rl := newTestRateLimiter()
	host := "fresh-host.com"

	start := time.Now()
	rl.ApplyDelay(context.Background(), host, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed > 10*time.Millisecond {
		t.Errorf("ApplyDelay on first request took %v, expected instant return", elapsed)
	}
}
