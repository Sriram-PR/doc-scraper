package fetch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
)

// SitemapDiscoverer defines the callback interface for handling discovered sitemap URLs
type SitemapDiscoverer interface {
	FoundSitemap(sitemapURL string)
}

// RobotsHandler manages fetching, parsing, caching, and checking robots.txt data
type RobotsHandler struct {
	fetcher         *Fetcher
	rateLimiter     *RateLimiter
	robotsCache     map[string]*robotstxt.RobotsData // hostname -> parsed data (or nil)
	robotsCacheMu   sync.Mutex
	globalSemaphore *semaphore.Weighted
	sitemapNotifier SitemapDiscoverer // Component to notify about found sitemaps
	cfg             *config.AppConfig
	log             *logrus.Entry
}

// NewRobotsHandler creates a RobotsHandler
func NewRobotsHandler(
	fetcher *Fetcher,
	rateLimiter *RateLimiter,
	globalSemaphore *semaphore.Weighted,
	sitemapNotifier SitemapDiscoverer,
	cfg *config.AppConfig,
	log *logrus.Entry,
) *RobotsHandler {
	return &RobotsHandler{
		fetcher:         fetcher,
		rateLimiter:     rateLimiter,
		robotsCache:     make(map[string]*robotstxt.RobotsData),
		globalSemaphore: globalSemaphore,
		sitemapNotifier: sitemapNotifier,
		cfg:             cfg,
		log:             log,
	}
}

// GetRobotsData retrieves robots.txt data for the targetURL's host, using cache or fetching
// Returns parsed data or nil on any error/4xx/missing file
// signalChan is only for coordinating the initial crawler startup fetch
func (rh *RobotsHandler) GetRobotsData(targetURL *url.URL, signalChan chan<- bool, ctx context.Context) *robotstxt.RobotsData {
	if ctx == nil {
		ctx = context.Background()
	}
	// Signal completion on exit if channel provided (non-blocking)
	if signalChan != nil {
		defer func() {
			select {
			case signalChan <- true:
			default:
				rh.log.Warn("Failed robots signalChan send")
			}
		}()
	}

	host := targetURL.Hostname()
	hostLog := rh.log.WithField("host", host)

	// 1. Check Cache
	rh.robotsCacheMu.Lock()
	robotsData, found := rh.robotsCache[host]
	rh.robotsCacheMu.Unlock()
	if found {
		return robotsData // Return cached data (could be nil)
	}

	// 2. Prepare Fetch URL
	robotsURL := &url.URL{Scheme: targetURL.Scheme, Host: host, Path: "/robots.txt"}
	if targetURL.Scheme != "http" && targetURL.Scheme != "https" {
		hostLog.Warnf("Invalid scheme '%s', defaulting to https for robots.txt", targetURL.Scheme)
		robotsURL.Scheme = "https"
	}
	robotsURLStr := robotsURL.String()
	robotsLog := hostLog.WithField("robots_url", robotsURLStr)
	robotsLog.Info("Fetching robots.txt...") // Log only on cache miss

	// 3. Acquire Global Semaphore
	semTimeout := rh.cfg.SemaphoreAcquireTimeout
	acquiredSemaphore := false
	robotsLog.Debug("Acquiring global semaphore...")
	ctxAcquire, cancelAcquire := context.WithTimeout(ctx, semTimeout)
	err := rh.globalSemaphore.Acquire(ctxAcquire, 1)
	cancelAcquire()
	if err != nil {
		robotsLog.Errorf("Error acquiring global semaphore: %v", err)
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock() // Cache failure
		return nil
	}
	acquiredSemaphore = true
	robotsLog.Debug("Acquired global semaphore.")
	defer func() { // Ensure release
		if acquiredSemaphore {
			rh.globalSemaphore.Release(1)
			robotsLog.Debug("Released global semaphore.")
		}
	}()

	// 4. Apply Rate Limit (using default delay)
	rh.rateLimiter.ApplyDelay(host, rh.cfg.DefaultDelayPerHost)

	// 5. Fetch Request (with retries via Fetcher)
	req, err := http.NewRequestWithContext(ctx, "GET", robotsURLStr, nil)
	if err != nil {
		robotsLog.Errorf("Error creating request: %v", err)
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}
	req.Header.Set("User-Agent", rh.cfg.DefaultUserAgent) // Use default agent for robots

	resp, fetchErr := rh.fetcher.FetchWithRetry(req, ctx)
	rh.rateLimiter.UpdateLastRequestTime(host) // Update time after attempt

	if fetchErr != nil {
		// Fetcher already logged error details
		robotsLog.Errorf("Fetching robots.txt failed: %v", fetchErr)
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}
	// Success: 2xx response.
	defer resp.Body.Close()

	// 7. Read and Parse Body.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		robotsLog.Errorf("Error reading body: %v", err)
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}

	data, err := robotstxt.FromBytes(bodyBytes)
	if err != nil {
		robotsLog.Errorf("Error parsing content: %v", err)
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}

	// 8. Cache Success & Notify Sitemaps
	robotsLog.Info("Successfully fetched and parsed robots.txt")
	rh.robotsCacheMu.Lock()
	rh.robotsCache[host] = data // Cache successful parse
	rh.robotsCacheMu.Unlock()

	if rh.sitemapNotifier != nil && len(data.Sitemaps) > 0 {
		robotsLog.Infof("Found %d sitemap directive(s)", len(data.Sitemaps))
		for _, sitemapURL := range data.Sitemaps {
			rh.sitemapNotifier.FoundSitemap(sitemapURL) // Notify discoverer
		}
	}

	return data
}

// TestAgent checks if the user agent is allowed access based on cached/fetched rules
// Returns true if allowed (or robots fetch/parse fails), false otherwise
func (rh *RobotsHandler) TestAgent(targetURL *url.URL, userAgent string, ctx context.Context) bool {
	// Get data, fetching if needed. Handles caching internally
	robotsData := rh.GetRobotsData(targetURL, nil, ctx)

	// Assume allowed if robots data could not be obtained (4xx, 5xx, network error, parse error)
	if robotsData == nil {
		return true
	}

	// Perform check using the parsed data
	return robotsData.TestAgent(targetURL.RequestURI(), userAgent)
}
