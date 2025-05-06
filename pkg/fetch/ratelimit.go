package fetch

import (
	"math/rand"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// RateLimiter manages request timing per host for politeness
type RateLimiter struct {
	hostLastRequest   map[string]time.Time // hostname -> last request attempt time
	hostLastRequestMu sync.Mutex           // Protects hostLastRequest map
	defaultDelay      time.Duration        // Fallback delay if specific delay is invalid
	log               *logrus.Logger
}

// NewRateLimiter creates a RateLimiter
func NewRateLimiter(defaultDelay time.Duration, log *logrus.Logger) *RateLimiter {
	return &RateLimiter{
		hostLastRequest: make(map[string]time.Time),
		defaultDelay:    defaultDelay,
		log:             log,
	}
}

// ApplyDelay sleeps if the time since the last request to the host is less than minDelay
// Includes jitter (+/- 10%) to desynchronize requests
func (rl *RateLimiter) ApplyDelay(host string, minDelay time.Duration) {
	// Use default delay if minDelay is invalid
	if minDelay <= 0 {
		minDelay = rl.defaultDelay
	}
	// No delay needed if effective delay is zero or negative
	if minDelay <= 0 {
		return
	}

	// Read last request time safely
	rl.hostLastRequestMu.Lock()
	lastReqTime, exists := rl.hostLastRequest[host]
	rl.hostLastRequestMu.Unlock() // Unlock before potentially sleeping

	if exists {
		elapsed := time.Since(lastReqTime)
		if elapsed < minDelay {
			sleepDuration := minDelay - elapsed

			// Add jitter: +/- 10% of sleepDuration
			var jitter time.Duration
			if sleepDuration > 0 {
				jitterRange := int64(sleepDuration) / 5 // 20% range width for +/-10%
				if jitterRange > 0 {                    // Avoid Int63n(0)
					jitter = time.Duration(rand.Int63n(jitterRange)) - (sleepDuration / 10)
				}
			}

			finalSleep := sleepDuration + jitter
			if finalSleep < 0 {
				finalSleep = 0 // Ensure non-negative sleep
			}

			if finalSleep > 0 {
				rl.log.WithFields(logrus.Fields{
					"host": host, "sleep": finalSleep, "required_delay": minDelay, "elapsed": elapsed,
				}).Debug("Rate limit applying sleep")
				time.Sleep(finalSleep)
			}
		}
	}
	// Note: Timestamp update via UpdateLastRequestTime happens *after* the request attempt in calling code
}

// UpdateLastRequestTime records the current time as the last request attempt time for the host
// Call this *after* an HTTP request attempt to the host
func (rl *RateLimiter) UpdateLastRequestTime(host string) {
	rl.hostLastRequestMu.Lock()
	rl.hostLastRequest[host] = time.Now()
	rl.hostLastRequestMu.Unlock()
}
