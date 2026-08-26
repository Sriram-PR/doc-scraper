package fetch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

// robotsNegativeTTL bounds how long a failed robots.txt fetch stays cached.
// Without it a single transient error would fail-open (allow everything) for
// the whole process; after the TTL the next request re-attempts the fetch.
const robotsNegativeTTL = 10 * time.Minute

// SitemapDiscoverer is the callback interface for handling sitemap URLs found in robots.txt.
type SitemapDiscoverer interface {
	FoundSitemap(sitemapURL string)
}

// robotsCacheEntry is a cached robots.txt lookup. A successful fetch has a nil
// expires (kept for the process life); a failed fetch stores nil data with a
// non-zero expires so it is re-attempted once the negative TTL elapses.
type robotsCacheEntry struct {
	data    *robotstxt.RobotsData
	expires time.Time
}

// RobotsHandler manages fetching, caching, and querying robots.txt data.
type RobotsHandler struct {
	fetcher         HTTPFetcher
	rateLimiter     *RateLimiter
	robotsCache     map[string]robotsCacheEntry
	robotsCacheMu   sync.Mutex
	inflight        singleflight.Group
	globalSemaphore *semaphore.Weighted
	hostSemPool     *HostSemaphorePool
	sitemapNotifier SitemapDiscoverer
	cfg             *config.AppConfig
	log             *slog.Logger
}

func NewRobotsHandler(
	fetcher HTTPFetcher,
	rateLimiter *RateLimiter,
	globalSemaphore *semaphore.Weighted,
	hostSemPool *HostSemaphorePool,
	sitemapNotifier SitemapDiscoverer,
	cfg *config.AppConfig,
	log *slog.Logger,
) *RobotsHandler {
	return &RobotsHandler{
		fetcher:         fetcher,
		rateLimiter:     rateLimiter,
		robotsCache:     make(map[string]robotsCacheEntry),
		globalSemaphore: globalSemaphore,
		hostSemPool:     hostSemPool,
		sitemapNotifier: sitemapNotifier,
		cfg:             cfg,
		log:             log,
	}
}

// lookupCache returns cached robots data for host and whether a live entry
// exists. A negative entry past its TTL is reported as absent so the caller
// re-fetches.
func (rh *RobotsHandler) lookupCache(host string) (*robotstxt.RobotsData, bool) {
	rh.robotsCacheMu.Lock()
	defer rh.robotsCacheMu.Unlock()
	entry, found := rh.robotsCache[host]
	if !found {
		return nil, false
	}
	if !entry.expires.IsZero() && !time.Now().Before(entry.expires) {
		return nil, false
	}
	return entry.data, true
}

func (rh *RobotsHandler) cacheSuccess(host string, data *robotstxt.RobotsData) {
	rh.robotsCacheMu.Lock()
	rh.robotsCache[host] = robotsCacheEntry{data: data}
	rh.robotsCacheMu.Unlock()
}

func (rh *RobotsHandler) cacheFailure(host string) {
	rh.robotsCacheMu.Lock()
	rh.robotsCache[host] = robotsCacheEntry{expires: time.Now().Add(robotsNegativeTTL)}
	rh.robotsCacheMu.Unlock()
}

// GetRobotsData returns parsed robots.txt for the host, fetching and caching on first call.
// Returns nil on any fetch/parse error (callers should treat nil as "allow all").
// signalChan, if non-nil, receives true when the fetch completes (used for startup coordination).
func (rh *RobotsHandler) GetRobotsData(targetURL *url.URL, signalChan chan<- bool, ctx context.Context) *robotstxt.RobotsData {
	if ctx == nil {
		ctx = context.Background()
	}
	if signalChan != nil {
		defer func() {
			select {
			case signalChan <- true:
			default:
				rh.log.Warn("Failed robots signalChan send")
			}
		}()
	}

	// Host (with port), not Hostname: robots.txt is per-origin, so a site on a
	// non-default port must fetch its own host:port/robots.txt and cache under
	// that same key. Hostname() drops the port and silently fails such sites.
	host := targetURL.Host
	hostLog := rh.log.With("host", host)

	if data, found := rh.lookupCache(host); found {
		return data
	}

	// The crawler primes robots.txt on one goroutine while seeding URLs on
	// another, so without collapsing concurrent misses every worker that starts
	// before the priming fetch returns issues its own duplicate request.
	v, _, _ := rh.inflight.Do(host, func() (any, error) {
		if data, found := rh.lookupCache(host); found {
			return data, nil
		}
		return rh.fetchAndCacheRobots(targetURL, host, hostLog, ctx), nil
	})
	data, _ := v.(*robotstxt.RobotsData)
	return data
}

// fetchAndCacheRobots performs the robots.txt request for one host and records
// the outcome in the cache. Callers must hold the singleflight slot for host.
func (rh *RobotsHandler) fetchAndCacheRobots(targetURL *url.URL, host string, hostLog *slog.Logger, ctx context.Context) *robotstxt.RobotsData {
	robotsURL := &url.URL{Scheme: targetURL.Scheme, Host: host, Path: "/robots.txt"}
	if targetURL.Scheme != "http" && targetURL.Scheme != "https" {
		hostLog.Warn(fmt.Sprintf("Invalid scheme '%s', defaulting to https for robots.txt", targetURL.Scheme))
		robotsURL.Scheme = "https"
	}
	robotsURLStr := robotsURL.String()
	robotsLog := hostLog.With("robots_url", robotsURLStr)
	robotsLog.Info("Fetching robots.txt...")

	semTimeout := config.DefaultSemaphoreAcquireTimeout

	// Host semaphore first, then global, matching the page and image fetch
	// paths so robots.txt counts against max_requests_per_host like any other
	// request rather than slipping over the cap.
	if rh.hostSemPool != nil {
		ctxHost, cancelHost := context.WithTimeout(ctx, semTimeout)
		hostErr := rh.hostSemPool.Acquire(ctxHost, host)
		cancelHost()
		if hostErr != nil {
			robotsLog.Error(fmt.Sprintf("Error acquiring host semaphore: %v", hostErr))
			rh.cacheFailure(host)
			return nil
		}
		defer rh.hostSemPool.Release(host)
	}

	acquiredSemaphore := false
	robotsLog.Debug("Acquiring global semaphore...")
	ctxAcquire, cancelAcquire := context.WithTimeout(ctx, semTimeout)
	err := rh.globalSemaphore.Acquire(ctxAcquire, 1)
	cancelAcquire()
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error acquiring global semaphore: %v", err))
		rh.cacheFailure(host)
		return nil
	}
	acquiredSemaphore = true
	robotsLog.Debug("Acquired global semaphore.")
	defer func() {
		if acquiredSemaphore {
			rh.globalSemaphore.Release(1)
			robotsLog.Debug("Released global semaphore.")
		}
	}()

	rh.rateLimiter.ApplyDelay(ctx, host, rh.cfg.DefaultDelayPerHost)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURLStr, nil)
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error creating request: %v", err))
		rh.cacheFailure(host)
		return nil
	}
	req.Header.Set("User-Agent", rh.cfg.DefaultUserAgent)

	resp, fetchErr := rh.fetcher.FetchWithRetry(req, ctx)

	if fetchErr != nil {
		// The fetcher returns the response alongside 4xx/5xx errors; close it to
		// avoid a leak. A 404 (no robots.txt) means allow-all -- log it quietly.
		if resp != nil {
			resp.Body.Close()
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			robotsLog.Info("No robots.txt found (404); allowing all for host")
		} else {
			robotsLog.Warn(fmt.Sprintf("Fetching robots.txt failed, allowing all for host: %v", fetchErr))
		}
		rh.cacheFailure(host)
		return nil
	}
	defer resp.Body.Close()

	const maxRobotsSize = 1 * 1024 * 1024 // 1 MB
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxRobotsSize))
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error reading body: %v", err))
		rh.cacheFailure(host)
		return nil
	}

	data, err := robotstxt.FromBytes(bodyBytes)
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error parsing content: %v", err))
		rh.cacheFailure(host)
		return nil
	}

	robotsLog.Info("Successfully fetched and parsed robots.txt")
	rh.cacheSuccess(host, data)

	if rh.sitemapNotifier != nil && len(data.Sitemaps) > 0 {
		robotsLog.Info(fmt.Sprintf("Found %d sitemap directive(s)", len(data.Sitemaps)))
		for _, sitemapURL := range data.Sitemaps {
			rh.sitemapNotifier.FoundSitemap(sitemapURL)
		}
	}

	return data
}

// TestAgent reports whether the given user agent is allowed to access targetURL.
// Returns true if no robots data could be obtained (fail-open).
func (rh *RobotsHandler) TestAgent(targetURL *url.URL, userAgent string, ctx context.Context) bool {
	robotsData := rh.GetRobotsData(targetURL, nil, ctx)
	if robotsData == nil {
		return true
	}
	return robotsData.TestAgent(targetURL.RequestURI(), userAgent)
}
