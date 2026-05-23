package fetch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"

	"github.com/temoto/robotstxt"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
)

// SitemapDiscoverer is the callback interface for handling sitemap URLs found in robots.txt.
type SitemapDiscoverer interface {
	FoundSitemap(sitemapURL string)
}

// RobotsHandler manages fetching, caching, and querying robots.txt data.
type RobotsHandler struct {
	fetcher         HTTPFetcher
	rateLimiter     *RateLimiter
	robotsCache     map[string]*robotstxt.RobotsData // hostname -> parsed data (nil = fetch failed)
	robotsCacheMu   sync.Mutex
	globalSemaphore *semaphore.Weighted
	sitemapNotifier SitemapDiscoverer
	cfg             *config.AppConfig
	log             *slog.Logger
}

// NewRobotsHandler creates a RobotsHandler.
func NewRobotsHandler(
	fetcher HTTPFetcher,
	rateLimiter *RateLimiter,
	globalSemaphore *semaphore.Weighted,
	sitemapNotifier SitemapDiscoverer,
	cfg *config.AppConfig,
	log *slog.Logger,
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

	host := targetURL.Hostname()
	hostLog := rh.log.With("host", host)

	rh.robotsCacheMu.Lock()
	robotsData, found := rh.robotsCache[host]
	rh.robotsCacheMu.Unlock()
	if found {
		return robotsData
	}

	robotsURL := &url.URL{Scheme: targetURL.Scheme, Host: host, Path: "/robots.txt"}
	if targetURL.Scheme != "http" && targetURL.Scheme != "https" {
		hostLog.Warn(fmt.Sprintf("Invalid scheme '%s', defaulting to https for robots.txt", targetURL.Scheme))
		robotsURL.Scheme = "https"
	}
	robotsURLStr := robotsURL.String()
	robotsLog := hostLog.With("robots_url", robotsURLStr)
	robotsLog.Info("Fetching robots.txt...")

	semTimeout := config.DefaultSemaphoreAcquireTimeout
	acquiredSemaphore := false
	robotsLog.Debug("Acquiring global semaphore...")
	ctxAcquire, cancelAcquire := context.WithTimeout(ctx, semTimeout)
	err := rh.globalSemaphore.Acquire(ctxAcquire, 1)
	cancelAcquire()
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error acquiring global semaphore: %v", err))
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock() // Cache failure
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
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}
	req.Header.Set("User-Agent", rh.cfg.DefaultUserAgent)

	resp, fetchErr := rh.fetcher.FetchWithRetry(req, ctx)
	rh.rateLimiter.UpdateLastRequestTime(host)

	if fetchErr != nil {
		robotsLog.Error(fmt.Sprintf("Fetching robots.txt failed: %v", fetchErr))
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}
	defer resp.Body.Close()

	const maxRobotsSize = 1 * 1024 * 1024 // 1 MB
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxRobotsSize))
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error reading body: %v", err))
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}

	data, err := robotstxt.FromBytes(bodyBytes)
	if err != nil {
		robotsLog.Error(fmt.Sprintf("Error parsing content: %v", err))
		rh.robotsCacheMu.Lock()
		rh.robotsCache[host] = nil
		rh.robotsCacheMu.Unlock()
		return nil
	}

	robotsLog.Info("Successfully fetched and parsed robots.txt")
	rh.robotsCacheMu.Lock()
	rh.robotsCache[host] = data
	rh.robotsCacheMu.Unlock()

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
